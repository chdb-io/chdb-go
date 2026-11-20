# Optional static engine linking (experimental)

An ordinary `go build` uses purego and loads a shared `libchdb` at runtime;
it does not need cgo. The separate `lib/embedded` module packages a shared
library into the program but extracts it before loading. With
`CGO_ENABLED=1 go build -tags chdb_static`, the cgo adapter links a prebuilt
`libchdb.a` into the executable. This removes the runtime `libchdb` file
requirement, not every libc, C++ runtime, or operating-system dependency.

The adapter is pinned to the chdb-core v26.11.2-rc.1 prerelease. Its
macOS/arm64 archive has passed a local module build, engine query, Arrow
suite, `cgocheck2`, race and vendor/offline checks. A platform or runtime
baseline counts as verified only after the four-target CI matrix passes
against this release.

## Opt-in Go modules (staged, not yet published)

The repository now includes a native-module packager, Vercel Blob publication
adapter, and a small static module-discovery site. They split each authenticated
release archive into relocatable `.syso`
files, distribute those across size-bounded native modules, and generate one
small `<authorized-prefix>/static` module that selects the target's payloads.
The root module has no dependency on these modules, including under build tags.

After publication, an application will add the generated static module and
blank-import `<authorized-prefix>/static`, then build with:

```sh
CGO_ENABLED=1 go build -tags chdb_static -o myapp .
```

No native-library paths, download script, or `CGO_LDFLAGS_ALLOW` are needed in
this mode. The static adapter carries the header pinned in
`static-release.json` and checks that the linked engine reports the same
version before binding it. Do not combine the module
payloads with manual archive linker flags: that would link the engine twice.

The final public module identity and compatible published root bindings version
have not been selected here. The Vercel probe project/store is for staged public
download validation, not a published engine release. There is no published
static module to `go get` yet. See [native-module staging and publication](static-module-publishing.md)
for local commands, validation, and the remaining public-release gates.

## Where the native library comes from

The [chdb-core v26.11.2-rc.1 prerelease](https://github.com/chdb-io/chdb-core/releases/tag/v26.11.2-rc.1)
has static archives and matching `chdb.h` files for the four targets below.
It is a release candidate; these instructions move to the next stable
chdb-core release once it is pinned. Its SHA-256 digests are:

| Go target | Release asset | SHA-256 |
| --- | --- | --- |
| `linux/amd64` | `linux-x86_64-libchdb-static.tar.gz` | `7f7ff849da2f8dea820fc3914d23697e8c49e7ca483600257b66945256c2896b` |
| `linux/arm64` | `linux-aarch64-libchdb-static.tar.gz` | `87b0046248df34044d130d303194635528c69fd4a9f684ce0d5508c705bf1f06` |
| `darwin/arm64` | `macos-arm64-libchdb-static.tar.gz` | `c60fa51f4e6d4538441ab9372b4c9b38d235af015d5bc3c40c896e7c79642b64` |
| `darwin/amd64` | `macos-x86_64-libchdb-static.tar.gz` | `c9c160467da5a1a63775d627de05fb20fd697a163f78f8bde518eed7d299b726` |

The static archive is **not in the `chdb-go/v2` module**. For manual linking, download
the matching release asset once at build time, verify its digest, and point
cgo at its `libchdb.a` and `chdb.h`. A normal `go get` cannot run a download
script during `go build`. Copying all four native archives into the root Go
module would also make purego users download them. The repository's
`update_libchdb.sh` installs shared libraries, not this static archive.

The staging implementation above removes that download step once its generated
modules are published. Until then, the explicit release-asset workflow below
works independently of a module origin. Go builds do not execute dependency
download hooks equivalent to a Rust `build.rs`.

## Build an application

Run the following in your application's Go module. Select the asset and
digest from the table for the machine doing the build. Keep the extracted
directory outside the module source tree, and use a path without spaces:

```sh
set -e
CHDB_STATIC_DIR=$(mktemp -d)
ASSET=macos-arm64-libchdb-static.tar.gz
EXPECTED_SHA256=c60fa51f4e6d4538441ab9372b4c9b38d235af015d5bc3c40c896e7c79642b64
gh release download v26.11.2-rc.1 -R chdb-io/chdb-core \
  -p "$ASSET" -D "$CHDB_STATIC_DIR"
ACTUAL_SHA256=$(shasum -a 256 "$CHDB_STATIC_DIR/$ASSET" | cut -d ' ' -f 1)
test "$ACTUAL_SHA256" = "$EXPECTED_SHA256"
tar -xzf "$CHDB_STATIC_DIR/$ASSET" -C "$CHDB_STATIC_DIR"
grep -Fq '#define CHDB_VERSION "26.11.2-rc.1"' "$CHDB_STATIC_DIR/chdb.h"

CGO_ENABLED=1 \
  CGO_CFLAGS="-DCHDB_STATIC_HEADER=\"$CHDB_STATIC_DIR/chdb.h\"" \
  CGO_LDFLAGS="-Wl,-force_load,$CHDB_STATIC_DIR/libchdb.a" \
  go build -trimpath -tags chdb_static,chdb_static_manual -o myapp .
otool -L ./myapp
```

This example is for `darwin/arm64`. On `darwin/amd64`, select its asset and
digest but keep the same cgo flags. On Linux, use `sha256sum` in place of
`shasum -a 256`, and replace the final build command with:

```sh
CGO_ENABLED=1 \
  CGO_CFLAGS="-DCHDB_STATIC_HEADER=\"$CHDB_STATIC_DIR/chdb.h\"" \
  CGO_LDFLAGS_ALLOW='-Wl,(--whole-archive|--no-whole-archive)|-l:libchdb[.]a' \
  CGO_LDFLAGS="-L$CHDB_STATIC_DIR" \
  go build -trimpath -tags chdb_static,chdb_static_manual -o myapp .
readelf -d ./myapp
readelf -V ./myapp
```

`chdb_static_manual` selects manual archive linking; omit it when importing the
generated static module. On macOS, `-force_load` retains engine code reached
through static registration. On Linux, the manual adapter's `#cgo` directive uses
`-l:libchdb.a`, which selects the archive rather than a nearby shared
library. Do not repeat the whole-archive flags in `CGO_LDFLAGS`: Go
aggregates that variable across cgo packages, which can duplicate the
archive at link time. The manual directive keeps system libraries such as
`-lm` after the archive so GNU ld's `--as-needed` processing retains them.
Module mode keeps its system-library flags without adding a manual archive.
Check that `otool -L` or `readelf -d` does not list a
shared `libchdb`; run the same executable on each operating-system version
you intend to support. A Linux glibc binary does not imply musl support.

The static tag requires cgo and one of the four listed targets; unsupported
combinations fail at build time. `CHDB_LIB_PATH` cannot override a statically
linked engine, and importing `lib/embedded` alongside it is an error at
first engine use. `chdbpurego.LoadedLibraryPath()` returns
`ErrStaticallyLinked`; `chdbpurego.Version()` reports the running engine's
version. The static adapter fails at first engine use when the linked
engine's version differs from the header it was built with; always pass the
`chdb.h` from the same release as the archive. Platform CI builds, runs,
and audits these exact release assets; inspect the checks for the commit
you are using before relying on a platform-specific compatibility claim.
