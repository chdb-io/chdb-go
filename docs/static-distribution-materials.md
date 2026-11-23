# Static distribution materials

The [v26.11.1 review](static-license-review.md) establishes a practical scope:
preserve applicable third-party texts and source/rebuild access for the actual
LGPL libraries, without treating every source-tree registry entry as linked
code. These instructions do not require removing LGPL libraries or publishing
all downstream business source.

## Files carried by every module

Prepare a directory outside the checkout containing:

| File | Required content |
| --- | --- |
| THIRD_PARTY_NOTICES.txt | Applicable copyright, license/disclaimer and upstream NOTICE texts, including component notices and the actual LGPL license grants. Do not supply only SPDX names or a list of links. |
| SOURCE_PROVENANCE.json | `engine`, exact `core_commit`, pinned component forks/commits, source delivery locations and relevant modifications/build inputs. |
| STATIC_RELINKING.md | Source availability instructions, the supported rebuilding route and a concise distinction between SDK distribution and downstream binary distribution. |

`-notices /absolute/prepared/materials` embeds the three files in every native
part and in the static entry module, alongside the project's LICENSE. A shared
union across platforms is acceptable; it avoids trying to split legal
attributions at arbitrary relocatable-object boundaries. It is not a declaration
that every component mentioned in a notice is compiled on every platform.

The receipt pins all notice sizes and SHA-256 values. Assembly rejects different
notice sets, and reading a bundle verifies the bytes inside every ZIP. Notice
bytes are included in the module size budget rather than a fixed 1 MiB estimate.
The three files have a combined 16 MiB safety limit.

The command checks delivery and integrity, not the legal sufficiency of text or
whether a linked source URL has actually been published. Public engine upload
plans reject bundles without these materials. Legacy/development bundles remain
readable and usable for local validation without them.

## Source provenance

For v26.11.2-rc.1 the three files have been prepared from the core commit
`59a7f688d1fc8d895115e104f09a2c4071490d6a` and its pinned submodules; their
file records are in [static-v26.11.2-rc.1-inventory.json](compliance/static-v26.11.2-rc.1-inventory.json).
Every pinned submodule commit was confirmed reachable at its `.gitmodules` URL.
Source is delivered as those exact commits at their public repository URLs,
including for the LGPL components (MariaDB Connector/C and GNU SASL); no
separate source archive is published. The modified-library rebuild below has
been tested end to end on all four platforms.

The v26.11.1 identity was:

```json
{
  "engine": "v26.11.1",
  "core_commit": "bc9c42e6edf4f394884a6a0458aa49abff85f1b6"
}
```

That minimal example is **not a finished distribution manifest**. Complete it
with the prepared source artifacts and their hashes, fork locations, patches,
build scripts and the reviewed dependency inventory. In particular preserve:

- MariaDB Connector/C commit `b3c09ab333c2ef9277291e721871a2bafca6e8bf`
  on all platforms, with its COPYING.LIB and file-level grants.
- GNU SASL commit `2d16b4e0d9435bec4546875ef07d36383bb993a5` on Linux,
  with its LGPL and included gnulib notices.
- The core static build, symbol isolation/prelink and module packaging scripts
  used for the shipped bytes. A default-branch link alone does not pin these.

Required source may be distributed separately from Go ZIPs. Prepare equivalent
source access with the release instead of assuming that pointing at a project
home page satisfies every binary-distribution obligation. Record actual source
locations after they exist; do not invent an already-published source URL.

## Rebuilding and local replacement

For a source consumer, the intended route is to check out the exact core and
submodule sources, modify the relevant library, rebuild core, regenerate the
native modules and rebuild the application. Linux's archive is already
prelinked; replacing an arbitrary MariaDB object inside a finished `.syso` is
not the supported interface.

The packager authenticates the original upstream archive. A modified build must
use an explicitly separate local release manifest with its own archive hash
and packaging identity; do not disable the checksum check or claim that the
modified bytes are the original authenticated release. For a local replacement,
generate matching entry and payload modules, extract them into local module
directories and add application-level `replace` directives for all affected
module paths before resolving/building. Keep that work isolated from the public
Go cache and do not upload modified bytes under an existing public version.

For v26.11.2-rc.1 this was tested on all four platforms: MariaDB Connector/C
was changed, the core rebuilt at the RC commit as the release workflows build
it, the archive packaged under a separate local manifest (packaging identity
99) and replaced into an application using the published root `v2.5.0-rc.2`.
Each application ran queries and reported the changed MariaDB message. On
macOS, build the tarball with `COPYFILE_DISABLE=1 tar --no-xattrs`; the
packager rejects `._*` members.
It also does not promise that a binary-only recipient can rebuild an application
without suitable application source or object/relink materials. Downstream
binary distributors must choose and document their applicable LGPL compliance
route. Opening all business source is not the only possible route.

## Remaining release material work

The upstream-text collection, separate corresponding-source delivery and
end-to-end replacement example still need to be prepared. These instructions
and the audit inventory are not substitutes for those deliverables. Keep
validation moving under the separate CI namespace, and complete these items
before the first permanent-namespace engine publication.
