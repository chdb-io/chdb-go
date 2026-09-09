// Package durable implements chDB Durable V1: an addressable, single-writer
// chDB engine whose authoritative state lives in object storage you own.
//
// A durable object is one chDB database whose committed state is a full
// checkpoint plus a chain of write-ahead-log segments, published as immutable
// objects under a prefix, with one compare-and-set `head.json` naming the
// current manifest and holding the writer lease. Local MergeTree is the hot
// working copy; the object itself is a folder of open-format files you can
// move between clouds.
//
//	ns, err := durable.NewNamespace("s3://my-bucket/durable?region=eu-west-1",
//		durable.NamespaceOptions{Owner: "worker-1"})
//
//	obj, existed, err := ns.Open(ctx, "tenant-123", durable.OpenOptions{Database: "mem"})
//	defer obj.Close(ctx)
//
//	if !existed {
//		_, err = obj.Execute(ctx, "CREATE TABLE events (id UInt64, at DateTime) ENGINE = MergeTree ORDER BY id")
//	}
//	ticket, err := obj.Execute(ctx, "INSERT INTO events VALUES (1, '2026-09-07 00:00:00')")
//	err = obj.FlushThrough(ctx, ticket)   // now it survives losing this machine
//	rows, err := obj.Query(ctx, "SELECT count() FROM events", "JSONEachRow")
//	_, err = obj.Checkpoint(ctx)          // fold base + WAL into a fresh base
//
// # What Execute guarantees, and what it does not
//
// Execute means the statement ran locally and joined the WAL buffer. It does
// not mean the write left the machine. Durability is Flush, or FlushThrough
// for one statement's watermark. A service that answers a client before
// flushing is choosing to lose that write if the process dies, and it should
// choose that knowingly rather than by accident.
//
// # What the engine decides, and what this package decides
//
// Whether a statement may run at all is not this package's judgement. Every
// Query and Execute is put to ClickHouse's own parser first — how many
// executable statements is this, what class are they, does every persistent
// write land in the database this object owns, does the text embed a
// credential — and the answer is the gate. There is no prefix list and no
// regular expression anywhere in this package, because neither can see through
// `INSERT ... FORMAT` inline data or resolve an unqualified table name.
// Likewise `BACKUP` and `RESTORE` are never assembled as text: core takes the
// database name and the path as arguments and does its own quoting.
//
// That needs chdb-core v26.7.2-rc.2 or later, which is where the three
// management entry points were added. An older engine is refused at open with
// an engine_incompatible error rather than working partially.
//
// # Determinism is the caller's job
//
// Recovery re-executes logged SQL, so a statement must produce the same result
// on replay as it did originally. Log literals: compute a timestamp or an id
// in the caller and log the value, not `now()`, `rand()`,
// `generateUUIDv4()`, or an `INSERT ... SELECT` from a volatile source. V1
// promises ordered replay of the original statement text and nothing more.
// Non-deterministic or bulk transformations belong in a Checkpoint, which
// snapshots actual state.
//
// # One object per process
//
// chdb-core binds one data path per process, so one process holds one open
// durable object at a time. Opening a second returns an error naming both
// paths. Fan-out across many objects is sequential, or spread across worker
// processes; this package does not pretend otherwise.
//
// # Version compatibility
//
// An object records the exact chdb_version() that wrote it, and that is not a
// gate. Compatibility is decided by two explicit fields — the archive-format
// generation and the minimum reader version — so an object written by
// v26.7.2-rc.2 opens on every later chdb-core release that can still restore
// its archive. See the comment on assertEngineCompatible in negotiate.go for
// why the two checks are separate and why neither is a version equality test.
//
// # Security scope
//
// This package provides single-writer *coordination* — the lease plus the
// compare-and-set fence — not security. Access control is entirely your object
// store's IAM: anyone who can write the object's prefix can read, modify or
// take its lock. There is no application-level auth, no client-side
// encryption, and no tamper protection beyond the length and SHA-256 checks
// that detect a damaged archive. For multi-tenant use, give each tenant
// credentials scoped to its own prefix.
//
// # Where the specification lives
//
// The protocol is specified in CHDB_DURABLE_V1_CONTRACT.md in the chdb
// repository, and that document — not this implementation — is the source of
// truth. Semantics change there first, and the same fixtures are read by the
// Python, Node and Go bindings.
package durable
