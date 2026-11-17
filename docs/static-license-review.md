# Static distribution license review for chDB v26.11.1

Review date: 2026-11-18. This review concerns republishing the existing four
chdb-core static release assets as Go modules. It does not change licenses,
authorize publication, or certify every source/header file as a legal SBOM.

## Conclusion

Keep the existing v26.11.1 engine and split-module design. The review found no
reason that the use of Go modules, Blob hosting, or the presence of LGPL code
by itself requires abandoning the design or rebuilding a permissive-only engine.
The practical work is to preserve applicable notices and provide the source and
rebuilding materials required for the actual LGPL components. These materials
are not yet included in the current payload ZIPs, so this is a conditional
engineering assessment, not a statement that the staged ZIPs are release-ready.

The focused LGPL list is MariaDB Connector/C on all four platforms and GNU SASL
on Linux. Both already exist in the pinned core build. They are not dependencies
introduced by the Go binding or by splitting the archive. Do not turn unrelated
Rust/MPL entries or XZ command-line licenses into additional release vetoes.

## Actual artifact scope

The engine source commit is `bc9c42e6edf4f394884a6a0458aa49abff85f1b6`.
All four downloaded tarball SHA-256 values match `static-release.json`.
The existing macOS arm64 Go consumer also reports that core commit through
`system.build_options`. Foreign-platform archives were inspected, not executed.

| Platform | Archive object members | Relevant defined-symbol evidence |
| --- | ---: | --- |
| macOS arm64 | 8747 | `mysql_init.chdb`, `mariadb_connection.chdb`, `lzma_code.chdb` |
| macOS amd64 | 8759 | Same MariaDB and liblzma anchors |
| Linux amd64 | 1 prelinked ELF object | `mysql_init`, `gsasl_init`, `krb5_init_context`, `u_getVersion_78`, `lzma_code` |
| Linux arm64 | 1 prelinked ELF object | Same Linux-specific anchors |

The two macOS archives retain 146 target/member-name groups each. All members
are assigned to 61 review families, including chDB/ClickHouse itself; these are
not 61 independently versioned dependencies. Linux prelinking has erased the
original member boundaries. Its critical libraries are verified by defined
symbols and the pinned build configuration, rather than fabricated per-library
object counts. Header-only and nested vendored code also requires carrying the
upstream component notices; object-name matching is not a complete source SBOM.

Detailed evidence:

- [Machine-readable inventory](compliance/static-v26.11.1-inventory.json):
  asset digests, per-platform archive groups, symbol checks, pinned submodule
  commits, selected license-file hashes, interpretation limits and exclusions.
- [Dependency and obligation list](compliance/static-v26.11.1-obligations.tsv):
  all 61 review families, macOS object counts and five Linux-specific entries.
  Linux entries include platform code/data as well as libraries; do not add the
  two counts and call the result a complete dependency count.

## Obligations by actual component

| Component or family | Actual inclusion | Applicable treatment |
| --- | --- | --- |
| chDB and ClickHouse code | All platforms | Apache-2.0 text, applicable upstream notices/attribution, and a clear description of repackaging changes. |
| MariaDB Connector/C | All platforms, confirmed by members and symbols | Preserve the fork's `COPYING.LIB` and file-level notices. Its copy is LGPL 2.1; some file headers allow Library GPL 2 or later. Supply corresponding library source, exact fork commit and build/patch steps. Address downstream static relinking as described below. |
| GNU SASL | Both Linux archives, not found in macOS artifacts | LGPL-2.1-or-later; corresponding library source and source/build/relink treatment. Retain notices for its included gnulib code. |
| LLVM libc++, libc++abi and libunwind | Runtime implementation symbols in all four archives | Apache-2.0 WITH LLVM-exception and retained legacy/component notices. Include the relevant runtime license files, not just a generic project label. |
| Arrow/Parquet, Avro, OpenSSL, AWS SDK, MongoDB drivers, H3, Thrift and other Apache components | Named macOS archive families; shared pinned build for Linux | Preserve applicable Apache texts and existing NOTICE/component material. This does not itself require publishing the application's source. |
| MIT/BSD/ISC/Zlib families | See TSV: Azure, c-ares, protobuf, libpqxx, RE2, codecs, hashes and others | Carry required copyright, license and disclaimer text in accompanying distribution materials. Preserve nested component notices where present. |
| Poco and Boost | Named archive groups | BSL-1.0, with its binary exception; preserve embedded PCRE BSD and Expat MIT notices separately. Original pdjson upstream uses Unlicense; retain its provenance when collecting Poco's bundled notices. |
| RocksDB, librdkafka and libarchive | Named archive groups | Collect their actual composite license files. RocksDB's LevelDB BSD notice is not the whole project license; librdkafka's main code is BSD-2-Clause, and libarchive has file-level BSD/MIT/Apache material. Do not flatten them to one heuristic label. |
| liblzma | Defined symbols in every archive | The pinned XZ `COPYING` explicitly licenses liblzma under **0BSD**. The XZ tools/scripts' GPL/LGPL do not apply merely because liblzma is linked. |
| ICU and Kerberos | Defined symbols on both Linux platforms | ICU Unicode License V3 and component/data notices; Kerberos's full NOTICE and component terms. ICU is not simply public domain despite the registry label. |
| Linux compatibility code | Enabled by pinned Linux build | Retain in-tree musl/Arm/Sun and other permissive notices. The compatibility layer is not itself a bundled copy of GNU glibc. |
| ULID and public-domain components | Named archive groups | Preserve the relevant dedication/provenance. The pinned `ulid-c/UNLICENSE` is absent from the license registry because its collector does not recognize that filename. |
| System libraries | macOS consumer loads system frameworks/libSystem/libiconv; Linux runtime audit remains part of CI | Dynamic system dependencies are separate from code embedded in the payload. Do not add an entire OS source distribution to every Go module. |

`system.licenses` in the tested engine has 1613 records. It scans source-tree
dependencies and embeds their texts, including unused ones. ClickHouse's own
documentation explicitly warns that listed libraries may not have been compiled.
An embedded GPL/MPL license text is not evidence that its library code was linked.

The pinned build sets `ENABLE_RUST=0`; macOS also disables HDFS/GSASL/Kerberos/ICU.
No `ssh_new` implementation was found in any archive and libssh is not enabled
by this build. No lemmagen archive family/lemmatizer anchor was identified.
Do not promote those source-list entries to confirmed payload dependencies.

## Our distribution versus downstream application distribution

For our Go payloads, preserve the upstream licenses and make the corresponding
LGPL library source available, including fork modifications and the scripts and
instructions needed to rebuild the included version. A mutable link to an
upstream default branch is not a reliable source pin. For network distribution,
the LGPL 2.1 section 4 equivalent-source-access route is a practical option;
stage the required source material with the release instead of assuming a
generic GitHub home-page link is enough. Source may be a separate downloadable
artifact; it need not be copied into every Go ZIP.

For downstream distributors of statically linked applications, LGPL 2.1 section
6 permits a work using the library to have its own terms, subject to conditions
including notices, permitted modification/debugging and an appropriate means
of relinking with a modified library. This is not an automatic requirement to
open all business source: application object/relink materials can be an option.
Our documentation must not advertise the SDK as imposing no downstream static
redistribution obligations.

The current Linux core archive is already one prelinked object and our packager
combines objects further on macOS. Therefore do not promise that a user can
replace an isolated MariaDB object inside an existing `.syso`. The supported
source/build path needs to rebuild the relevant core library, regenerate the
native modules, and relink. A future verified relink example is more useful than
an abstract demand to eliminate all LGPL code. Source availability and notices
remain necessary even if shared linking is offered as an alternative.

## DuckDB and ClickHouse comparison

| Project and checked sample | Observed practice | What it means for chDB |
| --- | --- | --- |
| DuckDB v2.0.0 official Linux static tarball | Contains static archives and headers; no standalone LICENSE/NOTICE member in the downloaded sample. Third-party licenses remain in its source tree. | This is an observation of one official artifact, not proof that omitted notices may be discarded. |
| DuckDB `go.duckdb.org/libduckdb/linux-amd64@v0.20000.0` | Contains MIT LICENSE, native archives, Go metadata and provenance. No separate third-party-notices file in this sample. Its generator explicitly copies the root LICENSE into each platform module. | Root LICENSE plus provenance is normal lightweight module practice. chDB additionally needs to handle the actual LGPL libraries it carries. |
| DuckDB bundled source licenses | E.g. Zstd has BSD terms; Mbed TLS offers Apache-2.0 OR GPL-2.0-or-later. | A GPL word in a file or a dual-license expression is not a mandatory GPL choice. Do not infer from DuckDB's MIT headline that every vendored file has no conditions. |
| ClickHouse fixed source baseline and package manifest | Explicitly integrates LGPL MariaDB and optional GNU SASL. `system.licenses` exposes embedded third-party texts; the official package recipe carries the project LICENSE and docs. | chDB's broad dependency mix is inherited, not a novel problem created by Go modules. Source and notice practices are useful precedents. |
| ClickHouse `clickhouse-common-static` | Installs `/usr/bin/clickhouse`; it is an application package, not an `.a` SDK for arbitrary downstream applications. | Its packaging does not answer every obligation of a proprietary downstream application statically linking our SDK. |

The downloaded DuckDB Go sample has module hash
`h1:7WPX45BIgaeoLmVuWGwzLfuIC78JEvdlZIPnHbyhh1c=`. Its provenance names
DuckDB source `d54cbbe3df17e8e5ce68312f483b4155836112aa` and the official static
tarball SHA-256 `112d62c980f5447cd303a70b3dacbc09acd41ccb4ef6d154e8629f543a0f8568`.

These comparisons support a modest, concrete release checklist. They do not
establish a legal exemption based on another project's omissions, nor do they
justify treating LGPL as a prohibition on redistribution or commercial use.

## Minimum work before public payload publication

1. Put the project LICENSE, a readable third-party notice collection and pinned
   source/provenance information in each independently distributed payload
   module, or use an explicitly verified equivalent delivery arrangement.
   Reusing the full component notices across parts is acceptable and simpler
   than proving a precise legal split for each relocatable object.
2. Provide source access for the actual LGPL libraries, the exact fork versions
   and modifications, and the core/native-module rebuilding instructions. Keep
   this as separate source material if it avoids inflating each module.
3. Add concise downstream static-redistribution guidance and check the practical
   rebuild/relink path. Do not require all users to open their business code.
4. Check that packaging retains these materials and counts their bytes against
   Go module limits. The current packager's single LICENSE copy and 1 MiB size
   reservation are not yet a complete notice-delivery implementation.

No blanket requirement is added to remove every copyleft component, rebuild
chdb-core immediately, obtain an external legal opinion for every permissive
library, or create an exhaustive source-level SPDX SBOM before native testing.
A narrow unresolved license interpretation affecting actual distribution terms
should be escalated rather than silently waived. Uploads, CI dispatch and
publication still need their existing user approvals.

## Primary references

- [ClickHouse third-party documentation](https://clickhouse.com/docs/resources/develop-contribute/contribute/contrib)
- [ClickHouse package recipe](https://github.com/ClickHouse/ClickHouse/blob/master/packages/clickhouse-common-static.yaml)
- [chDB pinned static build](https://github.com/chdb-io/chdb-core/blob/v26.11.1/chdb/build/build_static_lib.sh)
- [Pinned MariaDB integration](https://github.com/chdb-io/chdb-core/blob/v26.11.1/contrib/mariadb-connector-c-cmake/CMakeLists.txt)
- [MariaDB LGPL text](https://github.com/ClickHouse/mariadb-connector-c/blob/b3c09ab333c2ef9277291e721871a2bafca6e8bf/COPYING.LIB), especially sections 4, 6 and 7
- [MariaDB source-file grant](https://github.com/ClickHouse/mariadb-connector-c/blob/b3c09ab333c2ef9277291e721871a2bafca6e8bf/libmariadb/mariadb_lib.c)
- [GNU SASL grant](https://github.com/ClickHouse/libgsasl/blob/2d16b4e0d9435bec4546875ef07d36383bb993a5/src/gsasl.h)
- [Pinned XZ component licensing](https://github.com/tukaani-project/xz/blob/d3e650e63c110e830fd5391e7f8b45df0b91d3da/COPYING)
- [Apache 2.0 redistribution terms](https://www.apache.org/licenses/LICENSE-2.0)
- [DuckDB FAQ](https://duckdb.org/faq)
- [DuckDB v2.0.0 artifact packaging](https://github.com/duckdb/duckdb/blob/v2.0.0/scripts/package_release_artifact.sh)
- [DuckDB Go native-module staging](https://github.com/duckdb/duckdb-go-bindings/blob/main/internal/cmd/brood/stage.go)
- [DuckDB Zstd BSD license](https://github.com/duckdb/duckdb/blob/v2.0.0/third_party/zstd/LICENSE)
- [DuckDB Mbed TLS license alternatives](https://github.com/duckdb/duckdb/blob/v2.0.0/third_party/mbedtls/LICENSE)
- [Pinned ULID dedication](https://github.com/ClickHouse/ulid-c/blob/c433b6783cf918b8f996dacd014cb2b68c7de419/UNLICENSE)
- [Original pdjson dedication](https://github.com/skeeto/pdjson/blob/master/UNLICENSE)
