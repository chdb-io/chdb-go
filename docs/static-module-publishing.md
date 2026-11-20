# Static engine modules: staging and publication

## Current path: Vercel Blob, tiny public probe first

The native split/ZIP packager remains unchanged. Publication now follows the
Maven pattern: Vercel Blob stores immutable versioned files, and a tiny Vercel
static site serves module discovery, version lists and external rewrites.
See [Vercel module publication](../deploy/vercel-modules/README.md) for the current
commands and staged acceptance checks. The R2 implementation below remains an
optional adapter; it is not a prerequisite for the Vercel path.

`probe`, `blob-plan` and `blob-site` write local staging data only.
`blob-publish` is a remote write and needs a fresh destination-bound confirmation.
`verify-public` is read-only remote validation: anonymous HTTP, compiled-in
default Go proxy and checksum database, cold-cache `go get`, and an offline
rebuild. A real engine upload is rejected unless a passed tiny-probe report
matches the selected site, store and module namespace. No CI publishes either
the tiny probe or engine modules automatically.

The small probe has two versions, each with `.zip`, `.mod`, and `.info` (six
Blob objects). Its `list` is served by the Vercel deployment, not uploaded as a
mutable Blob key. When deploying another release, pass the previous site's
`catalog.json` so existing module versions remain discoverable. Do not delete
published Go modules as a failed-upload rollback: Go proxies/checksum records
can outlive the uploading process.

The root binding release must still be published before a public static entry
can use it. Passing a tiny probe does not establish native-platform compatibility
or turn a synthetic local bindings version into a public release.

## Optional R2 adapter and common packaging details

Status: implementation and local validation, not a published service. No domain
or R2 bucket is provisioned by these commands. `package`, `assemble`, `verify`
and `plan` are local only. Only an explicit, separately confirmed `publish`
uploads to R2. No command pushes Git, creates tags, changes credentials or
deploys a Worker.

## Layout and user contract

Source, manifests and CI live in GitHub. Versioned module artifacts live in R2
behind an authorized chDB/ClickHouse vanity domain. Both the entry module and
native modules use that vanity prefix; a `github.com/...` import path cannot be
silently redirected to an unrelated R2 origin. The prefix is an explicit input,
not a hardcoded claim of domain ownership.

For a hypothetical `go.example.com/chdb` prefix, the generated modules are:

```
go.example.com/chdb/static
go.example.com/chdb/libchdb/darwin-arm64/part0
go.example.com/chdb/libchdb/darwin-arm64/part1
... equivalent independently sized parts for the other platforms
```

`example.com` and `static-ci.example` are test identities, not public releases.
After a real publication, an application adds `<prefix>/static` at a fixed
version, blank-imports it, and builds with `CGO_ENABLED=1 go build -tags chdb_static`.
The application need not know how many payload modules or files exist. There
are no runtime downloads, extraction into the module cache, or library paths to
set. A normal C compiler/linker and compatible system runtime remain necessary.

The root bindings module never imports the static entry module, even under
`chdb_static`. This keeps native modules out of default purego consumers'
`go mod tidy`. Static consumers' tidy/vendor may collect other platforms too;
only normal target builds select a single platform. This is not a promise of
one-platform downloads for every Go command. `go mod vendor` and offline
`-mod=vendor` builds work, but a vendor directory holds native files over
100 MiB, so it cannot be committed as is to an ordinary GitHub repository.

## Pinned, reproducible packaging

`static-release.json` records upstream assets, SHA-256 digests, engine release,
packaging revision, timestamp, the exact adapter-header digest and the
chdb-core source commit. The module version uses `internal/enginetag`, e.g.
engine v26.11.1, packaging 1 becomes v0.261101.1, and engine v26.11.2-rc.1
becomes v0.261102.0-rc.1.1. Change the packaging revision whenever any published bytes change.

On each target's native runner:

1. Download its pinned `chdb-core` tar.gz. Packaging checks the full archive
   digest before extraction and verifies the extracted header against the
   adapter's checked-in `chdb-purego/static_chdb.h`.
2. Read ordinary GNU/BSD/Darwin archives by member offset. Preserve duplicate
   object filenames rather than allowing `ar x` to overwrite them. Reject thin
   archives, bitcode, wrong architectures and malformed inputs.
3. On Darwin, group approximately 64 MiB of objects per relocatable `.syso`
   with `ld -r -keep_private_externs`, preserve the strictest input deployment
   target, and recursively split oversized groups. On Linux, use chdb-core's
   split archive (`*-libchdb-static-split.tar.gz`, chdb-io/chdb-core#342): its
   `libchdb-split.json` must match the pinned engine version, source commit and
   architecture, and the archive's members must match the manifest's link
   order, sizes and SHA-256 digests. Each member is copied to one `.syso`
   unchanged; it is never relinked, renamed or stripped. Inputs are object
   files, so no consumer `--whole-archive`/`-force_load` flag is needed.
4. Pack modules below 384 MiB uncompressed. Individual native files are at most
   256 MiB (chdb-core's Linux shards come out near 200 MiB), so a module holds
   at most one such shard beside a smaller one; both module zip and
   uncompressed contents remain below Go's 500 MiB ceiling. Those checks include the final symbol tables, not just source-object
   sizes. Copy the repository license into each module.
5. Emit `.info`, `.mod`, `.zip` and a digest/size inventory in `bundle.json`.
   Do not overwrite existing staging directories or place binaries in the repo.

Example on macOS/arm64, with an already downloaded archive:

```sh
go run ./scripts/staticbundle package \
  -archive /absolute/path/macos-arm64-libchdb-static.tar.gz \
  -module-prefix static-ci.example/chdb \
  -bindings-version v2.99.0-static-ci \
  -out /absolute/temporary/parent/native-darwin-arm64
```

The output directory must not already exist and its parent must exist. Real
publication must replace both the prefix and the synthetic bindings version
with authorized, publicly resolvable identities. The root bindings release must
include this implementation; an older release is not made compatible merely by
putting its version in the manifest.

Native modules contain immutable engine/packaging identities. The generated
entry module checks every selected part against its expected versions, so MVS
cannot silently mix native packagings. The adapter separately checks the linked
engine's runtime version before binding its C ABI.

## Assemble and validate

Combine all four native-runner outputs with the same prefix, engine, packaging,
timestamp, header and bindings version:

```sh
go run ./scripts/staticbundle assemble \
  -input /tmp/native-darwin-amd64,/tmp/native-darwin-arm64,/tmp/native-linux-amd64,/tmp/native-linux-arm64 \
  -out /tmp/chdb-static-complete
```

For development on one machine only, `-allow-partial` creates an entry module
for the available target. Other targets deliberately fail to compile, and
`plan` refuses partial bundles. Partial is not a production release.

```sh
go run ./scripts/staticbundle verify \
  -bundle /tmp/chdb-static-complete \
  -out /tmp/chdb-static-consumer-check
```

The verifier snapshots current root source using git's tracked/non-ignored
file inventory (including new working-tree implementation files), excludes
nested Go modules, and refuses native payloads in the root snapshot. CI runs
against its committed checkout; local runs intentionally test uncommitted work.
It constructs a separate local proxy and cold cache inside the verification
directory, without changing the staged release. It then:

- resolves a real external static consumer with no workspace or replacements;
- rebuilds with `GOPROXY=off`, clearing native path and linker-allow flags;
- executes an aggregate query, checks the linked engine identity, and audits
  runtime dependencies for dynamic libchdb;
- checks that `-trimpath` binaries do not contain the checkout/staging paths;
- runs `go mod verify`;
- vendors the dependencies and builds/runs offline again, checking that headers
  and `.syso` payloads survive vendoring;
- uses a second cold cache for a purego consumer's tidy and build, and checks
  that no module from the native namespace was acquired.

`GOSUMDB=off` is used only for unpublished synthetic modules in this local test.
It is not a user setup instruction. Third-party modules may be filled from the
existing Go download cache or the normal public proxy before the offline step.

`.github/workflows/static-modules.yml` repeats native packaging and consumer
tests on Linux amd64/arm64 and macOS amd64/arm64, then assembles all outputs.
It retains CI artifacts only; it does not deploy or upload to R2. A green local
macOS test does not establish Linux, musl, or older-macOS compatibility. The
existing manual-archive CI retains broader engine/Arrow tests.

## Module origin (not deployed)

`deploy/module-origin/worker.mjs` is a public, read-only Worker over an R2
binding named `MODULES`. Configure `MODULE_PREFIX` to the real prefix. Its
hostname must match the public request hostname. Use an authorized custom
domain with HTTPS; do not require login, signed URLs, tokens or browser challenges.
The bucket can remain private: only this module namespace is exposed by the
Worker. `wrangler.example.jsonc` is deliberately not a deployable production
configuration and does not enable a workers.dev endpoint.

The entry discovery page returns:

```html
<meta name="go-import" content="go.example.com/chdb mod https://go.example.com">
```

The Go module protocol reads versioned objects such as:

```
GET /go.example.com/chdb/static/@v/v0.261101.1.mod
GET /go.example.com/chdb/static/@v/v0.261101.1.info
GET /go.example.com/chdb/static/@v/v0.261101.1.zip
```

The corresponding R2 keys start with `proxy/`. ZIP bodies are streamed, not
buffered in Worker memory. HEAD, ETag, immutable cache headers, namespace checks
and storage-error responses are covered by `node --test deploy/module-origin/worker.test.mjs`.
`@v/list` is synthesized from completed immutable `.info` keys, with pagination.
`@latest` may return 404; Go can choose the latest version from the version list.
No mutable `list`/`@latest` index needs to be overwritten at publication.

## Public-release gates

The release-level [license review](static-license-review.md) records the actual
v26.11.1 payload evidence, focused LGPL source/relink obligations, and a pragmatic
comparison with DuckDB and ClickHouse. Review it before treating existing core
release assets as ready for independent Go-module redistribution. The embedded
source-wide license registry is not a list of all libraries actually linked.

Before any remote write, confirm the exact authorized domain, bucket, account,
root bindings release and publication inventory with the user. Review bundled
third-party license/notice obligations from the upstream build: copying the
root Apache license is not a claim that every bundled dependency notice has
already been audited. Resolve notices before the first public publication.

```sh
go run ./scripts/staticbundle plan -bundle /tmp/chdb-static-complete
```

This validates hashes and prints an inventory; it is not an upload. To prepare
a destination-specific plan, also supply `-endpoint` (the account's HTTPS R2 S3
endpoint) and `-bucket`. The resulting `approval_id` binds the exact receipt,
endpoint and bucket. Review that exact action with the user and obtain fresh
confirmation before executing `publish` with the same arguments and
`-confirm <approval_id>`. Do not treat an old plan ID as standing permission.

`publish` requires AWS CLI v2 with credentials already configured outside the
checkout. It uses `s3api put-object --if-none-match '*'` for every immutable key,
disables SDK automatic retries, never writes mutable indexes, and stops on the
first failed or uncertain request. It publishes all native ZIP/mod/info objects
before exposing the static entry, with each module's info last. A preflight HEAD
alone would not provide overwrite protection. The CLI has only been tested
with an injected upload runner here, not a live R2 account. On partial/uncertain
uploads, inspect the origin; never overwrite a published version or blindly
replay the entire upload. Use a new packaging version when recovery requires
different bytes.

After separately approved origin deployment and publication, validate all four
platforms with default `GOPROXY` and `GOSUMDB`, a cold cache, real `go get`, and
an offline rebuild. Validate the lowest supported Go/system runtime versions
and full four-platform vendoring. No public-release claim is warranted before
those checks.
