#!/usr/bin/env python3
"""Check, read-only, that each pinned commit is reachable at its .gitmodules URL
on GitHub (gh api -X GET repos/<owner>/<repo>/commits/<sha>). Writes a JSON map
{path: {url, commit, ok}}; "." is the core commit at chdb-io/chdb-core."""
import json, re, subprocess, sys
core, C, out = sys.argv[1], sys.argv[2], sys.argv[3]
def git(*a): return subprocess.run(["git", "-C", core, *a], check=True, capture_output=True, text=True).stdout
urls, cur = {}, None
for line in git("show", f"{C}:.gitmodules").splitlines():
    line = line.strip()
    if line.startswith("[submodule"): cur = None
    elif line.startswith("path ="): cur = line.split("=", 1)[1].strip()
    elif line.startswith("url =") and cur: urls[cur] = line.split("=", 1)[1].strip()
res = {}
def check(path, url, sha):
    m = re.match(r"https://github\.com/([^/]+)/([^/]+?)(?:\.git)?/?$", url)
    ok = False
    if m:
        r = subprocess.run(["gh", "api", "-X", "GET", f"repos/{m[1]}/{m[2]}/commits/{sha}", "-q", ".sha"], capture_output=True, text=True)
        ok = r.returncode == 0 and r.stdout.strip() == sha
    res[path] = {"url": url, "commit": sha, "ok": ok}
check(".", "https://github.com/chdb-io/chdb-core", C)
for path, url in sorted(urls.items()):
    t = git("ls-tree", C, path).split()
    if len(t) >= 3 and t[1] == "commit":
        check(path, url, t[2])
json.dump(res, open(out, "w"), indent=2)
bad = [p for p, v in res.items() if not v["ok"]]
print(f"{len(res)} checked, {len(res) - len(bad)} verified; not verified: {bad}")
