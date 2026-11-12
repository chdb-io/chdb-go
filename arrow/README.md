# Optional Arrow integration for chdb-go

This separate Go module exchanges Arrow Go batches with chDB through the Arrow C
Data interface. Importing the main `chdb-go/v2` module alone does not add an
Arrow dependency or require cgo. Programs that import this module need Go 1.23
or later, `CGO_ENABLED=1`, and a working C compiler on Linux or macOS. The
runtime engine must be chdb-core `v26.11.0-rc.3` or newer with the Arrow C ABI.

The repository's `update_libchdb.sh` installs the pinned engine; alternatively,
set `CHDB_LIB_PATH` to the released `libchdb.so` file. The separately versioned
`lib/embedded` engine module currently carries an older engine and does not
provide these Arrow functions. `GetCapabilities` reports the functions found
in the loaded engine; operations also reject incompatible engine versions.

```go
import (
    "github.com/chdb-io/chdb-go/v2/chdb"
    chdbarrow "github.com/chdb-io/chdb-go/v2/arrow"
)

sess, err := chdb.NewSession()
if err != nil { /* handle error */ }
defer sess.Close()

table, err := chdbarrow.QueryTable(sess, "SELECT number FROM numbers(10)")
if err != nil { /* handle error */ }
defer table.Release()
```

For incremental output, use `QueryReader(ctx, sess, sql)` and call `Close` on
the result reader, including when stopping before EOF. Each returned batch is
borrowed until the next `Next` or `Close`; retain it if it must live longer.
`QueryTableWithOptions` and `QueryReaderWithOptions` allow explicit output type
mapping.

For input, `RegisterReaderContext(ctx, sess, reader)` returns a `Source` for
one synchronous query. Build SQL inside `source.Query` or `source.QueryTable`
using its supplied `arrowstream(...)` expression, then call `source.Close` and
check its error. Do not save or use that expression outside the callback or
scan a source twice. `InsertReaderContext` inserts a multi-batch reader into
an existing table and closes its temporary source before returning;
`RegisterBatch`, `QueryBatch`, and `InsertBatch` cover single batches.

The input path copies batches into C-owned memory before passing them to the
engine; no end-to-end zero-copy guarantee is made. Cancellation is checked at
batch boundaries. It does not forcibly interrupt a blocked user reader or
promise to roll back an INSERT already running in the native engine. Close
readers and sources before closing the session; `Session.Close` also closes
any remaining resources it owns.
