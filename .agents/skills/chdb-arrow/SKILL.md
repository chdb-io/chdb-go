---
name: chdb-arrow
description: Choose chdb-go Arrow module APIs versus a standard Arrow database driver for Go RecordBatch or reader input, typed results, or bulk insert. Not for ordinary database/sql row queries.
---

# Go Arrow path selection

Arrow Database Connectivity (ADBC) is a separate standard database-driver interface. A `RecordBatch` holds columnar values; a `RecordReader` supplies batches as needed. The optional module below keeps application Arrow objects available for SQL without first writing a table.

- Import the optional `github.com/chdb-io/chdb-go/v2/arrow` module for in-process SQL over an Arrow Go `RecordBatch`: `QueryBatch(sess, batch, func(src string) string { return "SELECT ... FROM " + src }, "TSV")`. For a one-shot `array.RecordReader`, `QueryFromReaderContext` returns formatted bytes; `QueryFromReaderBatchesContext` consumes typed output batches. For manual scope use `RegisterBatch` / `RegisterReaderContext` and check `Source.Close()`; do not reuse a consumed source.
- Typed results: `QueryTable(sess, sql)` plus `table.Release()`, or `QueryReader(ctx, sess, sql)` with `Next`, `RecordBatch`, `Err`, and `Close`. Bulk insert into an existing table: `InsertBatch` or `InsertReaderContext` (name-mapped columns). `database/sql.Rows` is not typed Arrow; `QueryFromReaderBatchesSQLContext` keeps an Arrow scan on a borrowed `*sql.Conn`.
- If a Go application already uses and has verified an ADBC driver manager with chDB, use ADBC for standard Arrow results or bulk ingest. General `BindStream` parameter binding does not expose an arbitrary reader as `FROM` input; choose this module for that case. This optional module needs Go 1.23+, cgo and compatible `libchdb` via `CHDB_LIB_PATH`; cancellation is cooperative at batch boundaries. Start with `arrow/README.md` for this binding's documentation map and examples.
