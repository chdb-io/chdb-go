#!/usr/bin/env python3
"""Write chdb-go's compliance records for one engine release: an inventory JSON
and an obligations TSV in the shape of the v26.11.1 records.

Inputs: the previous inventory (family mapping and obligations), records.json
from generate.py (license file records), the RC assets, their member lists and
defined-symbol lists (nm --defined-only, local symbols included)."""
import argparse, collections, hashlib, json, os, tarfile

ANCHORS = {"mariadb": ["mariadb_connection", "mysql_init"], "gsasl": ["gsasl_init"], "libssh": ["ssh_new"],
           "lemmagen": ["lem_create_instance"], "liblzma": ["lzma_code"], "icu": ["u_getVersion_78"],
           "krb5": ["krb5_init_context"], "libcxx": ["__cxa_throw"], "libunwind": ["_Unwind_RaiseException"],
           "gdeflate": ["gdeflate_libdeflate_gdeflate_compress"]}

def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for b in iter(lambda: f.read(1 << 20), b""):
            h.update(b)
    return h.hexdigest()

def anchors(symbols, mach):
    found = {}
    for k, names in ANCHORS.items():
        hits = []
        for n in names:
            base = ("_" + n) if mach else n
            for cand in (base, base + ".chdb"):
                if cand in symbols:
                    hits.append(cand)
        found[k] = hits
    return found

def main():
    ap = argparse.ArgumentParser()
    for a in ("previous", "records", "release", "assets", "symbols", "out-json", "out-tsv", "review-date"):
        ap.add_argument("--" + a, required=True)
    o = ap.parse_args()
    prev = json.load(open(o.previous))
    recs = {f["id"]: f for f in json.load(open(o.records))["families"]}
    rel = json.load(open(o.release))
    assets = json.load(open(o.assets))  # platform -> {tarball, archive, members?, manifest?, symbols}

    group_family = {}
    for f in prev["families"]:
        for ev in f["archiveEvidence"].values():
            for g in ev.get("groups", []):
                group_family[g] = f["id"]
    group_family["gdeflate"] = "gdeflate"

    archives, fam_ev = [], collections.defaultdict(dict)
    for plat in ("darwin-arm64", "darwin-amd64", "linux-amd64", "linux-arm64"):
        a, r = assets[plat], rel["platforms"][plat]
        if sha256(a["tarball"]) != r["sha256"]:
            raise SystemExit(f"{plat}: asset digest differs from static-release.json")
        syms = set(open(a["symbols"]).read().split("\n")) - {""}
        if plat.startswith("darwin"):
            members = [m for m in open(a["members"]).read().split("\n") if m and not m.startswith("__.SYMDEF")]
        else:
            members = json.load(open(a["manifest"]))["link_order"]
        groups = collections.defaultdict(list)
        for m in members:
            groups[m.split("__", 1)[0] if plat.startswith("darwin") and "__" in m else
                   ("libchdb_part" if plat.startswith("linux") else "unprefixed")].append(m)
        archives.append({
            "platform": plat, "asset": r["asset"], "sha256": r["sha256"],
            "archiveBytes": os.path.getsize(a["tarball"]), "nativeBytes": os.path.getsize(a["archive"]),
            "objectMembers": len(members),
            "groups": {g: {"count": len(v), "examples": v[:4]} for g, v in sorted(groups.items())},
            "symbolCount": len(syms), "anchors": anchors(syms, plat.startswith("darwin"))})
        if plat.startswith("darwin"):
            for g, v in groups.items():
                fid = group_family.get(g)
                if fid is None:
                    raise SystemExit(f"{plat}: archive group {g} has no review family")
                ev = fam_ev[fid].setdefault(plat, {"objects": 0, "groups": []})
                ev["objects"] += len(v)
                ev["groups"].append(g)
    for ev in fam_ev.values():
        for e in ev.values():
            e["groups"].sort()

    prev_fam = {f["id"]: f for f in prev["families"]}
    prev_linux = {f["id"]: f for f in prev["linuxSpecific"]}
    families, linux = [], []
    for fid, rec in recs.items():
        def where(s):
            if s["source"] == "chdb-core":
                return "/" + s["path"]
            if s["source"].startswith("github.com/"):
                return s["source"] + ":" + s["path"]
            return "/" + s["source"] + "/" + s["path"]
        lic = [{"path": where(s), "source": s["source"], "commit": s["commit"], "textSHA256": s["sha256"],
                **({"lines": s["lines"]} if "lines" in s else {})} for s in rec["files"]]
        if rec["platforms"] == "linux":
            p = prev_linux.get(fid, {})
            linux.append({"id": fid, "license": rec["license"],
                          "commit": next((s["commit"] for s in rec["files"] if s["source"].startswith("contrib/")), None),
                          "evidence": p.get("evidence", ""), "obligation": p.get("obligation", ""), "licenseRecords": lic})
            continue
        p = prev_fam.get(fid)
        obligation = p["obligation"] if p else prev_fam["brotli"]["obligation"]  # same MIT treatment
        families.append({"id": fid, "reviewLicense": rec["license"], "obligation": obligation,
                         "archiveEvidence": dict(sorted(fam_ev.get(fid, {}).items())), "licenseRecords": lic})
    missing = set(fam_ev) - {f["id"] for f in families}
    if missing:
        raise SystemExit(f"archive families without license records: {sorted(missing)}")

    inv = {
        "schema": 1, "reviewDate": o.review_date,
        "scope": prev["scope"], "engine": rel["engine"], "coreCommit": rel["source_commit"],
        "changesFromPrevious": {
            "previousEngine": prev["engine"], "previousCoreCommit": prev["coreCommit"],
            "addedFamilies": ["gdeflate"],
            "submodulesChanged": {"contrib/llvm-project": "140ad525c086cfe79c8dcf33302a4f168d5a187b -> fa3910d62714",
                                  "contrib/jemalloc": "bumped; not linked (only DB::ServerSetting names match 'jemalloc')",
                                  "contrib/gdeflate": "added; linked on all four platforms",
                                  "contrib/arrow-testing": "added; test data, not linked"},
            "removedMacGroups": ["chdb_func", "libdarwin-compatibility"],
            "licenseFileCorrections": "License records now name the files actually reproduced in THIRD_PARTY_NOTICES.txt (e.g. xz COPYING/COPYING.0BSD, rocksdb LICENSE.Apache/LICENSE.leveldb, lz4 lib/LICENSE, each AWS C library, LLVM runtime LICENSE files)."},
        "evidenceLimits": [
            "Both macOS archives retain library-prefixed members and are fully covered by the target-family mapping; an unmapped group fails record generation.",
            "Linux archives are chdb-core's split archive: nine relocatable shards each, cut across library boundaries. Critical LGPL and platform-specific libraries are verified by defined symbol anchors; remaining libraries are not individually reconstructed from the shards.",
            "symbolCount counts distinct defined symbol names (nm --defined-only, local symbols included), so it is not comparable with the v26.11.1 counts. The split archive makes most symbols local, so anchors are matched with or without the .chdb suffix.",
            prev["evidenceLimits"][2], prev["evidenceLimits"][3]],
        "archives": archives, "families": families, "linuxSpecific": linux,
        "notPromotedToBlockers": prev["notPromotedToBlockers"] + [
            {"id": "jemalloc", "reason": "No je_*/mallctl definitions in any archive; only DB::ServerSetting names contain 'jemalloc'"},
            {"id": "libdeflate", "reason": "No contrib/libdeflate archive group; every libdeflate_* definition is gdeflate's renamed copy"},
            {"id": "arrow-testing", "reason": "Test data submodule; not built into the library"}],
    }
    json.dump(inv, open(o.out_json, "w"), indent=2)
    open(o.out_json, "a").write("\n")

    with open(o.out_tsv, "w") as t:
        t.write("family\tmac_arm64_objects\tmac_amd64_objects\treview_license\tobligation\tupstream_commit\tlicense_paths\n")
        for f in families:
            ev = f["archiveEvidence"]
            commits = sorted({r["commit"] for r in f["licenseRecords"] if r["source"].startswith("contrib/")})
            t.write("\t".join([f["id"], str(ev.get("darwin-arm64", {}).get("objects", 0)), str(ev.get("darwin-amd64", {}).get("objects", 0)),
                               f["reviewLicense"], f["obligation"], ";".join(commits) or "core commit",
                               "; ".join(r["path"] for r in f["licenseRecords"]) or "see note in THIRD_PARTY_NOTICES.txt"]) + "\n")
        for f in linux:
            t.write("\t".join([f["id"], "Linux only", "Linux only", f["license"], f["obligation"], f["commit"] or "core commit",
                               "; ".join(r["path"] for r in f["licenseRecords"])]) + "\n")

if __name__ == "__main__":
    main()
