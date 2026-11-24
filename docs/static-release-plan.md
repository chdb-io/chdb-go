# Static Go module release plan

The selected domain is `go.chdb.io`, with module prefix `go.chdb.io/chdb`.
`static-publication.json` is the machine-readable version and namespace plan;
`static-release.json` pins the existing engine assets and header. Neither file
creates a tag, provisions a domain or publishes an artifact.

There are three phases (`-phase`):

| Layer | `validation` (CI only) | `public-rc` | `release` (stable) |
| --- | --- | --- | --- |
| Module prefix | `static-ci.example/chdb` | `go.chdb.io/chdb` | `go.chdb.io/chdb` |
| chdb-core | v26.11.2-rc.1 | v26.11.2-rc.1 | next stable chdb-core, re-pinned and re-validated |
| Root bindings | v2.5.0-rc.2 (checkout snapshot) | v2.5.0-rc.2 (published tag) | v2.5.0 |
| Arrow module tag | v2/arrow/v0.1.0-rc.1 (checkout snapshot) | v2/arrow/v0.1.0-rc.1 (published tag) | v2/arrow/v0.1.0 |
| Static engine module version | v0.261102.0-rc.1.1 | v0.261102.0-rc.1.1 | derived from the stable engine |

`public-rc` uses the validation RC versions at the permanent namespace, so the
public RC depends on exactly what CI validated. It accepts only a prerelease
static version, so it never takes a stable identity; `release` accepts only a
stable engine. Both public phases need `-notices`, and every Blob or publish
command must name one of them: the default `validation` phase never uploads.

## Local and CI validation

RC snapshots use `static-ci.example/chdb`, not the production namespace. These
snapshots test the checkout's source under the planned RC versions; they are
not proof that those tags exist remotely. The workflow runs all four targets
by default and must assemble a complete bundle; a manual `platform_set=macos`
run is for Mac-only debugging. It packages the pinned native assets, runs external static/Arrow consumers,
offline/vendor builds and pointer/race checks, then assembles CI-only artifacts.
No workflow
publishes engine modules, creates tags or deploys a site.

A macOS-only aggregate uses `-allow-partial`, retains `complete=false` and cannot
be published as a complete engine bundle. A green macOS run is not evidence of
Linux readiness. Failed macOS consumers retain native backtraces/build metadata
and the generated native modules for diagnosis; no core dumps are uploaded.

Before Darwin prelinking, the packager gives anonymous section-leading data
local boundary symbols. This prevents `ld -r` from attaching a following
object's constants to a preceding weak symbol, which could replace the data
or lose its SIMD alignment during the final link. Both Mac runners execute a
small Intel Mach-O regression that checks the linked constant bytes and
alignment without running a translated executable.

The source-only Arrow workflows use
`.github/scripts/prepare-arrow-workspace.sh` with a temporary `GOWORK` file.
It replaces Arrow's exact required root version with the checkout, so these
tests do not require an unpublished RC tag. This replacement never enters a
source `go.mod`, a packaged module, or the external-consumer verification.

```sh
go run ./scripts/staticbundle release-plan \
  -publication-plan static-publication.json -phase validation

go run ./scripts/staticbundle package \
  -publication-plan static-publication.json -phase validation \
  -archive /absolute/path/macos-arm64-libchdb-static.tar.gz \
  -out /absolute/temporary/parent/native

go run ./scripts/staticbundle assemble \
  -publication-plan static-publication.json -phase validation \
  -input /absolute/temporary/parent/native -allow-partial \
  -out /absolute/temporary/parent/assembled

go run ./scripts/staticbundle verify \
  -publication-plan static-publication.json -phase validation \
  -bundle /absolute/temporary/parent/assembled \
  -out /absolute/temporary/parent/consumer-check
```

Use each platform's native runner and authenticated release archive. Four
platforms are required to assemble a complete bundle; partial builds are only
for development. Outputs must not exist and must be outside the checkout.

## RC publication and stable promotion

1. Push a reviewed source commit to a dedicated test branch and pass the
   four-platform native CI; it is required before a complete release.
   A Git push needs fresh confirmation and uploads temporary Actions artifacts.
2. After the static adapter is on main, publish the root `v2.5.0-rc.2` tag from
   it, then the `v2/arrow/v0.1.0-rc.1` tag. (`v2.5.0-rc.1` already exists and
   predates the static adapter, so it cannot serve.) Arrow's checked-in
   dependency is prepared for root `v2.5.0-rc.2`; an external Arrow RC must
   resolve the published root without a workspace or `replace`.
3. Validate the final domain with a newly generated tiny probe. The previous
   `*.vercel.app` report is not acceptance evidence for `go.chdb.io`. Domain
   attachment/DNS, Blob upload and discovery deployment are separate writes.
4. Prepare the license/notice and source/relink delivery material described in
   [static distribution materials](static-distribution-materials.md). Re-run
   size and native tests when adding those bytes.
5. Package/assemble the public RC with `-phase public-rc -notices
   /absolute/prepared/materials`. Its static entry requires the published
   `v2.5.0-rc.2`. Upload and deploy it only after exact, separately confirmed
   inventories, then run the public cold-cache, default proxy/checksum and
   offline acceptance on all four targets.
6. After RC acceptance and a stable chdb-core release, re-pin
   `static-release.json` and pass the four-platform CI again, then tag the
   tested root as `v2.5.0`. Update Arrow's root
   requirement to `v2.5.0`, refresh its real `go.sum` from the published root,
   validate it externally, then tag `v2/arrow/v0.1.0`. Do not fabricate checksums
   before the root version exists.
7. Package/assemble using `-phase release -notices /absolute/prepared/materials`.
   The stable entry requires **v2.5.0**, not its RC. Validate this bundle locally
   before presenting an exact Blob upload inventory for confirmation.
8. Run real public default-proxy/checksum and offline acceptance on the stable
   bundle and supported runners before announcing it as available to users.

The stable static version is immutable. Do not first publish
a static version requiring `v2.5.0-rc.2`, then overwrite its ZIP or go.mod to
require `v2.5.0`. The public RC (`v0.261102.0-rc.1.1`, requiring the root RC)
and the stable release (a stable engine version, requiring `v2.5.0`) are
different versions at the permanent path; neither replaces the other. Fixes after that publication need a new packaging revision.

Publishing a Git tag makes a Go version discoverable; creating a GitHub Release
is a different action. The existing `release.yml` responds to GitHub Release
creation and uploads CLI binaries, so do not invoke it implicitly for a module
tag. Every tag push, release creation, upload and deployment retains its own
confirmation checkpoint.

## New engine baseline checklist

Every upload and deployment is a separate, confirmed action; nothing below
publishes automatically. A new chdb-core baseline (for example v26.12.x) goes
through these steps:

1. **Core release.** chdb-core publishes the four static assets for the tag
   (Linux: `*-libchdb-static-split.tar.gz`; macOS: `*-libchdb-static.tar.gz`).
   Check the release's files and `SHA256SUMS`, not only its CI.
2. **Pins (one chdb-go PR).**
   - `static-release.json`: engine, assets, SHA-256s, header SHA-256, source commit.
   - `chdb-purego/static_chdb.h`: the release's `chdb.h`.
   - `static-publication.json`: engine and the derived static version; the root
     and Arrow RC/stable versions.
   - `release-notices/<engine>/` and `docs/compliance/static-<engine>-*`:
     regenerate with [the notice tools](../release-notices/tools/README.md) and
     review what changed in the payload.
3. **CI.** All four platforms of `static-modules.yml` pass, including the
   `quickstart` job (`go get .../static@latest`, tagged build, run, offline
   rebuild) against the assembled bundle.
4. **Tags.** Tag the root bindings together with the static release: the static
   adapter in the root checks that the linked engine has its header's version.
   Then tag `v2/arrow/vX.Y.Z` if Arrow changed. Each tag push is confirmed.
5. **Package.** Dispatch `static-modules.yml` with `platform_set=all` and
   `phase=public-rc` (or `release` for a stable engine). Keep the complete
   bundle and `blob-plan.json` artifacts.
6. **Upload and deploy (confirmed separately).** Recompute the plan locally and
   check its `approval_id`, then `blob-publish`. Generate the site with
   `blob-site -previous-catalog` (the deployed `https://go.chdb.io/catalog.json`)
   so earlier versions stay listed, review its inventory, then deploy.
7. **Public acceptance.** Dispatch `static-public-acceptance.yml`: the same
   quickstart against `go.chdb.io` with default `GOPROXY`/`GOSUMDB` on all four
   platforms.

## Final domain target

`deploy/vercel-modules/go.chdb.io.target.json` is a **local candidate target**.
It reuses the existing dedicated Go test project/store and selects the new
origin. It does not assert that Vercel has attached the hostname or that DNS is
configured. The project/store remain unchanged until an exact remote action is
approved; do not modify the Maven project or its storage.

Generate the final-domain probe and site locally:

```sh
go run ./scripts/staticbundle probe \
  -publication-plan static-publication.json \
  -out /absolute/temporary/parent/probe

go run ./scripts/staticbundle blob-plan \
  -publication-plan static-publication.json \
  -bundle /absolute/temporary/parent/probe \
  -blob-target deploy/vercel-modules/go.chdb.io.target.json

go run ./scripts/staticbundle blob-site \
  -publication-plan static-publication.json \
  -bundle /absolute/temporary/parent/probe \
  -blob-target deploy/vercel-modules/go.chdb.io.target.json \
  -out /absolute/temporary/parent/site
```

The existing test site uses a different catalog namespace. Keep its previous
deployment available for recovery; do not merge a cross-namespace catalog or
delete earlier public probe objects as rollback.
