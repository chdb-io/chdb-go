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

	"github.com/chdb-io/chdb-go/chdb"
)

func main() {
	// Stateless Query (ephemeral)
	result, err := chdb.Query("SELECT version()", "CSV")
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println(result)

	tmp_path := filepath.Join(os.TempDir(), "chdb_test")
	defer os.RemoveAll(tmp_path)
	// Stateful Query (persistent)
	session, _ := chdb.NewSession(tmp_path)
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

        _ "github.com/chdb-io/chdb-go/chdb/driver"
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

### Golang API docs

- See [lowApi.md](lowApi.md) for the low level APIs.
- See [chdb.md](chdb.md) for high level APIs.
