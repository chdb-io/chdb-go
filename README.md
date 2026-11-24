<a href="https://chdb.io" target="_blank">
  <img src="https://avatars.githubusercontent.com/u/132536224" width=130 />
</a>

[![chDB-go](https://github.com/chdb-io/chdb-go/actions/workflows/chdb.yml/badge.svg)](https://github.com/chdb-io/chdb-go/actions/workflows/chdb.yml)

# chdb-go

Go bindings and a CLI for [chDB](https://github.com/chdb-io/chdb), the in-process
SQL OLAP engine powered by ClickHouse.

```go
result, err := chdb.Query("SELECT version()", "CSV")
```

## Install

chdb-go needs the chDB engine (`libchdb`). Pick one way to get it:

| Way | How | Good for |
| --- | --- | --- |
| Installed on the machine | `curl -sL https://lib.chdb.io \| bash` | Development machines; no cgo |
| Embedded in the build | `import _ "github.com/chdb-io/chdb-go/lib/embedded"` | Shipping one binary; the engine is extracted to a cache on first run (Linux needs glibc) |
| Static (experimental) | `go get go.chdb.io/chdb/static@latest` | One executable with no engine file at runtime; needs cgo |

Then add the bindings:

```sh
go get github.com/chdb-io/chdb-go/v2
```

**Installed on the machine.** Upgrading chdb-go does not upgrade the engine;
re-run the installer to move both together.

**Embedded.** One blank import and nothing needs installing. See
[engine modules](docs/engine.md#engine-modules) to pin a specific engine.

**Static.** Import `_ "go.chdb.io/chdb/static"` and build with
`CGO_ENABLED=1 go build -tags chdb_static`. The current release is
`v0.261102.0-rc.1.1` (chdb-core v26.11.2-rc.1, a release candidate) for Linux
and macOS, amd64 and arm64. See the [static engine guide](docs/static-linking.md).

If chdb-go cannot find the engine, the error lists every location it tried; see
[where the engine is loaded from](docs/engine.md#where-the-engine-is-loaded-from).

## Usage

### Queries and sessions

```go
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/chdb-io/chdb-go/v2/chdb"
)

func main() {
	// Stateless query
	result, err := chdb.Query("SELECT version()", "CSV")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result)

	// Stateful session with data on disk; Cleanup also deletes the folder
	session, err := chdb.NewSession(filepath.Join(os.TempDir(), "chdb_test"))
	if err != nil {
		log.Fatal(err)
	}
	defer session.Cleanup()

	if _, err := session.Query("CREATE DATABASE IF NOT EXISTS testdb; " +
		"CREATE TABLE IF NOT EXISTS testdb.testtable (id UInt32) ENGINE = MergeTree() ORDER BY id;"); err != nil {
		log.Fatal(err)
	}
	if _, err := session.Query("USE testdb; INSERT INTO testtable VALUES (1), (2), (3);"); err != nil {
		log.Fatal(err)
	}
	rows, err := session.Query("SELECT * FROM testtable;")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(rows)
}
```

### database/sql driver

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
	defer db.Close()

	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM url('https://datasets.clickhouse.com/hits_compatible/athena_partitioned/hits_0.parquet')`).Scan(&count)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("count: %d", count)
}
```

### Query parameters

Use ClickHouse `{name:Type}` placeholders with `sql.Named`. The engine parses
each value as the declared type; it is not interpolated into SQL:

```go
var total int64
err := db.QueryRowContext(ctx,
	"SELECT count() FROM events WHERE user_id = {id:Int64}",
	sql.Named("id", int64(42))).Scan(&total)
```

This needs an engine that supports parameter binding. Positional
`?` arguments are interpolated into the SQL text, so prefer named placeholders
for untrusted input. Named and positional arguments cannot be mixed.

### Concurrency

A process runs one engine bound to one data path, and that engine runs queries
from several connections in parallel. Each pooled `database/sql` connection is
its own engine connection:

```go
db, err := sql.Open("chdb", "session=/path/to/data")
if err != nil {
	log.Fatal(err)
}
defer db.Close()
db.SetMaxOpenConns(8)
```

All connections in a process must use the same data path.

### Durable objects

`chdb/durable` keeps a database's state in object storage you own (S3, R2,
MinIO or a directory) as a checkpoint plus a write-ahead log, with a
single-writer lease:

```go
ns, err := durable.NewNamespace("s3://my-bucket/durable?region=eu-west-1",
	durable.NamespaceOptions{Owner: "worker-1"})
obj, existed, err := ns.Open(ctx, "tenant-123", durable.OpenOptions{Database: "mem"})
defer obj.Close(ctx)

if !existed {
	_, err = obj.Execute(ctx, "CREATE TABLE events (id UInt64) ENGINE = MergeTree ORDER BY id")
}
ticket, err := obj.Execute(ctx, "INSERT INTO events VALUES (1)")
err = obj.FlushThrough(ctx, ticket) // survives losing this machine
```

One process holds one open durable object at a time, and the lease is
coordination, not security: access control is your object store's IAM. See the
[durable objects guide](docs/durable.md).

### Arrow

Arrow Go input and typed output live in a separate module,
`github.com/chdb-io/chdb-go/v2/arrow` (Go 1.23+, cgo). Programs that do not
import it stay cgo-free. See the [Arrow guide](v2/arrow/README.md).

### Stopping the engine

Closing sessions does not stop the engine's threads. Exiting the process is
fine. To stop them earlier, or before exiting a binary built with `-race`,
close every session and then call:

```go
if err := chdb.Shutdown(); err != nil {
	log.Println("chdb:", err)
}
```

After `Shutdown` the process cannot open another session. See
[stopping the engine](docs/shutdown.md).

## CLI

```sh
go install github.com/chdb-io/chdb-go/v2@latest
chdb-go "SELECT 123"                # one query
chdb-go "SELECT 123" JSON           # with an output format
chdb-go                             # interactive; data is lost on exit
chdb-go --path /tmp/chdb            # interactive with persistent data
```

`go install` puts `chdb-go` in `$(go env GOPATH)/bin`. The CLI uses an engine
installed on the machine. To build it from source, run `make build`.

## Reference

- [Where the engine is loaded from, and engine modules](docs/engine.md)
- [Static engine](docs/static-linking.md)
- [Durable objects](docs/durable.md) and [stopping the engine](docs/shutdown.md)
- [High-level API](chdb.md) and [low-level API](lowApi.md)
