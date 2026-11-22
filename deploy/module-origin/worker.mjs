// Public read-only vanity discovery and GOPROXY endpoint. Bind a private R2
// bucket as MODULES; the Worker exposes only the configured module namespace.
// The same host serves /chdb/static?go-get=1 and the protocol's full module
// paths, e.g. /go.example.com/chdb/static/@v/v0.261101.1.zip.
const prefixPattern = /^[a-z0-9]+(?:[.-][a-z0-9]+)+(?:\/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$/;
const versionPattern = /^v0\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-rc\.[1-9][0-9]*\.[1-9][0-9]*)?$/;

export default {
  async fetch(request, env) {
    if (request.method !== "GET" && request.method !== "HEAD") {
      return new Response("read only\n", { status: 405, headers: { Allow: "GET, HEAD" } });
    }
    const prefix = env.MODULE_PREFIX;
    if (typeof prefix !== "string" || !prefixPattern.test(prefix) || prefix.includes("..")) {
      return new Response("module origin not configured\n", { status: 503 });
    }
    const url = new URL(request.url);
    const [host, ...segments] = prefix.split("/");
    if (url.hostname !== host || url.pathname.includes("%") || url.pathname.includes("\\")) {
      return new Response("not found\n", { status: 404 });
    }
    const vanityRoot = segments.length ? `/${segments.join("/")}` : "";
    const vanityPath = url.pathname === vanityRoot || url.pathname.startsWith(`${vanityRoot}/`);
    if (vanityPath && !url.pathname.includes("/@v/") && !url.pathname.endsWith("/@latest")) {
      const html = `<!doctype html><html><head><meta name="go-import" content="${prefix} mod https://${host}"></head><body>chDB native Go modules</body></html>`;
      return new Response(request.method === "HEAD" ? null : html, {
        headers: { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "public, max-age=300" },
      });
    }
    const protocolRoot = `/${prefix}/`;
    if (!url.pathname.startsWith(protocolRoot)) return new Response("not found\n", { status: 404 });
    const rest = url.pathname.slice(protocolRoot.length);
    const match = /^(static|libchdb\/(?:darwin|linux)-(?:amd64|arm64)\/part(?:0|[1-9][0-9]*))\/@v\/(.+)$/.exec(rest);
    if (!match) return new Response("not found\n", { status: 404 });
    const module = `${prefix}/${match[1]}`;
    const filename = match[2];
    try {
      if (filename === "list") {
        // No mutable list or @latest files are uploaded. Publishing .info last
        // makes a completed version discoverable without rewriting old bytes.
        const objectPrefix = `proxy/${module}/@v/`;
        const versions = [];
        let cursor;
        for (let page = 0; page < 100; page++) {
          const result = await env.MODULES.list({ prefix: objectPrefix, cursor });
          for (const object of result.objects) {
            const name = object.key.slice(objectPrefix.length);
            if (name.endsWith(".info") && versionPattern.test(name.slice(0, -5))) versions.push(name.slice(0, -5));
          }
          if (!result.truncated) {
            if (!versions.length) return new Response("not found\n", { status: 404 });
            return new Response(request.method === "HEAD" ? null : `${versions.sort().join("\n")}\n`, {
              headers: { "Content-Type": "text/plain; charset=utf-8", "Cache-Control": "public, max-age=60" },
            });
          }
          cursor = result.cursor;
        }
        return new Response("version listing too large\n", { status: 503 });
      }
      const file = /^(.*)\.(zip|mod|info)$/.exec(filename);
      if (!file || !versionPattern.test(file[1])) return new Response("not found\n", { status: 404 });
      const key = `proxy/${module}/@v/${filename}`;
      const object = request.method === "HEAD" ? await env.MODULES.head(key) : await env.MODULES.get(key);
      if (!object) return new Response("not found\n", { status: 404 });
      const headers = new Headers({
        "Content-Type": file[2] === "zip" ? "application/zip" : file[2] === "info" ? "application/json" : "text/plain; charset=utf-8",
        "Content-Length": String(object.size),
        "Cache-Control": "public, max-age=31536000, immutable",
        ETag: object.httpEtag,
        "X-Content-Type-Options": "nosniff",
      });
      if (request.headers.get("If-None-Match") === object.httpEtag) return new Response(null, { status: 304, headers });
      return new Response(request.method === "HEAD" ? null : object.body, { headers });
    } catch {
      return new Response("module storage unavailable\n", { status: 503 });
    }
  },
};
