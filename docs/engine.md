# The chDB engine

How chdb-go finds `libchdb`, and the engine modules that carry it inside a build.

## Where the engine is loaded from

`chdb-go` opens `libchdb` at runtime. It is looked up in this order:

1. `CHDB_LIB_PATH`, if set — the library file, not a directory. Setting it
   disables the rest of the search, so a copy that does not load is an error
   rather than a reason to use a different one. It outranks a carried engine,
   which is how a build that has one can still be pointed at another.
2. An engine compiled into the binary, if the build imports one of the
   [engine modules](#engine-modules). Nothing below is tried.
3. The directory holding the running executable.
4. `PATH`.
5. `/usr/local/lib`, `/opt/homebrew/lib` and `/usr/lib`.
6. The directories in `LD_LIBRARY_PATH`, `DYLD_LIBRARY_PATH` and
   `DYLD_FALLBACK_LIBRARY_PATH`.
7. The dynamic loader, by name — this is what reaches an `ldconfig`-registered
   install, `/usr/lib/<triple>` on a multiarch distribution, or a `RUNPATH`.

When none of them yields a usable library, the error lists every location that
was tried and why each one failed.

`chdbpurego.LoadedLibraryPath()` returns the absolute path of the library the
process is actually using, which is worth logging at startup if you ship
`libchdb` yourself.

The static engine (`go.chdb.io/chdb/static`, built with `-tags chdb_static`) is
linked into the executable and does not use this search. Setting
`CHDB_LIB_PATH` with it is an error.

## Engine modules

`lib/<platform>` are four Go modules, one per platform, each carrying a compressed
`libchdb`. Bringing one into the build makes the engine part of it: on first run
the engine is extracted to a cache directory named after its digest, and reused
after that.

One blank import does it, with no platform in the path and no engine version in
your `go.mod`:

```go
import _ "github.com/chdb-io/chdb-go/lib/embedded"
```

`lib/embedded` is a dispatch module: its `go.mod` names the four platform modules
and which version of each, and the build takes the one that matches. On a platform
none of them covers it registers nothing and the build still succeeds, leaving the
engine to be looked up on the machine as usual.

Import a platform module directly instead when you want to choose the engine
version yourself:

```go
import (
    chdbpurego "github.com/chdb-io/chdb-go/v2/chdb-purego"
    engine "github.com/chdb-io/chdb-go/lib/linux-amd64"
)

func init() {
    chdbpurego.RegisterEmbeddedEngine(chdbpurego.EmbeddedEngine{
        Version:  engine.Version,
        FileName: engine.FileName,
        Digest:   engine.Digest,
        Size:     engine.Size,
        Open:     engine.Open,
    })
}
```

`CHDB_CACHE_DIR` chooses where it is extracted, defaulting to the user cache
directory and then the temporary directory. It must be a directory no other user
can write to or substitute, or extraction refuses it.

### Reading the version

The middle field is the chdb-core release, six digits, two per field. The last is
which packaging of that same engine this is, counting from 1.

```
v0.260700.1
   ▲▲▲▲▲▲ ▲
   26 07 00 — chdb-core v26.7.0, on the ClickHouse 26.7 line
            1 — first packaging of it
```

| module version | chdb-core release | ClickHouse line |
| --- | --- | --- |
| `v0.260700.1` | `v26.7.0` | 26.7 |
| `v0.260700.2` | `v26.7.0`, repackaged | 26.7 |
| `v0.260702.0-rc.1.1` | `v26.7.2-rc.1` | 26.7 |

The first two fields of a chdb-core release are the ClickHouse line it carries; its
third is chdb-core's own counter, so `v26.7.0` is some ClickHouse 26.7 and not a
ClickHouse 26.7.0. `chdbpurego.EmbeddedEngineVersion()` returns the chdb-core
release at runtime, and `SELECT version()` returns the exact ClickHouse build.

Candidates sort below every release of the same engine, so
`go get lib/<platform>@latest` never picks one.

Maintainers: see [publishing engine modules](engine-module-publishing.md).
