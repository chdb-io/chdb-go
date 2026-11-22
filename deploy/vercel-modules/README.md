# Vercel Blob Go module publication

The selected production domain and RC/stable versions are recorded in
[`static-publication.json`](../../static-publication.json). Follow the
[release plan](../../docs/static-release-plan.md) for the permanent
`go.chdb.io/chdb` namespace, final-domain probe, separate CI namespace and
stable-root-before-static-entry ordering. The configured production target is
local planning data, not evidence of completed DNS/domain setup.

This is the current publishing path. It keeps `staticbundle package/assemble`
and their size, checksum, object-preservation and platform checks. R2 is not
required. No command here changes the Maven project or its Blob store.

The small Vercel site serves `go-import` discovery, immutable-artifact rewrites,
`@v/list`, and `@latest`. Native ZIPs stay in Blob and never enter the site's
deployment or a serverless function. As in the Maven gateway, external rewrite
caching is disabled to avoid reusing one HTTP Range response for another range.
Blob continues to cache immutable objects. Discovery/list pages get short cache
lifetimes and are replaced only by a separately approved site deployment.

## 1. Prepare the tiny probe locally

Use an already authorized, stable production hostname, not a protected
single-deployment URL. `target.example.json` contains public identifiers only;
replace them with the selected team/project/store and stable origin. The Blob
base URL must match the selected public store. No tokens belong in this file.

```sh
go run ./scripts/staticbundle probe \
  -module-prefix YOUR-STABLE-HOST/chdb \
  -out /absolute/temporary/parent/probe

go run ./scripts/staticbundle blob-plan \
  -bundle /absolute/temporary/parent/probe \
  -blob-target /absolute/path/target.json

go run ./scripts/staticbundle blob-site \
  -bundle /absolute/temporary/parent/probe \
  -blob-target /absolute/path/target.json \
  -out /absolute/temporary/parent/site
```

Outputs must not already exist, and parents must exist. The probe publishes
`<prefix>/probe` at v0.0.1 and v0.0.2, each returning a test string. It contains
no engine or third-party dependency. The six versioned Blob files are under a
1 MiB safety cap; `list` is generated locally for the site instead of uploaded.
This supersedes the preliminary seven-file plan that included a Blob list key.

The site output uses Build Output API v3, includes its linked project identity
in `.vercel/project.json`, and writes a separate `site-plan.json` for review.
It is not deployed by `blob-site` or `blob-publish`.

## 2. Approve and upload only the probe artifacts

`blob-plan` validates every file, refuses mutable paths, and prints an
`approval_id` bound to the exact receipt, team, project, store and origin.
Describe that precise remote write to the user and obtain fresh confirmation
before running `blob-publish`. An approval in another chat or for a prior
upload/deployment does not authorize this action.

Use Vercel CLI **65.11.0**, the validated CLI version, with the selected project
already linked and its store connected with OIDC. Refresh `.env.local` in that
linked directory using the normal Maven-style credential flow:

```sh
vercel env pull .env.local --environment development --scope TEAM
```

This is normal authentication preparation, not a request to create a long-lived
read-write token. The uploader parses only `BLOB_STORE_ID` and
`VERCEL_OIDC_TOKEN`, never executes/sources the env file, checks store identity
and token expiry, and passes credentials via child-process environment rather
than command arguments or plans. It also checks the linked project and team IDs.

After fresh user confirmation:

```sh
go run ./scripts/staticbundle blob-publish \
  -bundle /absolute/temporary/parent/probe \
  -blob-target /absolute/path/target.json \
  -project-dir /absolute/path/linked-project \
  -vercel-cli /absolute/path/to/vercel-or-vc.js \
  -confirm APPROVAL_ID
```

The adapter preflights **all** target objects with anonymous HEAD before any
write. CLI `blob put` uses public access, deterministic paths, and the CLI's
default `allowOverwrite: false` / `addRandomSuffix: false`; neither `--force`
nor `--add-random-suffix` is passed. Backend collision protection also covers
stale CDN HEAD results. Small files use single-part upload; files over 64 MiB
use multipart. The pinned Vercel SDK can internally retry network/part requests;
the adapter does not retry an uncertain file or the batch, and never deletes
published blobs as rollback. Stop and inspect the store on uncertainty.

## 3. Separately approve deployment, then prove the public path

After Blob publication, review the generated site's exact inventory and obtain
**another** confirmation before `vercel deploy --prebuilt --prod --scope TEAM`
from the generated site directory. Do not change deployment protection, DNS,
Maven or the store's access policy automatically. The stable production alias
must serve anonymous requests without cookies or Vercel bypass headers.

Only after that deployment:

```sh
go run ./scripts/staticbundle verify-public \
  -bundle /absolute/temporary/parent/probe \
  -blob-target /absolute/path/target.json \
  -out /absolute/temporary/parent/public-check
```

The verifier checks discovery metadata, both Blob-direct and gateway bytes, and
the two-version list with no auth or redirect bypass. It then uses a fresh Go
module/build cache with `GOENV=off`, `GOWORK=off`, `GOTOOLCHAIN=local`, and
`GOAUTH=off`. Use a toolchain supporting `GOAUTH=off` (development used Go 1.26.5).
The probe module itself declares Go 1.21; this is not a lowest-toolchain claim.

Crucially, online `go get` does **not** override `GOPROXY` or `GOSUMDB`: the
verifier checks `https://proxy.golang.org,direct` and `sum.golang.org`. It tests
v0.0.1, then `@latest` resolving to v0.0.2, builds and executes both, and runs
`go mod verify`. The final forced rebuild uses `GOPROXY=off` with outgoing HTTP
proxies blocked, leaving checksum verification enabled. A success report is
written only after the offline executable also returns the expected result.

Unit tests mock public HTTP/Go commands; those tests are not a substitute for
this live verification. A public test page alone also does not establish that
the public Go proxy and checksum database can fetch a module.

## 4. Only then use real engine artifacts

Keep the existing package/assemble/native-consumer checks. Package for the
chosen stable namespace and a compatible published root bindings release;
do not reuse the synthetic local bindings version in a public release.

`blob-plan` and `blob-site` also accept a complete engine `bundle.json`.
`blob-publish` additionally requires `-probe-report` pointing to the passed
`public-verification.json` for the **same** site/store/namespace. Partial engine
bundles remain non-publishable. Four-platform tests, upstream notices, and the
compatible root release remain required gates, not consequences of a tiny
probe's success.

The `Static engine Go modules (no publication)` workflow runs on pull requests,
main, and dedicated `codex/static-modules-*` test branches. It runs all four
targets by default; a manual `platform_set=macos` run skips Linux for Mac-only
debugging. Each selected runner authenticates and
repackages its pinned archive from `static-release.json` (Linux uses
chdb-core's split archive), then tests an external static consumer,
vendor/offline builds, and the Arrow quickstart plus its integration tests under
`cgocheck2` and the race detector against the packaged `.syso` modules. Linux
also executes its packaged binary on Ubuntu 22.04 and 24.04 without libchdb.
The final job assembles the selected platforms and retains the bundle as a
temporary Actions artifact. The all-platform run must assemble a complete
bundle; a macOS-only bundle stays partial and non-publishable. It never uploads to Blob or deploys
the discovery site.

These checks intentionally use synthetic local bindings/Arrow versions and a
file proxy with checksum-database verification disabled. They establish native
packaging/link/runtime behavior, not public release availability. The existing
manual-archive `Optional static chDB engine` workflow is complementary evidence,
not a substitute for this split-module workflow. Triggering CI and uploading its
artifacts still requires approval for the Git push/remote operation.

Before a public engine release, select a permanent authorized domain and pin the
exact tested source commit. Root bindings and the nested Arrow module need
independently resolvable versions. For a user-facing release, publish a new root
tag containing this module-compatible adapter and an `arrow/vX.Y.Z` tag with the
root dependency updated to the compatible release. A fixed commit/pseudo-version
can be used for pre-release testing; `main` is not a reproducible version pin.
No root release tag is a substitute for the Arrow module's independent version.

If the permanent namespace differs from the test `*.vercel.app` origin, repeat
the tiny public probe on the final namespace. Its earlier success cannot unlock
engine publication for a different site/store/namespace. Then verify the real
engine downloads through default Go proxy/checksum settings, Arrow consumers,
offline/vendor builds and runtime dependencies on each supported platform.

When generating a subsequent site, pass `-previous-catalog` with the currently
deployed `catalog.json`. Generation merges old versions, rejects cross-store
catalogs, and uses semantic version order (including prereleases), so an index
update does not erase older versions or select rc.10 below rc.2 lexically.

Changing from a temporary `*.vercel.app` namespace to `go.chdb.io` changes Go
module identity. Regenerate/revalidate under the final prefix; do not just
rewrite an old ZIP's URL or overwrite an existing version.
