<a href="https://chdb.io" target="_blank">
  <img src="https://avatars.githubusercontent.com/u/132536224" width=130 />
</a>

[![chDB-go](https://github.com/chdb-io/chdb-go/actions/workflows/chdb.yml/badge.svg)](https://github.com/chdb-io/chdb-go/actions/workflows/chdb.yml)

# chdb-go
[chDB](https://github.com/chdb-io/chdb) go bindings and chDB cli.

## Install

The engine can either come from the machine or from the build. Both are supported;
which one you want depends on whether you would rather install something or carry
it.

### From the machine

Install `libchdb` once, then use `chdb-go` against it:

```
curl -sL https://lib.chdb.io | bash
go get github.com/chdb-io/chdb-go/v2
```

The engine is looked up at runtime — see [where the engine is loaded
from](#where-the-engine-is-loaded-from). Upgrading `chdb-go` does not upgrade the
engine, so re-run the installer, or `update_libchdb.sh`, to move both together.

### From the build

Import an [engine module](#engine-modules) and nothing needs installing: the engine
travels inside the binary and is extracted to a cache directory on first run. This
is what you want for a self-contained binary, a `FROM scratch` image, or anywhere
you cannot ask for an install step.

### The CLI

```
go install github.com/chdb-io/chdb-go/v2@latest
$GOPATH/bin/chdb-go            # with or without a persistent --path
```

The CLI resolves the engine the same way, so it needs one installed on the machine.

### Where the engine is loaded from

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

### or Build from source
1. Build `chdb-go`
  - run `make build`
2. Run `chdb-go` with or without persistent `--path`
  - run `./chdb-go`

## Engine modules

`lib/<platform>` are four Go modules, one per platform, each carrying a compressed
`libchdb`. Importing one makes the engine part of the build: on first run it is
extracted to a cache directory named after its digest, and reused after that.

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

### Publishing

```
scripts/package-engine.sh v26.7.0                    # every platform
CHDB_PACKAGING=2 scripts/package-engine.sh v26.7.0   # repackage the same engine
```

The script prints the exact `git tag` commands. Use those — the module proxy
serves a tag's bytes permanently, so a wrong tag can only be superseded, never
fixed. `internal/enginetag` defines the rule, `go run ./scripts/enginetag -verify
lib/<platform>/<version>` checks a tag against the module it names, and CI runs
that check on every `lib/**` tag pushed.

Payloads are added with `git add -f` at publish time and are not on the default
branch, so a clone does not carry every engine version ever shipped.

## chdb-go CLI

1. Simple mode
```bash
./chdb-go "SELECT 123"
./chdb-go "SELECT 123" JSON
```
2. Interactive mode
```bash
./chdb-go # enter interactive mode, but data will be lost after exit
./chdb-go --path /tmp/chdb # interactive persistent mode
```

```bash
chdb-io/chdb-go [main] » ./chdb-go 
Enter your SQL commands; type 'exit' to quit.
 :) CREATE DATABASE IF NOT EXISTS testdb;


```

#### Go lib Example
```go
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chdb-io/chdb-go/v2/chdb"
)

func main() {
	// Stateless Query (ephemeral)
	result, err := chdb.Query("SELECT version()", "CSV")
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println(result)

	tmp_path := filepath.Join(os.TempDir(), "chdb_test")
	// Stateful Query (persistent)
	session, _ := chdb.NewSession(tmp_path)
	// session cleanup will also delete the folder
	defer session.Cleanup()

	_, err = session.Query("CREATE DATABASE IF NOT EXISTS testdb; " +
		"CREATE TABLE IF NOT EXISTS testdb.testtable (id UInt32) ENGINE = MergeTree() ORDER BY id;")
	if err != nil {
		fmt.Println(err)
		return
	}

	_, err = session.Query("USE testdb; INSERT INTO testtable VALUES (1), (2), (3);")
	if err != nil {
		fmt.Println(err)
		return
	}

	ret, err := session.Query("SELECT * FROM testtable;")
	if err != nil {
		fmt.Println(err)
	} else {
		fmt.Println(ret)
	}
}
```

#### Go SQL driver for chDB
```go
package main

import (
        "database/sql"
        "log"

        _ "github.com/chdb-io/chdb-go/v2/chdb/driver"
)

func main() {
        db, err := sql.Open("chdb", "")
        if err != nil {
                log.Fatal(err)
        }
        rows, err := db.Query(`select COUNT(*) from url('https://datasets.clickhouse.com/hits_compatible/athena_partitioned/hits_0.parquet')`)
        if err != nil {
                log.Fatalf("select fail, err: %s", err)
        }
        cols, err := rows.Columns()
        if err != nil {
                log.Fatalf("get result columns fail, err: %s", err)
        }
        log.Printf("result columns: %v", cols)
        defer rows.Close()
        var count int
        for rows.Next() {
                err := rows.Scan(&count)
                if err != nil {
                        log.Fatalf("scan fail, err: %s", err)
                }
                log.Printf("count: %d", count)
        }
}
```

#### Concurrency

chDB runs a single embedded engine per process bound to one data path, but that
engine accepts multiple connections that execute queries concurrently. The
`database/sql` driver opens an independent native chDB connection per pooled
connection, so you can scale read/write parallelism with `SetMaxOpenConns`:

```go
db, err := sql.Open("chdb", "session=/path/to/data")
if err != nil {
        log.Fatal(err)
}
defer db.Close()

// Each pooled connection is its own native chDB connection to the same data
// path, so queries run in parallel instead of serializing on one connection.
db.SetMaxOpenConns(8)
```

All connections in a process must share the same data path; opening a second,
different data path while connections are still open returns an error.

### Golang API docs

- See [lowApi.md](lowApi.md) for the low level APIs.
- See [chdb.md](chdb.md) for high level APIs.
