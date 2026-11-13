# Arrow Go guide for chdb-go

Use the optional Arrow module when a Go program needs to query Arrow batches
without writing them to disk, insert batches into chDB, or receive SQL results
as typed Arrow data. Input and output use the Arrow C Data interface, not an
Arrow IPC file. No end-to-end zero-copy guarantee is made: input buffers are
copied into native-owned memory before the engine reads them.

The ordinary `github.com/chdb-io/chdb-go/v2` module remains Go 1.21 and
cgo-free. Only programs importing `github.com/chdb-io/chdb-go/v2/arrow` take
on the Arrow Go and cgo requirements. These are two independently versioned
Go modules in the same repository; a version of the main module is not an
Arrow module version.

## Install and run

You need:

- Go 1.23 or later, `CGO_ENABLED=1`, and a working C compiler (for example,
  Xcode Command Line Tools on macOS or a C toolchain on Linux).
- Linux or macOS, with a released amd64 or arm64 engine. The module's CI runs
  on Linux amd64 and macOS arm64; the other two engine assets exist but are
  not part of that CI matrix. This module is not built for Windows or with
  `CGO_ENABLED=0`.
- A `libchdb.so` from chdb-core **v26.11.0-rc.3 or newer** that exports the
  Arrow C ABI. The Go compiler does not need chdb-core headers or source;
  `libchdb` is loaded at runtime.

In your Go application, install the separately released module:

```sh
go get github.com/chdb-io/chdb-go/v2/arrow@latest
CGO_ENABLED=1 go run .
```

The `@latest` command requires a published Arrow-module tag (of the form
`arrow/vX.Y.Z`); the main module's `v2.4.0` tag alone does not publish it.
The Arrow module currently requires the main module at `v2.4.0` or later.

Download the dynamic library for your platform from the
[chdb-core releases](https://github.com/chdb-io/chdb-core/releases/tag/v26.11.0-rc.3).
The release assets are named `linux-x86_64-libchdb.tar.gz`,
`linux-aarch64-libchdb.tar.gz`, `macos-x86_64-libchdb.tar.gz`, and
`macos-arm64-libchdb.tar.gz`. For example, from a directory where you want
to keep the library:

```sh
asset=macos-arm64-libchdb.tar.gz  # replace with your platform's asset above
base=https://github.com/chdb-io/chdb-core/releases/download/v26.11.0-rc.3
curl -fL -o "$asset" "$base/$asset"
curl -fL -o SHA256SUMS "$base/SHA256SUMS"
if command -v sha256sum >/dev/null; then
  grep "  $asset$" SHA256SUMS | sha256sum -c -
else
  grep "  $asset$" SHA256SUMS | shasum -a 256 -c -
fi
tar -xzf "$asset"
export CHDB_LIB_PATH="$PWD/libchdb.so"
```

Stop if the checksum does not match. `CHDB_LIB_PATH` must be an **absolute path
to the file**, not its containing directory. Run your Go program with that
environment variable set.

When working from a chdb-go source checkout, `./update_libchdb.sh` instead
downloads its pinned engine into that directory. An installed engine is not
upgraded by `go get`. The separately versioned `lib/embedded` module currently
carries an older engine: a program that imports it needs `CHDB_LIB_PATH` set
to a compatible library to override the embedded copy. See the main
[engine loading guide](../README.md#where-the-engine-is-loaded-from).

From this repository's `arrow` directory, run the
[complete quickstart](examples/quickstart/main.go) with:

```sh
CGO_ENABLED=1 go run ./examples/quickstart
```

It checks the engine, queries an in-memory Arrow batch, inserts two batches
into a temporary table, materializes a typed result, and reads several output
batches. The library path must be set first.

Choose the API by what your program already has:

| You have or need | Call | Cleanup |
| --- | --- | --- |
| One input batch and formatted SQL bytes | `QueryBatch` | Release your batch |
| A multi-batch input reader and formatted SQL bytes | `QueryFromReaderContext` | Release your reader |
| Incremental Arrow input and incremental typed output | `QueryFromReaderBatchesContext` | Release your reader; retain/release any output batches you keep |
| An existing table to fill | `InsertBatch` or `InsertReaderContext` | Release your input |
| A complete typed SQL result | `QueryTable` | Release the returned table |
| Typed results one batch at a time | `QueryReader` | Close the returned reader |
| A source you manage across one synchronous query | `RegisterReaderContext` | Close the returned source; check the error |

## Query an Arrow batch in memory

Build an `arrow.RecordBatch` with Arrow Go's array builders, then pass it to
`QueryBatch`. The SQL builder receives a temporary `arrowstream('...')`
expression. It must return one query that reads that expression at most once:

```go
schema := arrow.NewSchema([]arrow.Field{
    {Name: "id", Type: arrow.PrimitiveTypes.Int64},
}, nil)
builder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
defer builder.Release()
builder.Field(0).(*array.Int64Builder).AppendValues([]int64{7, 5}, nil)
batch := builder.NewRecordBatch()
defer batch.Release()

out, err := chdbarrow.QueryBatch(sess, batch, func(source string) string {
    return "SELECT sum(id) FROM " + source
}, "TSV")
// out is []byte containing "12\n"; handle err before using it.
```

The imports and session setup are in the [runnable example](examples/quickstart/main.go).
The caller retains ownership of `batch`. `QueryBatch` closes its temporary
source before returning; no file or persistent staging table is created.
An empty `format` uses `TSV`.

For multiple batches, create an Arrow Go `array.RecordReader` and call
`QueryFromReader` or `QueryFromReaderContext`. The module requests and copies
each batch only as the engine scans; it does not first collect all batches in
a table. Keep the reader valid and do not call `Next` on it concurrently with
the query. The module retains/releases its own reference; the caller releases
the reader's reference. These convenience functions close the temporary
source and return any close error along with a query or producer error.

For an incremental typed pipeline, call `QueryFromReaderBatchesContext`.
It scans one input reader and calls your consumer once per output batch:

```go
err := chdbarrow.QueryFromReaderBatchesContext(ctx, sess, input,
    func(source string) string { return "SELECT id FROM " + source },
    func(batch arrow.RecordBatch) error {
        // Process batch before returning; Retain and later Release to keep it.
        return process(batch)
    })
// Handle err and release your input reader.
```

This is a synchronous call, not a lazy result reader: the source stays
registered until the output stream is exhausted or the consumer returns an
error, and is unregistered before the function returns. The SQL must scan it
at most once. The consumer must not close the source or session inside its
callback. Cancellation is cooperative between batches; it cannot interrupt
an already-running native query, fetch, input `Next`, or consumer call. The
consumer owns any concurrency or backpressure policy beyond this synchronous
handoff. `Source.QueryBatchesContext` offers the same scan for a manually
registered source; call `Source.Close` afterward and check its error. This
interface does not promise an end-to-end memory bound inside the native SQL
engine, whose query plan may need to buffer data (for example, a global sort).

For ClickHouse `{name:Type}` placeholders, use `Parameter{Name: "name",
Value: "..."}` with `QueryReaderParams` or
`QueryFromReaderBatchesParamsContext`. The native engine parses each value as
the type declared in SQL; values are not inserted into the SQL string.
Parameters apply to one query only, and require an engine with the streaming
Arrow parameter ABI (`GetCapabilities(sess).ParamBinding`). Positional `?`
arguments in the existing `database/sql` driver are a separate interpolation
path, not this native binding path.

For applications already using `database/sql`, obtain a `*sql.Conn` with
`db.Conn(ctx)` and call `QueryFromReaderBatchesSQLContext` with the same
reader, query builder, parameters, and consumer. It runs the entire scan
inside that connection's `Raw` scope, releasing the Arrow source and result
before returning the connection to the pool. Import `chdb/driver` to register
the SQL driver. Do not retain the underlying session or run the callback
asynchronously. This interface is intentionally synchronous; a lazy Arrow
reader cannot safely outlive `sql.Conn.Raw`.

`Source.QueryContext`, `Source.QueryTableContext`,
`Source.QueryTableWithOptionsContext`, `QueryTableContext`, and
`QueryTableWithOptionsContext` check their context before and after the native
call. A deadline reached while libchdb is executing is reported only after
that call returns; these functions do **not** promise immediate interruption.
`QueryReader` also checks cancellation after its initial fetch and each later
fetch, releasing a result that arrived after cancellation. Never use a Go
goroutine that returns on timeout while leaving a native query running on a
session you then reuse or close: its resources still belong to that call.

If you need to manage that lifetime yourself, `RegisterBatch` or
`RegisterReaderContext` returns a `Source`. Use `source.Query` for formatted
bytes or `source.QueryTable` for typed output, then **call `source.Close()` and
check its error** before closing the session. The source is one-shot. Its
expression is process-visible inside chDB, but safe use is limited to the
single tracked query on the registering session. Do not cache it, use it from
another session, scan it twice, or start an asynchronous query with it.
The SQL builder may close the source or session; if either closes before the
builder returns, no native query starts and the call returns `ErrSourceClosed`.

## Insert batches into an existing table

`InsertBatch(sess, "events", batch)` inserts one batch;
`InsertReaderContext(ctx, sess, "events", reader)` consumes a multi-batch
reader. Create the destination table first. Columns are matched by name, not
by their positions in that table. Table and column names are limited to ASCII
letters, digits and underscores, starting with a letter or underscore; the
destination may also be `database.table`. SQL fragments are rejected as
identifiers. The caller retains ownership of each input batch or reader.

The temporary Arrow source is released before either insert function returns.
Cancellation can stop requesting further input batches, but an INSERT already
running in the engine is **not** promised to be interrupted or rolled back.
Use your normal transaction/consistency policy for partial failures.
The [quickstart](examples/quickstart/main.go) includes a two-batch insert and
checks the stored count and sum.

## Read typed SQL results

`QueryTable(sess, sql)` returns an `arrow.Table` containing the complete
result. Always call `table.Release()` when finished. The native query handle
and stream are released before `QueryTable` returns, including on errors.
For a large result without a temporary Arrow input source, use
`QueryReader(ctx, sess, sql)` to fetch batches incrementally instead:

```go
reader, err := chdbarrow.QueryReader(ctx, sess,
    "SELECT number FROM numbers(1000) SETTINGS max_block_size=100")
if err != nil { return err }
defer reader.Close()

fmt.Println(reader.Schema()) // available even if the result has zero rows
for reader.Next() {
    batch := reader.RecordBatch()
    fmt.Println(batch.NumRows())
}
return reader.Err()
```

This snippet assumes `ctx` and `sess` already exist and sits inside a
function returning `error`. `RecordBatch()` is borrowed until the next
`Next()` or `Close()`: call `batch.Retain()` and later `batch.Release()` if
you need it longer. Call `reader.Close()` if you stop early; it is safe to
call more than once. At EOF, the native handle is closed, and the reader's
schema and error remain available. Both functions return errors instead of
silently falling back to Arrow IPC when the engine lacks the required ABI.

`QueryTableWithOptions`, `QueryReaderWithOptions`, and
`Source.QueryTableWithOptions` accept `OutputOptions`. The default maps
ClickHouse `String` to Arrow UTF-8, materializes `LowCardinality` as its base
type, and errors on unsupported output types. Opt in to
`StringAsBinary`, `LowCardinalityAsDictionary`, or `UnsupportedAsBinary` only
when that representation suits your application. For example:

```go
table, err := chdbarrow.QueryTableWithOptions(sess, sql,
    chdbarrow.OutputOptions{StringAsBinary: true})
// Handle err, then Release the table when done.
```

## Supported input and lifetime rules

Input accepts a nonempty schema with nullable or non-nullable `Bool`, signed
and unsigned 8/16/32/64-bit integers, `Float32`, `Float64`, `Date32`,
`Timestamp`, `Decimal128`, `String` (UTF-8), and `Binary` fields. Other Arrow
input types, such as nested arrays, are rejected rather than silently
converted. Output types depend on the selected engine and the SQL expression;
use the type mapping options above for the documented fallback choices.

The safe ownership order is: finish the source query or reader loop; check
query/reader errors; explicitly close a `Source` or `ResultReader`; release
Arrow tables and any retained batches; then close the `chdb.Session`. A
session's `Close` also closes its live Arrow resources and waits for in-flight
native calls before retiring its connection, but explicit `Source.Close` is
needed to observe an unregister error. Do not share a single session's native
connection for concurrent Arrow queries.

Input contexts are checked before requesting each new batch; a user-provided
reader blocked inside `Next` must arrange its own cancellation. For output,
`QueryReader` fetches its first batch while being created, and its context is
checked at later `Next` boundaries. Closing a reader waits for any current
native fetch to finish before cancelling and destroying its handle; it is not
a guarantee that another goroutine can interrupt an already-blocked fetch.

If `GetCapabilities(sess)` reports an incompatible version or missing `Input`,
`TableOutput`, or `ReaderOutput`, select a compatible `libchdb` and check
`CHDB_LIB_PATH` (the path to a **file**, not a directory). The capability flags
report exported functions; `ContractCompatible` also checks the known minimum
engine version. They do not certify an arbitrary custom build's memory
ownership behavior.
