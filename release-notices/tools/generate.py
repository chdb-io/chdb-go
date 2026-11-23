#!/usr/bin/env python3
"""Build THIRD_PARTY_NOTICES.txt, SOURCE_PROVENANCE.json and the file records
for the chdb-go static modules from a chdb-core checkout.

Every license text is read with `git show <pinned commit>:<path>`: superproject
files at the core commit, submodule files at the commit the core commit pins
for that submodule. The working tree is never read. Upstream files outside the
core repository come from a local cache whose SHA-256 is pinned in the spec.

Usage: generate.py --core /path/to/chdb-core --spec spec.json \
          --release /path/to/chdb-go/static-release.json \
          --packager-commit <chdb-go commit> --verified verified-urls.json \
          --relinking STATIC_RELINKING.md --out /new/dir --records records.json
"""
import argparse, hashlib, json, os, subprocess, sys

def git(core, *args):
    return subprocess.run(["git", "-C", core, *args], check=True, capture_output=True).stdout

def sha256(b):
    return hashlib.sha256(b).hexdigest()

def main():
    ap = argparse.ArgumentParser()
    for a in ("core", "spec", "release", "packager-commit", "verified", "relinking", "out", "records"):
        ap.add_argument("--" + a, required=True)
    o = ap.parse_args()
    spec = json.load(open(o.spec))
    tools = os.path.dirname(os.path.abspath(o.spec))
    core, C = o.core, spec["core_commit"]
    release = json.load(open(o.release))
    verified = json.load(open(o.verified))
    if release["engine"] != spec["engine"] or release["source_commit"] != C:
        sys.exit("static-release.json does not pin this engine and core commit")
    if os.path.exists(o.out):
        sys.exit("output must not exist")

    gitmodules = git(core, "show", f"{C}:.gitmodules").decode()
    urls, cur = {}, None
    for line in gitmodules.splitlines():
        line = line.strip()
        if line.startswith("[submodule"):
            cur = None
        elif line.startswith("path ="):
            cur = line.split("=", 1)[1].strip()
        elif line.startswith("url =") and cur:
            urls[cur] = line.split("=", 1)[1].strip()

    def pinned(module):
        out = git(core, "ls-tree", C, module).decode().split()
        if len(out) < 3 or out[1] != "commit":
            sys.exit(f"{module} is not a submodule at {C}")
        return out[2]

    def read(entry):
        if "upstream" in entry:
            u = spec["upstream_files"][entry["upstream"]]
            data = open(os.path.join(tools, u["cache"]), "rb").read()
            if sha256(data) != u["sha256"]:
                sys.exit(f"cached upstream file changed: {entry['upstream']}")
            repo, rest = entry["upstream"].split("@", 1)
            commit, path = rest.split(":", 1)
            return data, {"source": f"github.com/{repo}", "commit": commit, "path": path}
        m = entry["module"]
        if m == ".":
            commit, data = C, git(core, "show", f"{C}:{entry['path']}")
            src = {"source": "chdb-core", "commit": C, "path": entry["path"]}
        else:
            commit = pinned(m)
            data = git(os.path.join(core, m), "show", f"{commit}:{entry['path']}")
            src = {"source": m, "commit": commit, "path": entry["path"]}
        if "lines" in entry:
            a, b = entry["lines"]
            lines = data.decode("utf-8").splitlines(keepends=True)
            if b > len(lines):
                sys.exit(f"line range beyond {m}:{entry['path']}")
            data = "".join(lines[a - 1:b]).encode("utf-8")
            src["lines"] = [a, b]
        return data, src

    texts, order, records = {}, [], []
    out = []
    w = out.append
    w("THIRD-PARTY NOTICES FOR THE chDB STATIC ENGINE GO MODULES\n")
    w("=" * 60 + "\n\n")
    w(f"Engine: chdb-core {spec['engine']} (commit {C})\n")
    w("Distributed by: chdb-go static native and entry modules (go.chdb.io/chdb)\n\n")
    w("These modules carry chdb-core's prebuilt static library. This file reproduces\n"
      "the license, copyright and NOTICE texts of the components linked into it,\n"
      "read verbatim from the exact source commits that the engine commit pins.\n"
      "Lines marked 'Note:' are explanations written for this distribution, not\n"
      "license text. A component listed here is linked on the platforms named in\n"
      "its section; one shared file covers all four platforms. Identical texts are\n"
      "printed once and referenced by their [Tnn] label.\n\n")
    w("See SOURCE_PROVENANCE.json for source locations and STATIC_RELINKING.md for\n"
      "the LGPL components' source and relinking information.\n\n")

    for fam in spec["families"]:
        plat = "all four platforms" if fam["platforms"] == "all" else "Linux (amd64, arm64) only"
        w("\n" + "#" * 78 + "\n")
        w(f"# {fam['id']}\n")
        w(f"# License: {fam['license']}\n")
        w(f"# Linked on: {plat}\n")
        w("#" * 78 + "\n\n")
        if fam.get("note"):
            w("Note: " + fam["note"] + "\n\n")
        frec = []
        for entry in fam["files"]:
            data, src = read(entry)
            h = sha256(data)
            loc = f"{src['source']}@{src['commit']}:{src['path']}"
            if "lines" in src:
                loc += f" (lines {src['lines'][0]}-{src['lines'][1]})"
            src["sha256"], src["bytes"] = h, len(data)
            frec.append(src)
            if h in texts:
                w(f"--- {loc}\n    Text identical to [{texts[h]}] (sha256 {h})\n\n")
                continue
            label = f"T{len(texts) + 1:02d}"
            texts[h] = label
            w(f"--- [{label}] {loc}\n    sha256 {h}\n\n")
            t = data.decode("utf-8", errors="strict")
            w(t if t.endswith("\n") else t + "\n")
            w("\n")
        records.append({"id": fam["id"], "license": fam["license"], "platforms": fam["platforms"], "files": frec})

    os.mkdir(o.out)
    notices = "".join(out).encode("utf-8")
    open(os.path.join(o.out, "THIRD_PARTY_NOTICES.txt"), "wb").write(notices)

    components = []
    for path in sorted(urls):
        try:
            commit = pinned(path)
        except SystemExit:
            continue
        v = verified.get(path)
        components.append({"path": path, "url": urls[path], "commit": commit,
                           "commit_verified_at_url": bool(v and v.get("commit") == commit and v.get("ok"))})
    by_path = {c["path"]: c for c in components}
    prov = {
        "schema": 1,
        "engine": spec["engine"],
        "core_commit": C,
        "core_repository": "https://github.com/chdb-io/chdb-core",
        "core_commit_verified": verified.get(".", {}).get("ok", False),
        "packager": {"repository": "https://github.com/chdb-io/chdb-go", "commit": o.packager_commit,
                     "paths": ["internal/staticbundle", "scripts/staticbundle", "static-release.json"]},
        "platforms": {p: {"asset": a["asset"], "sha256": a["sha256"]} for p, a in sorted(release["platforms"].items())},
        "header_sha256": release["header_sha256"],
        "build_scripts_at_core_commit": [
            "chdb/build.sh", "chdb/build_linux_arm64.sh", "chdb/build_mac_arm64.sh", "chdb/build_mac_on_linux.sh",
            "chdb/build/build_static_lib.sh", "chdb/build/build_static_lib_mac_on_linux.sh",
            "chdb/build/create_static_libchdb.py", "chdb/build/prelink_static_lib.sh",
            "chdb/build/split_static_lib.py", "chdb/build/split_static_lib_text.py",
            "chdb/build/check_static_lib_hermetic.sh", "chdb/build/rename_static_lib_symbols_macos.py",
            ".github/workflows/build_linux_x86_wheels.yml", ".github/workflows/build_linux_arm64_wheels-gh.yml",
            ".github/workflows/build_macos_x86_wheels.yml", ".github/workflows/build_macos_arm64_wheels.yml"],
        "lgpl_components": [
            dict(by_path["contrib/mariadb-connector-c"], name="MariaDB Connector/C", license="LGPL-2.1 (COPYING.LIB); some files LGPL-2.0-or-later", platforms="all"),
            dict(by_path["contrib/libgsasl"], name="GNU SASL", license="LGPL-2.1-or-later", platforms="linux"),
        ],
        "source_delivery": {
            "status": "commit-links",
            "statement": "The corresponding source is the core commit and the submodule commits listed here, at their public repository URLs. Each url was checked to serve its exact commit (commit_verified_at_url). This includes the LGPL components MariaDB Connector/C and GNU SASL. No separate source archive is published.",
        },
        "components": components,
    }
    for f in prov["build_scripts_at_core_commit"]:
        git(core, "cat-file", "-e", f"{C}:{f}")
    open(os.path.join(o.out, "SOURCE_PROVENANCE.json"), "w").write(json.dumps(prov, indent=2) + "\n")
    rel = open(o.relinking, "rb").read()
    open(os.path.join(o.out, "STATIC_RELINKING.md"), "wb").write(rel)
    json.dump({"engine": spec["engine"], "core_commit": C, "families": records}, open(o.records, "w"), indent=2)
    for n in ("THIRD_PARTY_NOTICES.txt", "SOURCE_PROVENANCE.json", "STATIC_RELINKING.md"):
        b = open(os.path.join(o.out, n), "rb").read()
        print(f"{n}\t{len(b)}\t{sha256(b)}")

if __name__ == "__main__":
    main()
