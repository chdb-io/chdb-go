import test from "node:test";
import assert from "node:assert/strict";
import worker from "./worker.mjs";

const prefix = "go.example.com/chdb";
const version = "v0.261101.1";
const key = `proxy/${prefix}/static/@v/${version}.zip`;
function env() {
  const calls = [];
  return {
    MODULE_PREFIX: prefix,
    calls,
    MODULES: {
      async get(k) { calls.push(["get", k]); return k === key ? { size: 3, httpEtag: '"abc"', body: new Uint8Array([1, 2, 3]) } : null; },
      async head(k) { calls.push(["head", k]); return k === key ? { size: 3, httpEtag: '"abc"' } : null; },
      async list(options) {
        calls.push(["list", options]);
        return { objects: [{ key: `proxy/${prefix}/static/@v/${version}.info` }, { key }], truncated: false };
      },
    },
  };
}
function request(path, options) { return new Request(`https://go.example.com${path}`, options); }

test("vanity discovery points to the proxy without credentials or GOPROXY settings", async () => {
  const e = env();
  const response = await worker.fetch(request("/chdb/static?go-get=1"), e);
  assert.equal(response.status, 200);
  assert.match(await response.text(), /go-import" content="go\.example\.com\/chdb mod https:\/\/go\.example\.com/);
  assert.deepEqual(e.calls, []);
});
test("streams a versioned module with immutable caching", async () => {
  const e = env();
  const response = await worker.fetch(request(`/${prefix}/static/@v/${version}.zip`), e);
  assert.equal(response.status, 200);
  assert.deepEqual(new Uint8Array(await response.arrayBuffer()), new Uint8Array([1, 2, 3]));
  assert.match(response.headers.get("Cache-Control"), /immutable/);
  assert.deepEqual(e.calls, [["get", key]]);
});
test("HEAD reads only metadata, and ETag supports revalidation", async () => {
  const e = env();
  const response = await worker.fetch(request(`/${prefix}/static/@v/${version}.zip`, { method: "HEAD" }), e);
  assert.equal(await response.text(), "");
  assert.equal(response.headers.get("Content-Length"), "3");
  assert.deepEqual(e.calls, [["head", key]]);
  const cached = await worker.fetch(request(`/${prefix}/static/@v/${version}.zip`, { headers: { "If-None-Match": '"abc"' } }), e);
  assert.equal(cached.status, 304);
});
test("lists only immutable completed .info versions, including pagination", async () => {
  const e = env();
  let page = 0;
  e.MODULES.list = async (options) => {
    page++;
    assert.equal(options.prefix, `proxy/${prefix}/static/@v/`);
    if (page === 1) return { objects: [{ key }], truncated: true, cursor: "page2" };
    assert.equal(options.cursor, "page2");
    return { objects: [{ key: `proxy/${prefix}/static/@v/${version}.info` }], truncated: false };
  };
  const response = await worker.fetch(request(`/${prefix}/static/@v/list`), e);
  assert.equal(await response.text(), `${version}\n`);
  assert.equal(page, 2);
});
test("never exposes unrelated R2 objects or accepts writes", async () => {
  for (const path of ["/private/secret", `/${prefix}/other/@v/list`, `/${prefix}/static/@v/../../secret`, `/${prefix}/static/@v/v0.261101.1.zip/extra`, "/chdb-other/static?go-get=1", "/chdb%2Fstatic?go-get=1"]) {
    const e = env();
    assert.equal((await worker.fetch(request(path), e)).status, 404, path);
    assert.deepEqual(e.calls, []);
  }
  const e = env();
  assert.equal((await worker.fetch(request("/chdb/static", { method: "PUT", body: "x" }), e)).status, 405);
  assert.equal((await worker.fetch(new Request("https://other.example/chdb/static?go-get=1"), e)).status, 404);
  assert.deepEqual(e.calls, []);
});
test("storage failures are not cached as missing versions", async () => {
  const e = env();e.MODULES.get = async () => { throw Error("storage down"); };
  const response = await worker.fetch(request(`/${prefix}/static/@v/${version}.zip`), e);
  assert.equal(response.status, 503);
  assert.equal(response.headers.get("Cache-Control"), null);
});
