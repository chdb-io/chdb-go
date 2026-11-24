# Durable objects

`chdb/durable` puts a chDB database's authoritative state in object storage you
own — S3, R2, MinIO, or a directory — as a full checkpoint plus write-ahead-log
segments, with one compare-and-set `head.json` that holds the manifest and the
single-writer lease. Local MergeTree stays the hot working copy; the object is a
folder of open-format files you can move between clouds, and read from the
Python and Node bindings, which implement the same protocol.

```go
import "github.com/chdb-io/chdb-go/v2/chdb/durable"

ns, err := durable.NewNamespace("s3://my-bucket/durable?region=eu-west-1",
        durable.NamespaceOptions{Owner: "worker-1"})

obj, existed, err := ns.Open(ctx, "tenant-123", durable.OpenOptions{Database: "mem"})
defer obj.Close(ctx)

if !existed {
        _, err = obj.Execute(ctx, "CREATE TABLE events (id UInt64) ENGINE = MergeTree ORDER BY id")
}
ticket, err := obj.Execute(ctx, "INSERT INTO events VALUES (1)")
err = obj.FlushThrough(ctx, ticket)   // now it survives losing this machine
rows, err := obj.Query(ctx, "SELECT count() FROM events", "JSONEachRow")
_, err = obj.Checkpoint(ctx)          // fold base + WAL into a fresh base
```

`Execute` means the statement ran locally and joined the WAL buffer — not that
it left the machine. Durability is `Flush`, or `FlushThrough` for one
statement's watermark. Because recovery re-executes logged SQL, log literals
rather than `now()`, `rand()` or `generateUUIDv4()`; a checkpoint is the place
for anything non-deterministic, since it snapshots actual state.

Whether a statement may run is decided by ClickHouse's parser, not by this
package: every `Query` and `Execute` is analysed first — how many statements,
what class, does every write land in the object's own database, does the text
embed a credential — and the answer is the gate. `BACKUP` and `RESTORE` are
never assembled as text either. That needs **chdb-core v26.7.2-rc.2 or later**,
where those three entry points were added; an older engine is refused at open
rather than working partially. The published [engine
modules](engine.md#engine-modules) carry v26.7.3, so a build using one needs nothing
installed.

Two constraints are worth knowing before you design around it:

- chdb-core binds one data path per process, so **one process holds one open
  durable object at a time**. Fan-out across objects is sequential or spread
  across worker processes.
- The lease is *coordination*, not security. Access control is entirely your
  object store's IAM: anyone who can write the prefix can read, modify or take
  the lock. Give each tenant credentials scoped to its own prefix.

Errors carry a frozen category, so a caller can branch on what happened:

```go
if errors.Is(err, durable.ErrLeaseHeld) { /* another writer is live */ }
switch durable.CategoryOf(err) {
case durable.CategoryCommitAmbiguous: // may or may not have committed
case durable.CategoryCorrupt:         // a referenced object is missing or damaged
}
```

The S3 backend uses only the standard library — `net/http` and a SigV4 signer —
so importing `chdb-go` adds no cloud SDK to your `go.mod`. It covers the two
operations the protocol needs and resolves credentials from explicit options,
the standard environment variables, or `~/.aws/credentials`; for SSO or
instance-role credentials, pass `durable.S3Options.Credentials` yourself or
supply your own `durable.Backend`.

The protocol is specified in `CHDB_DURABLE_V1_CONTRACT.md` in the
[chdb](https://github.com/chdb-io/chdb) repository, which is the source of truth
for all bindings.
