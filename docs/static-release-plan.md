# Static Go module release plan

The selected domain is `go.chdb.io`, with module prefix `go.chdb.io/chdb`.
`static-publication.json` is the machine-readable version and namespace plan;
`static-release.json` pins the existing engine assets and header. Neither file
creates a tag, provisions a domain or publishes an artifact.

| Layer | Validation | Stable release |
| --- | --- | --- |
| chdb-core | v26.11.1 | v26.11.1, unchanged |
| Root bindings | v2.5.0-rc.2 | v2.5.0 |
| Arrow module tag | arrow/v0.1.0-rc.1 | arrow/v0.1.0 |
| Native engine module version | v0.261101.1, local CI namespace only | v0.261101.1 at the permanent namespace |

## Local and CI validation

RC snapshots use `static-ci.example/chdb`, not the production namespace. These
snapshots test the checkout's source under the planned RC versions; they are
not proof that those tags exist remotely. The four-platform workflow packages
the pinned native assets, runs external static/Arrow consumers, offline/vendor
builds and pointer/race checks, then assembles CI-only artifacts. No workflow
publishes engine modules, creates tags or deploys a site.

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

1. Push a reviewed source commit to a dedicated test branch and pass native CI.
   A Git push needs fresh confirmation and uploads temporary Actions artifacts.
2. Publish the root `v2.5.0-rc.2` tag, then the `arrow/v0.1.0-rc.1` tag. Arrow's
   checked-in dependency is prepared for root `v2.5.0-rc.2`; an external Arrow RC
   must resolve the published root without a workspace or `replace`.
3. Validate the final domain with a newly generated tiny probe. The previous
   `*.vercel.app` report is not acceptance evidence for `go.chdb.io`. Domain
   attachment/DNS, Blob upload and discovery deployment are separate writes.
4. Prepare the license/notice and source/relink delivery material described in
   [static distribution materials](static-distribution-materials.md). Re-run
   size and native tests when adding those bytes.
5. After RC acceptance, tag the tested root as `v2.5.0`. Update Arrow's root
   requirement to `v2.5.0`, refresh its real `go.sum` from the published root,
   validate it externally, then tag `arrow/v0.1.0`. Do not fabricate checksums
   before the root version exists.
6. Package/assemble using `-phase release -notices /absolute/prepared/materials`.
   The stable entry requires **v2.5.0**, not its RC. Validate this bundle locally
   before presenting an exact Blob upload inventory for confirmation.
7. Run real public default-proxy/checksum and offline acceptance on the stable
   bundle and supported runners before announcing it as available to users.

The stable static version is immutable. Do not first publish
`go.chdb.io/chdb/static@v0.261101.1` requiring `v2.5.0-rc.2`, then overwrite its
ZIP or go.mod to require `v2.5.0`. Validation and release have different module
paths, and only the final stable dependency graph is published at the permanent
path. Fixes after that publication need a new packaging revision.

Publishing a Git tag makes a Go version discoverable; creating a GitHub Release
is a different action. The existing `release.yml` responds to GitHub Release
creation and uploads CLI binaries, so do not invoke it implicitly for a module
tag. Every tag push, release creation, upload and deployment retains its own
confirmation checkpoint.

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
