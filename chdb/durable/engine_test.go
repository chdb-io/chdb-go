package durable

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	chdbpurego "github.com/chdb-io/chdb-go/v2/chdb-purego"
)

// End-to-end against the real engine.
//
// Everything else in this package is tested with a stand-in engine, which is
// what makes the fault matrix reachable. These tests exist for the two things
// a stand-in cannot establish: that the gates are fed by ClickHouse's own
// parser rather than by string matching, and that a real chDB archive written
// by one scratch directory restores into another.
//
// chDB binds one data path per process, so every object here is opened and
// closed in turn. That is the contract's §3.6 constraint rather than a
// limitation of the tests, and chdb.Session refuses a second path with an
// error naming both — which is why nothing needs to guard against it here.

func requireEngine(t *testing.T) {
	t.Helper()
	available, err := chdbpurego.AdminABIAvailable()
	if err != nil {
		t.Skipf("libchdb did not load: %v", err)
	}
	if !available {
		t.Skip("libchdb predates the backup/restore/classify ABI (chdb-core v26.7.2-rc.2)")
	}
}

// realNamespace binds a namespace to a directory on disk with the real engine.
func realNamespace(t *testing.T, root string) *Namespace {
	t.Helper()
	ns, err := NewNamespace("file://"+root, NamespaceOptions{
		Owner:       "engine-test",
		ScratchRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return ns
}

// The whole lifecycle, with nothing faked: create, write, flush, checkpoint,
// close, and recover on a fresh scratch directory.
func TestEngineLifecycleRecoversThroughTheObject(t *testing.T) {
	requireEngine(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "store")

	obj, existed, err := realNamespace(t, root).Open(ctx, "orders", OpenOptions{Database: "mem"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if existed {
		t.Fatal("a cold object reported as existing")
	}

	mustExecute(t, obj, "CREATE TABLE events (id UInt64, note String) ENGINE = MergeTree ORDER BY id")
	mustExecute(t, obj, "INSERT INTO events VALUES (1, 'one')")
	if _, err := obj.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// A checkpoint is a real BACKUP DATABASE, which is the step a stand-in
	// engine cannot stand in for.
	if _, err := obj.Checkpoint(ctx); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	// Written after the checkpoint, so recovery has to restore the base *and*
	// replay a segment on top of it.
	mustExecute(t, obj, "INSERT INTO events VALUES (2, 'two')")
	if err := obj.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, existed, err := realNamespace(t, root).Open(ctx, "orders", OpenOptions{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close(ctx)
	if !existed {
		t.Fatal("the object exists")
	}
	if reopened.Database() != "mem" {
		t.Fatalf("the object's database is %q; the head is authoritative", reopened.Database())
	}

	rows, err := reopened.Query(ctx, "SELECT id, note FROM events ORDER BY id", "CSV")
	if err != nil {
		t.Fatalf("query the recovered object: %v", err)
	}
	want := "1,\"one\"\n2,\"two\"\n"
	if rows != want {
		t.Fatalf("the recovered object holds\n%q\nwant\n%q", rows, want)
	}
}

// A database name needing quotes is the case a binding that assembled the SQL
// itself would get wrong — in BACKUP, in RESTORE, and in the CREATE DATABASE
// and USE that bracket them.
func TestEngineHandlesAQuotedDatabaseName(t *testing.T) {
	requireEngine(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "store")

	obj, _, err := realNamespace(t, root).Open(ctx, "orders", OpenOptions{Database: "my-db"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustExecute(t, obj, "CREATE TABLE events (id UInt64) ENGINE = MergeTree ORDER BY id")
	mustExecute(t, obj, "INSERT INTO events VALUES (7)")
	if _, err := obj.Checkpoint(ctx); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := obj.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, _, err := realNamespace(t, root).Open(ctx, "orders", OpenOptions{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close(ctx)
	rows, err := reopened.Query(ctx, "SELECT id FROM events", "CSV")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if strings.TrimSpace(rows) != "7" {
		t.Fatalf("the recovered object holds %q, want 7", rows)
	}
}

// The gates as core decides them. A string-matching binding gets at least the
// inline-data and unqualified-name cases wrong, which is why neither is
// allowed to be a binding's judgement.
func TestEngineGatesComeFromTheParser(t *testing.T) {
	requireEngine(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "store")

	obj, _, err := realNamespace(t, root).Open(ctx, "orders", OpenOptions{Database: "mem"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer obj.Close(ctx)

	mustExecute(t, obj, "CREATE TABLE events (id UInt64) ENGINE = MergeTree ORDER BY id")

	// Inline data holding a semicolon is one statement. No amount of splitting
	// on ";" gets this right.
	if _, err := obj.Execute(ctx, "INSERT INTO events FORMAT CSV 1\n"); err != nil {
		t.Fatalf("a single INSERT ... FORMAT should be accepted: %v", err)
	}
	// An unqualified name resolves through the connection's current database,
	// which the object pinned to its own.
	if _, err := obj.Execute(ctx, "INSERT INTO events VALUES (2)"); err != nil {
		t.Fatalf("an unqualified write to the object's own database should be accepted: %v", err)
	}
	// And a qualified one to the same database is the same write.
	if _, err := obj.Execute(ctx, "INSERT INTO mem.events VALUES (3)"); err != nil {
		t.Fatalf("a qualified write to the object's own database should be accepted: %v", err)
	}

	refusals := []struct {
		name string
		sql  string
		want *Error
	}{
		{"two statements", "INSERT INTO events VALUES (4); INSERT INTO events VALUES (5)", ErrClassificationRefused},
		{"a read through Execute", "SELECT count() FROM events", ErrClassificationRefused},
		{"session control", "SET max_threads = 1", ErrClassificationRefused},
		{"a global function", "CREATE FUNCTION plus_one AS (x) -> x + 1", ErrClassificationRefused},
		{"another database", "INSERT INTO other.events VALUES (6)", ErrClassificationRefused},
		{"database lifecycle", "DROP DATABASE mem", ErrClassificationRefused},
		{"a table function", "INSERT INTO FUNCTION file('out.csv', 'CSV') SELECT * FROM events", ErrClassificationRefused},
		{"a file sink", "SELECT * FROM events INTO OUTFILE '/tmp/durable-test.csv'", ErrClassificationRefused},
		{"unparseable", "this is not sql", ErrClassificationRefused},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			_, err := obj.Execute(ctx, tc.sql)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Execute reported %q, want %q: %v", CategoryOf(err), tc.want.Category, err)
			}
		})
	}

	// A mutation embedding a credential cannot be logged, because the WAL
	// outlives the statement.
	_, err = obj.Execute(ctx,
		"INSERT INTO events SELECT 1 FROM s3('https://example.com/x.csv', 'AKIAEXAMPLE', 'wJalrXUtnFEMIsecret', 'CSV', 'id UInt64')")
	if !errors.Is(err, ErrSecretRefused) && !errors.Is(err, ErrClassificationRefused) {
		t.Fatalf("a secret-bearing mutation reported %q: %v", CategoryOf(err), err)
	}
	if err != nil && strings.Contains(err.Error(), "wJalrXUtnFEMIsecret") {
		t.Fatal("the refusal quoted the credential it exists to keep out")
	}

	// Query is gated from the other side.
	if _, err := obj.Query(ctx, "INSERT INTO events VALUES (9)", "CSV"); !errors.Is(err, ErrClassificationRefused) {
		t.Fatalf("Query accepted a write, or reported %q", CategoryOf(err))
	}

	rows, err := obj.Query(ctx, "SELECT count() FROM events", "CSV")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// Three accepted writes, and nothing a refusal let through.
	if strings.TrimSpace(rows) != "3" {
		t.Fatalf("the database holds %q rows, want 3", rows)
	}
}

// A read-only handle takes no lease and serves the manifest as it stood at
// open. Two live objects in one process would break the engine's one-path
// rule, so the writer is closed first.
func TestEngineReadOnlyOpenServesASnapshot(t *testing.T) {
	requireEngine(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "store")

	writer, _, err := realNamespace(t, root).Open(ctx, "orders", OpenOptions{Database: "mem"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustExecute(t, writer, "CREATE TABLE events (id UInt64) ENGINE = MergeTree ORDER BY id")
	mustExecute(t, writer, "INSERT INTO events VALUES (1)")
	if err := writer.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	reader, _, err := realNamespace(t, root).Open(ctx, "orders", OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("read-only open: %v", err)
	}
	defer reader.Close(ctx)

	rows, err := reader.Query(ctx, "SELECT count() FROM events", "CSV")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if strings.TrimSpace(rows) != "1" {
		t.Fatalf("the reader sees %q rows, want 1", rows)
	}
	if _, err := reader.Execute(ctx, "INSERT INTO events VALUES (2)"); err == nil {
		t.Fatal("a read-only handle accepted a write")
	}
}

// chDB binds one data path per process. The failure has to be legible rather
// than a crash somewhere deeper, which is why durable objects go through
// chdb.Session's registry instead of opening native connections directly.
func TestEngineRefusesASecondObjectInOneProcess(t *testing.T) {
	requireEngine(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "store")

	first, _, err := realNamespace(t, root).Open(ctx, "orders", OpenOptions{Database: "mem"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer first.Close(ctx)

	_, _, err = realNamespace(t, root).Open(ctx, "invoices", OpenOptions{Database: "mem"})
	if err == nil {
		t.Fatal("a second open on a different data path should be refused")
	}
	if !strings.Contains(err.Error(), "one data path per process") {
		t.Fatalf("the error does not explain the constraint: %v", err)
	}
	// And the refusal did not strand a lease on the object it could not open.
	backend, err := NewLocalBackend(filepath.Join(root, "invoices"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, found, err := readHead(ctx, backend)
	if err != nil {
		t.Fatal(err)
	}
	if found && snapshot.head.Lease.Owner != nil {
		t.Fatalf("the failed open left the lease held: %+v", snapshot.head.Lease)
	}
}

// The version an object records is the engine's own, and it is what the
// compatibility gate is written against. If chdb_version() ever stops being
// orderable, every later reader silently loses the ability to open the object.
func TestEngineVersionIsOrderable(t *testing.T) {
	requireEngine(t)
	version, err := chdbpurego.Version()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := parseVersion(version); !ok {
		t.Fatalf("chdb_version() reports %q, which this package cannot order by release "+
			"precedence — so it cannot decide compatibility", version)
	}
	// And an object written by it is readable by itself, which is the trivial
	// case the gate must not get wrong.
	head := coldHead("mem", version, BackupFormatBaseline)
	if err := assertEngineCompatible(head, runningEngine{version: version, backupFormat: BackupFormatBaseline}); err != nil {
		t.Fatalf("an engine cannot read its own object: %v", err)
	}
}
