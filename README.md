<a href="https://chdb.io" target="_blank">
  <img src="https://avatars.githubusercontent.com/u/132536224" width=130 />
</a>

[![chDB-go](https://github.com/chdb-io/chdb-go/actions/workflows/chdb.yml/badge.svg)](https://github.com/chdb-io/chdb-go/actions/workflows/chdb.yml)

# chdb-go
[chDB](https://github.com/chdb-io/chdb) go bindings and chDB cli.

## Install

1. Download and install [`libchdb`](https://github.com/chdb-io/chdb/releases)
  - run `make update_libchdb` to download and extract libchdb.so. or
  - run `make install` to install libchdb.so
2. Build `chdb-go`
  - run `make build`
3. Run `chdb-go` with or without persistent `--path`
  - run `./chdb-go`

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

#### Go lib Example
```go
package main

import (
    "fmt"
    "github.com/chdb-io/chdb-go/chdb"
)

func main() {
    // Stateless Query (ephemeral)
    result := chdb.Query("SELECT version()", "CSV")
    fmt.Println(result)

    // Stateful Query (persistent)
    session, _ := NewSession(path)
    defer session.Cleanup()

    session.Query("CREATE DATABASE IF NOT EXISTS testdb; " +
    "CREATE TABLE IF NOT EXISTS testdb.testtable (id UInt32) ENGINE = MergeTree() ORDER BY id;")

    session.Query("USE testdb; INSERT INTO testtable VALUES (1), (2), (3);")

    ret := session.Query("SELECT * FROM testtable;")
    fmt.Println(ret)
}
```

### Golang API docs

- See [lowApi.md](lowApi.md) for the low level APIs.
- See [chdb.md](chdb.md) for high level APIs.
