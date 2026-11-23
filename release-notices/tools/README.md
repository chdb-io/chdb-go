# Release notice tools

These scripts build the three files in `release-notices/<engine>/` and the
compliance records in `docs/compliance/` from the pinned chdb-core sources.
They read license texts only with `git show <pinned commit>:<path>`, never a
working tree, so a run is reproducible: from the inputs in
`release-notices/inputs/<engine>/` they write the committed files byte for byte.

| Script | Writes |
| --- | --- |
| `verify_urls.py` | `verified-urls.json`: each pinned commit checked, read-only, at its `.gitmodules` URL |
| `generate.py` | `THIRD_PARTY_NOTICES.txt`, `SOURCE_PROVENANCE.json`, `STATIC_RELINKING.md` and `records.json` |
| `build_inventory.py` | `docs/compliance/static-<engine>-inventory.json` and `-obligations.tsv` |

The tools check delivery and integrity. Which files a component needs, and which
alternative of a dual license applies, is a review decision recorded in the spec.

## Inputs for one engine release

`release-notices/inputs/<engine>/`:

- `spec.json`: the engine, its core commit, and per family the license, platforms
  and the files to include (`{"module": "<submodule path or .>", "path": ...}`,
  optionally with a line range, or `{"upstream": "<owner/repo>@<commit>:<path>"}`
  for a file outside the core tree, served from `upstream/` and pinned by
  SHA-256 in `upstream_files`).
- `verified-urls.json`: written by `verify_urls.py`.
- `STATIC_RELINKING.md`: the relinking guide, copied as is.
- `upstream/`: cached upstream files named in the spec.

## Reproduce the current release

```sh
python3 release-notices/tools/generate.py \
  --core /path/to/chdb-core \
  --spec release-notices/inputs/v26.11.2-rc.1/spec.json \
  --release static-release.json \
  --packager-commit 55744f0a70a4a5336d501e7a5dba837436136a28 \
  --verified release-notices/inputs/v26.11.2-rc.1/verified-urls.json \
  --relinking release-notices/inputs/v26.11.2-rc.1/STATIC_RELINKING.md \
  --out /tmp/notices-check --records /tmp/notices-check.records.json
cmp /tmp/notices-check/THIRD_PARTY_NOTICES.txt release-notices/v26.11.2-rc.1/THIRD_PARTY_NOTICES.txt
```

`--core` is any chdb-core clone that has the core commit and the pinned submodule
commits (`git submodule update` once; the working tree does not matter).
`--packager-commit` is the chdb-go commit recorded as the packager in
`SOURCE_PROVENANCE.json`; changing it changes that file's bytes.

## A new engine baseline

1. Copy the previous `inputs/<engine>/` to the new engine's name. Set `engine`
   and `core_commit` in `spec.json`.
2. Find what changed in the payload, not only in the source tree:
   - submodule pointers: `git diff --raw <old> <new> | grep 160000`;
   - macOS: archive member groups (the prefix before `__` in `ar -t` names)
     against the previous inventory's `groups`;
   - Linux split archives: defined symbols, local ones included
     (`nm --defined-only`, not `-g`), for the anchors in `build_inventory.py`.
   A name in a symbol is not proof of a library (ClickHouse settings mention
   jemalloc); check for library-level symbols.
3. For a new or changed family, add or update its entry and files in `spec.json`.
   A new macOS member group also needs its family in `build_inventory.py`
   (`group_family`).
4. `python3 tools/verify_urls.py /path/to/chdb-core <core commit> inputs/<engine>/verified-urls.json`
5. Run `generate.py` with `--out release-notices/<engine>` (the directory must not exist).
6. Run `build_inventory.py` with the previous inventory, the new `records.json`
   and an assets file like `assets.example.json` (absolute paths to the
   downloaded tarballs, extracted archives, `ar -t` member lists and
   `nm --defined-only` symbol lists; Linux entries take the split manifest).
7. Review the diff of the three files and the records, then package with
   `-notices release-notices/<engine>`.
