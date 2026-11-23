# Source and relinking for the chDB static engine modules

These Go modules carry chdb-core v26.11.2-rc.1's prebuilt static library
(core commit `59a7f688d1fc8d895115e104f09a2c4071490d6a`) as `.syso` objects.
Two linked components are LGPL libraries:

- MariaDB Connector/C, on all four platforms (LGPL-2.1; some files LGPL-2.0-or-later).
- GNU SASL, on Linux only (LGPL-2.1-or-later).

Their license texts are in THIRD_PARTY_NOTICES.txt. Their exact source
commits, and those of every other component, are in SOURCE_PROVENANCE.json.

## Source availability

The corresponding source is the chdb-core repository at the core commit above,
with its submodules at the commits that commit pins. Each submodule commit in
SOURCE_PROVENANCE.json was checked to be reachable at its listed URL. The build
scripts that produced these bytes are listed there too.

This is how the source is delivered, including for the LGPL components
(MariaDB Connector/C and GNU SASL): their exact commits at the repository URLs
listed in SOURCE_PROVENANCE.json. No separate source archive is published.

## Rebuilding with a modified library

Replacing an object inside a finished `.syso` is not supported. To use a
modified LGPL library (or any other change):

1. Check out chdb-core at the core commit with all submodules, and change the
   library's source.
2. Build the static library with the same scripts: `chdb/build/build_static_lib.sh`
   (for Linux, the split archive: `chdb/build/split_static_lib.py` and its
   `libchdb-split.json`). Set `CHDB_VERSION=26.11.2-rc.1` so the engine reports
   the version of the header the Go adapter is built with.
3. Pack the archive and headers into a tarball with the release's layout.
   On macOS use `COPYFILE_DISABLE=1 tar --no-xattrs`: the packager rejects
   any member it does not expect, including `._*` metadata files.
4. Write a separate local release manifest for chdb-go's packager with the new
   archive's SHA-256 and a different packaging identity. Do not disable the
   checksum check, and do not present the modified archive as the original
   release.
5. Run `go run ./scripts/staticbundle package` and `assemble` from chdb-go with
   that manifest to generate the native and entry modules.
6. Point the application at them with `replace` directives for every affected
   module path, and rebuild the application.

Keep this work out of the public Go module cache, and never upload modified
bytes under an existing public version.

This route has been tested end to end on macOS arm64 only (2026-11-23): a
changed MariaDB Connector/C error message, rebuilt at the core commit with
`build_static_lib.sh`, packaged with packaging identity 99 and replaced into an
application that requires the published root `v2.5.0-rc.2`. The application ran
queries and reported the changed message from the MariaDB client. The other
three platforms have **not yet been tested**.

## SDK distribution and binary distribution

These modules are an SDK: they deliver the library, its notices and the means
to rebuild it. A developer who ships an executable that links them is
distributing the LGPL libraries in that binary and must meet the LGPL terms for
it, for example by enabling users to relink the application with a modified
library (application object files, or source, or another route the LGPL
allows). Opening all application source is not the only way. Each binary
distributor chooses and documents its own route; these modules do not do that
for them.
