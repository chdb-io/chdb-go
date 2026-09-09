package durable

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixture holds one object store and the knobs to open writers against it.
type fixture struct {
	t       *testing.T
	root    string
	backend *faultBackend
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "store")
	local, err := NewLocalBackend(filepath.Join(root, "orders"))
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, root: root, backend: newFaultBackend(local)}
}

// open brings up an object against the shared store with a fresh engine, so a
// reopen recovers through the object rather than through leftover local state.
func (f *fixture) open(options OpenOptions) (*Object, *fakeEngine, bool, error) {
	f.t.Helper()
	engine := newFakeEngine()
	if options.Database == "" {
		options.Database = "mem"
	}
	options.ScratchRoot = f.t.TempDir()
	ns, err := NewNamespace("file:///unused", NamespaceOptions{
		EngineFactory: engine.factory(),
		BackendFactory: func(context.Context, string) (Backend, error) {
			return f.backend, nil
		},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	obj, existed, err := ns.Open(context.Background(), "orders", options)
	return obj, engine, existed, err
}

func (f *fixture) mustOpen(options OpenOptions) (*Object, *fakeEngine, bool) {
	f.t.Helper()
	obj, engine, existed, err := f.open(options)
	if err != nil {
		f.t.Fatalf("open: %v", err)
	}
	return obj, engine, existed
}

func (f *fixture) head() Head {
	f.t.Helper()
	snapshot, found, err := readHead(context.Background(), f.backend)
	if err != nil {
		f.t.Fatalf("read head: %v", err)
	}
	if !found {
		f.t.Fatal("no head was published")
	}
	return snapshot.head
}

func mustExecute(t *testing.T, obj *Object, sql string) WriteTicket {
	t.Helper()
	ticket, err := obj.Execute(context.Background(), sql)
	if err != nil {
		t.Fatalf("execute %q: %v", sql, err)
	}
	return ticket
}

// ------------------------------------------------------------- cold and warm

func TestOpenCreatesAColdObject(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	obj, _, existed := f.mustOpen(OpenOptions{Database: "mem", Owner: "worker-1"})
	if existed {
		t.Fatal("a cold open reported the object as existing")
	}
	defer obj.Close(ctx)

	head := f.head()
	if head.Protocol.Version != ProtocolVersion {
		t.Errorf("protocol version is %d", head.Protocol.Version)
	}
	if head.Engine.Name != EngineName || head.Engine.Version != "26.7.2-rc.2" {
		t.Errorf("engine is %+v", head.Engine)
	}
	if head.Engine.MinReader != "26.7.2-rc.2" || head.Engine.BackupFormat != BackupFormatBaseline {
		t.Errorf("a fresh object should demand exactly the engine that made it, got %+v", head.Engine)
	}
	if head.Manifest.DB != "mem" || head.Manifest.Base != nil || len(head.Manifest.WAL) != 0 {
		t.Errorf("a cold manifest is %+v", head.Manifest)
	}
	// Cold create and lease acquisition are one conditional write, so the
	// published head is already held.
	if head.Lease.Generation != 1 || head.Lease.Owner == nil || *head.Lease.Owner != "worker-1" {
		t.Errorf("a cold lease is %+v", head.Lease)
	}
}

func TestExecuteAndFlushPublishASegment(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	obj, _, _ := f.mustOpen(OpenOptions{})

	mustExecute(t, obj, "CREATE TABLE events (id UInt64) ENGINE = MergeTree ORDER BY id")
	mustExecute(t, obj, "INSERT INTO events VALUES (1)")

	// Execute means "ran locally and joined the buffer", nothing more.
	if obj.PendingStatements() != 2 {
		t.Fatalf("%d statements pending, want 2", obj.PendingStatements())
	}
	if len(f.head().Manifest.WAL) != 0 {
		t.Fatal("Execute must not publish anything by itself")
	}

	ref, err := obj.Flush(ctx)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if ref == nil {
		t.Fatal("flush published nothing")
	}
	head := f.head()
	if len(head.Manifest.WAL) != 1 || head.Manifest.WAL[0].Key != ref.Key {
		t.Fatalf("the manifest does not name the segment: %+v", head.Manifest)
	}
	if head.Manifest.Seq != 1 {
		t.Errorf("seq is %d, want 1", head.Manifest.Seq)
	}
	if obj.PendingStatements() != 0 {
		t.Errorf("%d statements still pending after a flush", obj.PendingStatements())
	}

	// The reference carries the length and digest a reader verifies against.
	data, found, err := f.backend.GetBytes(ctx, ref.Key)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if err := assertDigest(*ref, digestOf(data), "WAL segment"); err != nil {
		t.Fatalf("the published segment does not match its reference: %v", err)
	}

	// Flushing nothing is not an error and publishes nothing.
	again, err := obj.Flush(ctx)
	if err != nil {
		t.Fatalf("an empty flush should succeed: %v", err)
	}
	if again != nil {
		t.Fatalf("an empty flush published %s", again.Key)
	}
	if err := obj.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestReopenReplaysTheWAL(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	obj, _, _ := f.mustOpen(OpenOptions{})
	mustExecute(t, obj, "CREATE TABLE events (id UInt64) ENGINE = MergeTree ORDER BY id")
	mustExecute(t, obj, "INSERT INTO events VALUES (1)")
	if _, err := obj.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	mustExecute(t, obj, "INSERT INTO events VALUES (2)")
	if _, err := obj.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := obj.Close(ctx); err != nil {
		t.Fatal(err)
	}

	var phases []RestorePhase
	reopened, engine, existed := f.mustOpen(OpenOptions{
		OnRestoreProgress: func(p RestoreProgress) { phases = append(phases, p.Phase) },
	})
	defer reopened.Close(ctx)
	if !existed {
		t.Fatal("a reopen reported the object as cold")
	}

	want := []string{
		"CREATE TABLE events (id UInt64) ENGINE = MergeTree ORDER BY id",
		"INSERT INTO events VALUES (1)",
		"INSERT INTO events VALUES (2)",
	}
	got := engine.statements()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("replay produced\n%v\nwant\n%v", got, want)
	}
	// Replay goes straight to the engine: it must not re-enter the WAL.
	if reopened.PendingStatements() != 0 {
		t.Fatalf("replay buffered %d statements", reopened.PendingStatements())
	}
	if len(phases) < 3 || phases[0] != PhaseCreatingDatabase || phases[len(phases)-1] != PhaseReady {
		t.Fatalf("restore reported phases %v", phases)
	}
}

func TestCheckpointFoldsTheWALIntoANewBase(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	obj, _, _ := f.mustOpen(OpenOptions{})
	mustExecute(t, obj, "CREATE TABLE events (id UInt64) ENGINE = MergeTree ORDER BY id")
	mustExecute(t, obj, "INSERT INTO events VALUES (1)")
	if _, err := obj.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	// A statement executed but not flushed is still in the database the
	// checkpoint archives, so the checkpoint covers it.
	mustExecute(t, obj, "INSERT INTO events VALUES (2)")

	ref, err := obj.Checkpoint(ctx)
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	head := f.head()
	if head.Manifest.Base == nil || head.Manifest.Base.Key != ref.Key {
		t.Fatalf("the manifest does not name the new base: %+v", head.Manifest)
	}
	if len(head.Manifest.WAL) != 0 {
		t.Fatalf("the WAL list was not truncated: %+v", head.Manifest.WAL)
	}
	if head.Manifest.Seq != 2 {
		t.Errorf("seq is %d, want 2", head.Manifest.Seq)
	}
	if obj.PendingStatements() != 0 {
		t.Errorf("%d statements still pending; the checkpoint covered them", obj.PendingStatements())
	}
	if err := obj.Close(ctx); err != nil {
		t.Fatal(err)
	}

	reopened, engine, _ := f.mustOpen(OpenOptions{})
	defer reopened.Close(ctx)
	if len(engine.statements()) != 3 {
		t.Fatalf("restoring from the base produced %v", engine.statements())
	}
}

// ------------------------------------------------------------ read-only open

func TestReadOnlyOpenOfAMissingObjectIsNotFound(t *testing.T) {
	f := newFixture(t)
	_, _, _, err := f.open(OpenOptions{ReadOnly: true})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("category is %q, want not_found: %v", CategoryOf(err), err)
	}
	// And it must not have created anything on the way.
	if _, found, err := f.backend.GetBytes(context.Background(), HeadKey); err != nil || found {
		t.Fatalf("a read-only open created a head: found=%v err=%v", found, err)
	}
}

func TestExistingOnlyRefusesToCreate(t *testing.T) {
	f := newFixture(t)
	if _, _, _, err := f.open(OpenOptions{ExistingOnly: true}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("category is %q, want not_found", CategoryOf(err))
	}
}

func TestReadOnlyOpenTakesNoLeaseAndRefusesWrites(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	writer, _, _ := f.mustOpen(OpenOptions{})
	mustExecute(t, writer, "INSERT INTO events VALUES (1)")
	if _, err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(ctx); err != nil {
		t.Fatal(err)
	}

	generationBefore := f.head().Lease.Generation
	reader, engine, existed := f.mustOpen(OpenOptions{ReadOnly: true})
	defer reader.Close(ctx)
	if !existed {
		t.Fatal("the object exists")
	}
	if got := f.head().Lease.Generation; got != generationBefore {
		t.Fatalf("a read-only open moved the generation from %d to %d", generationBefore, got)
	}
	if len(engine.statements()) != 1 {
		t.Fatalf("the reader restored %v", engine.statements())
	}
	if _, err := reader.Execute(ctx, "INSERT INTO events VALUES (2)"); !errors.Is(err, ErrClassificationRefused) {
		t.Fatalf("a read-only handle accepted a write, or reported %q", CategoryOf(err))
	}
	if _, err := reader.Query(ctx, "SELECT count() FROM events", ""); err != nil {
		t.Fatalf("a read-only handle should still read: %v", err)
	}
}

// ------------------------------------------------------------------- leasing

func TestASecondWriterIsRefusedWhileTheLeaseIsLive(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	first, _, _ := f.mustOpen(OpenOptions{Owner: "worker-1"})
	defer first.Close(ctx)

	_, _, _, err := f.open(OpenOptions{Owner: "worker-2"})
	if !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("category is %q, want lease_held: %v", CategoryOf(err), err)
	}
	var e *Error
	errors.As(err, &e)
	if e.Owner != "worker-1" {
		t.Errorf("the error names owner %q, want worker-1", e.Owner)
	}
}

func TestAnExpiredLeaseCanBeTakenOver(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	// A TTL short enough to lapse during the test, with heartbeat and skew
	// scaled to match — the validate() rules still apply.
	brief := Tuning{
		LeaseTTL:           300 * time.Millisecond,
		HeartbeatInterval:  90 * time.Millisecond,
		ClockSkewAllowance: 50 * time.Millisecond,
		CommitDeadline:     2 * time.Second,
		MaxCommitAttempts:  3,
	}
	first, _, _ := f.mustOpen(OpenOptions{Owner: "worker-1", Tuning: brief})
	// Stop renewing without releasing, the way a crashed writer would.
	first.signalHeartbeatStop()
	generation := f.head().Lease.Generation

	// Past the expiry and past the skew allowance.
	time.Sleep(brief.LeaseTTL + brief.ClockSkewAllowance + 100*time.Millisecond)

	second, _, _, err := f.open(OpenOptions{Owner: "worker-2", Tuning: brief})
	if err != nil {
		t.Fatalf("an expired lease should be takeable: %v", err)
	}
	defer second.Close(ctx)
	if got := f.head().Lease.Generation; got != generation+1 {
		t.Fatalf("generation went from %d to %d, want one more", generation, got)
	}

	// The old writer is fenced the moment it tries to commit again.
	if _, err := first.Execute(ctx, "INSERT INTO events VALUES (1)"); !errors.Is(err, ErrLeaseFenced) {
		t.Fatalf("the superseded writer reported %q, want lease_fenced: %v", CategoryOf(err), err)
	}
}

func TestForceTakesAnUnexpiredLeaseAndFencesTheHolder(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	first, _, _ := f.mustOpen(OpenOptions{Owner: "worker-1"})
	mustExecute(t, first, "INSERT INTO events VALUES (1)")
	generation := f.head().Lease.Generation

	second, _, _, err := f.open(OpenOptions{Owner: "worker-2", Force: true})
	if err != nil {
		t.Fatalf("force should take an unexpired lease: %v", err)
	}
	defer second.Close(ctx)
	if got := f.head().Lease.Generation; got != generation+1 {
		t.Fatalf("generation went from %d to %d", generation, got)
	}

	// The dispossessed writer learns at its next commit, and its unflushed
	// work is what the force warning is about.
	_, err = first.Flush(ctx)
	if !errors.Is(err, ErrLeaseFenced) {
		t.Fatalf("the dispossessed writer reported %q, want lease_fenced: %v", CategoryOf(err), err)
	}
	if !first.IsFenced() {
		t.Fatal("a fenced writer should say so")
	}
	// And it stays unusable rather than recovering on the next call.
	if _, err := first.Execute(ctx, "INSERT INTO events VALUES (2)"); !errors.Is(err, ErrLeaseFenced) {
		t.Fatalf("a fenced handle accepted work, or reported %q", CategoryOf(err))
	}
}

func TestCloseReleasesTheLeaseForTheNextWriter(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	first, _, _ := f.mustOpen(OpenOptions{Owner: "worker-1"})
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	head := f.head()
	if head.Lease.Owner != nil || head.Lease.Instance != nil || head.Lease.ExpiresAt != nil {
		t.Fatalf("close left the lease as %+v, want fully released", head.Lease)
	}
	if head.Lease.Generation != 1 {
		t.Fatalf("a release moved the generation to %d", head.Lease.Generation)
	}

	second, _, _, err := f.open(OpenOptions{Owner: "worker-2"})
	if err != nil {
		t.Fatalf("a released lease should be free: %v", err)
	}
	defer second.Close(ctx)
	if got := f.head().Lease.Generation; got != 2 {
		t.Fatalf("taking a released lease produced generation %d, want 2", got)
	}
}

func TestOperationsAfterCloseAreRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	obj, _, _ := f.mustOpen(OpenOptions{})
	if err := obj.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := obj.Execute(ctx, "INSERT INTO events VALUES (1)"); !errors.Is(err, ErrClosed) {
		t.Fatalf("category is %q, want closed", CategoryOf(err))
	}
	if _, err := obj.Query(ctx, "SELECT 1", ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("category is %q, want closed", CategoryOf(err))
	}
	// Idempotent, and it does not report the first close's success twice as
	// something new.
	if err := obj.Close(ctx); err != nil {
		t.Fatalf("a second close reported %v", err)
	}
}

// A failed open must not strand the lease until the TTL expires.
func TestAFailedOpenReleasesWhatItTook(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	first, _, _ := f.mustOpen(OpenOptions{})
	mustExecute(t, first, "INSERT INTO events VALUES (1)")
	if _, err := first.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// Break the replay the next open has to perform.
	engine := newFakeEngine()
	engine.runErr = func(string) error { return newError(CategoryEngine, "durable: engine says no") }
	ns, err := NewNamespace("file:///unused", NamespaceOptions{
		EngineFactory:  engine.factory(),
		BackendFactory: func(context.Context, string) (Backend, error) { return f.backend, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ns.Open(ctx, "orders", OpenOptions{ScratchRoot: t.TempDir()}); err == nil {
		t.Fatal("an open whose replay fails must fail")
	}

	head := f.head()
	if head.Lease.Owner != nil {
		t.Fatalf("the failed open left the lease held: %+v", head.Lease)
	}
	// So the next writer gets in without waiting out a TTL.
	next, _, _, err := f.open(OpenOptions{})
	if err != nil {
		t.Fatalf("the lease was not released: %v", err)
	}
	next.Close(ctx)
}

// ------------------------------------------------------------- fault matrix

// A published segment whose head commit fails leaves the old manifest
// authoritative and the local buffer intact, so nothing is lost and a retry
// publishes a fresh unique segment.
func TestAFailedHeadCommitKeepsTheBufferAndTheOldManifest(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	obj, _, _ := f.mustOpen(OpenOptions{})
	defer obj.Close(ctx)

	mustExecute(t, obj, "INSERT INTO events VALUES (1)")

	// Refuse every head commit definitively.
	f.backend.mu.Lock()
	f.backend.onReplace = func(key string, _ int) (ReplaceOutcome, error, bool) {
		if key == HeadKey {
			return ReplaceOutcome{Status: ReplaceNotMatched}, nil, true
		}
		return ReplaceOutcome{}, nil, false
	}
	f.backend.mu.Unlock()

	_, err := obj.Flush(ctx)
	if err == nil {
		t.Fatal("a flush whose head commit never lands must fail")
	}
	// Every attempt was a definite refusal, so the commit provably did not
	// happen and a later retry is safe.
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("category is %q, want timeout: %v", CategoryOf(err), err)
	}
	if obj.PendingStatements() != 1 {
		t.Fatalf("the buffer holds %d statements; a failed flush must keep them",
			obj.PendingStatements())
	}
	if len(f.head().Manifest.WAL) != 0 || f.head().Manifest.Seq != 0 {
		t.Fatalf("the manifest moved anyway: %+v", f.head().Manifest)
	}

	// With the fault removed, the retry lands.
	f.backend.mu.Lock()
	f.backend.onReplace = nil
	f.backend.mu.Unlock()
	ref, err := obj.Flush(ctx)
	if err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	head := f.head()
	if len(head.Manifest.WAL) != 1 || head.Manifest.WAL[0].Key != ref.Key {
		t.Fatalf("the retry did not commit: %+v", head.Manifest)
	}
}

// The response to a head commit can be lost after it landed. Re-reading and
// finding our own intent, with our own lease still on it, is what turns that
// into a success rather than a double publish.
func TestACommitWhoseResponseWasLostReconcilesToSuccess(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	obj, _, _ := f.mustOpen(OpenOptions{})
	defer obj.Close(ctx)

	mustExecute(t, obj, "INSERT INTO events VALUES (1)")

	// The write goes through; the answer does not come back.
	f.backend.mu.Lock()
	f.backend.afterReplace = func(key string, _ int, outcome ReplaceOutcome) (ReplaceOutcome, error) {
		if key == HeadKey && outcome.Status == ReplaceDone {
			return ReplaceOutcome{Status: ReplaceAmbiguous}, nil
		}
		return outcome, nil
	}
	f.backend.mu.Unlock()

	ref, err := obj.Flush(ctx)
	if err != nil {
		t.Fatalf("a committed write reported as ambiguous should reconcile to success: %v", err)
	}
	head := f.head()
	if len(head.Manifest.WAL) != 1 || head.Manifest.WAL[0].Key != ref.Key {
		t.Fatalf("the manifest is %+v", head.Manifest)
	}
	if obj.PendingStatements() != 0 {
		t.Fatalf("%d statements still pending after a reconciled commit", obj.PendingStatements())
	}
	// And the object is still usable, with a token that matches the store.
	f.backend.mu.Lock()
	f.backend.afterReplace = nil
	f.backend.mu.Unlock()
	mustExecute(t, obj, "INSERT INTO events VALUES (2)")
	if _, err := obj.Flush(ctx); err != nil {
		t.Fatalf("the adopted token does not work: %v", err)
	}
}

// When ambiguity cannot be resolved, the answer is commit_ambiguous. A caller
// told "committed" would be undefended; one told "ambiguous" can check.
func TestAnUnresolvableCommitIsReportedAsAmbiguous(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	obj, _, _ := f.mustOpen(OpenOptions{Tuning: Tuning{
		CommitDeadline: 2 * time.Second, MaxCommitAttempts: 2,
	}})
	defer obj.Close(ctx)

	mustExecute(t, obj, "INSERT INTO events VALUES (1)")

	f.backend.mu.Lock()
	f.backend.onReplace = func(key string, _ int) (ReplaceOutcome, error, bool) {
		if key == HeadKey {
			return ReplaceOutcome{Status: ReplaceAmbiguous}, nil, true
		}
		return ReplaceOutcome{}, nil, false
	}
	f.backend.mu.Unlock()

	_, err := obj.Flush(ctx)
	if !errors.Is(err, ErrCommitAmbiguous) {
		t.Fatalf("category is %q, want commit_ambiguous: %v", CategoryOf(err), err)
	}
	var e *Error
	errors.As(err, &e)
	if e.Key == "" {
		t.Error("an ambiguous commit should name the key, for triage")
	}
	if obj.PendingStatements() != 1 {
		t.Fatalf("the buffer holds %d statements; nothing was proven, so nothing is dropped",
			obj.PendingStatements())
	}
}

// An upload whose response was lost is settled by re-reading the unique key
// and comparing the digest — matching bytes are ours.
func TestAnAmbiguousUploadIsSettledByItsDigest(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	obj, _, _ := f.mustOpen(OpenOptions{})
	defer obj.Close(ctx)

	mustExecute(t, obj, "INSERT INTO events VALUES (1)")

	// The bytes land; the caller is told nothing useful. Reconcile has to read
	// the segment back and compare its digest to find out.
	f.backend.mu.Lock()
	f.backend.afterPutBytes = func(key string, _ int, outcome PutOutcome) (PutOutcome, error) {
		if strings.HasPrefix(key, "wal/") && outcome == PutCreated {
			return PutAmbiguous, nil
		}
		return outcome, nil
	}
	f.backend.mu.Unlock()

	if _, err := obj.Flush(ctx); err != nil {
		t.Fatalf("an upload that landed should reconcile to success: %v", err)
	}
}

func TestAnUploadThatNeverLandedIsAmbiguous(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	obj, _, _ := f.mustOpen(OpenOptions{})
	defer obj.Close(ctx)

	mustExecute(t, obj, "INSERT INTO events VALUES (1)")

	f.backend.mu.Lock()
	f.backend.onPutBytes = func(key string, _ int) (PutOutcome, error, bool) {
		if strings.HasPrefix(key, "wal/") {
			// Reported uncertain, and nothing was written.
			return PutAmbiguous, nil, true
		}
		return 0, nil, false
	}
	f.backend.mu.Unlock()

	_, err := obj.Flush(ctx)
	if !errors.Is(err, ErrCommitAmbiguous) {
		t.Fatalf("category is %q, want commit_ambiguous: %v", CategoryOf(err), err)
	}
}

// A checkpoint whose head commit fails leaves the old base and WAL restorable,
// and keeps the local buffer.
func TestAFailedCheckpointCommitLeavesTheOldStateAuthoritative(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	obj, _, _ := f.mustOpen(OpenOptions{})

	mustExecute(t, obj, "INSERT INTO events VALUES (1)")
	if _, err := obj.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	mustExecute(t, obj, "INSERT INTO events VALUES (2)")
	before := f.head().Manifest

	f.backend.mu.Lock()
	f.backend.onReplace = func(key string, _ int) (ReplaceOutcome, error, bool) {
		if key == HeadKey {
			return ReplaceOutcome{Status: ReplaceNotMatched}, nil, true
		}
		return ReplaceOutcome{}, nil, false
	}
	f.backend.mu.Unlock()

	if _, err := obj.Checkpoint(ctx); err == nil {
		t.Fatal("a checkpoint whose commit never lands must fail")
	}
	after := f.head().Manifest
	if after.Base != nil || len(after.WAL) != len(before.WAL) || after.Seq != before.Seq {
		t.Fatalf("the manifest moved anyway: was %+v, now %+v", before, after)
	}
	if obj.PendingStatements() != 1 {
		t.Fatalf("the buffer holds %d statements; the checkpoint was never committed",
			obj.PendingStatements())
	}

	// The old base plus the old WAL still restores, which is the property that
	// makes the failure survivable. A read-only open is the way to check it
	// without the writer's close flushing the pending statement first.
	f.backend.mu.Lock()
	f.backend.onReplace = nil
	f.backend.mu.Unlock()

	reader, engine, _ := f.mustOpen(OpenOptions{ReadOnly: true})
	if len(engine.statements()) != 1 {
		t.Fatalf("recovery produced %v, want only the committed statement", engine.statements())
	}
	reader.Close(ctx)

	// And the writer was never fenced by the failure, so its close still
	// commits what it was holding.
	if err := obj.Close(ctx); err != nil {
		t.Fatalf("close after a failed checkpoint: %v", err)
	}
	if len(f.head().Manifest.WAL) != 2 {
		t.Fatalf("close published %d segments, want the original plus the pending one",
			len(f.head().Manifest.WAL))
	}
}

// ------------------------------------------------------------------ corrupt

func TestAMissingBaseIsCorruptNotAnEmptyObject(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	obj, _, _ := f.mustOpen(OpenOptions{})
	mustExecute(t, obj, "INSERT INTO events VALUES (1)")
	ref, err := obj.Checkpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := obj.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// Take the base away, the way a lifecycle rule or a bad clean-up would.
	if err := removeFromStore(f, ref.Key); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = f.open(OpenOptions{})
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("category is %q, want corrupt: %v", CategoryOf(err), err)
	}
}

func TestAMissingWALSegmentIsCorrupt(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	obj, _, _ := f.mustOpen(OpenOptions{})
	mustExecute(t, obj, "INSERT INTO events VALUES (1)")
	ref, err := obj.Flush(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := obj.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if err := removeFromStore(f, ref.Key); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = f.open(OpenOptions{})
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("category is %q, want corrupt: %v", CategoryOf(err), err)
	}
}

func TestATamperedWALSegmentIsCorrupt(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	obj, _, _ := f.mustOpen(OpenOptions{})
	mustExecute(t, obj, "INSERT INTO events VALUES (1)")
	ref, err := obj.Flush(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := obj.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// Same length, different bytes: only the digest catches this, which is why
	// the manifest records one.
	if err := overwriteInStore(f, ref.Key, []byte(`{"sql":"INSERT INTO events VALUES (9)"}`+"\n")); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = f.open(OpenOptions{})
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("category is %q, want corrupt: %v", CategoryOf(err), err)
	}
}

// ------------------------------------------------------ classification gates

func TestExecuteAndQueryGatesFollowTheAnalysis(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	obj, _, _ := f.mustOpen(OpenOptions{Database: "mem"})
	defer obj.Close(ctx)

	refusals := []struct {
		name string
		sql  string
		want *Error
	}{
		{"two statements", "INSERT INTO t VALUES (1); INSERT INTO t VALUES (2)", ErrClassificationRefused},
		{"PARALLEL WITH", "INSERT INTO t VALUES (1) PARALLEL WITH INSERT INTO t VALUES (2)", ErrClassificationRefused},
		{"a read through Execute", "SELECT 1", ErrClassificationRefused},
		{"session control", "SET max_threads = 1", ErrClassificationRefused},
		{"a global mutation", "CREATE FUNCTION plus_one AS (x) -> x + 1", ErrClassificationRefused},
		{"a write to system", "INSERT INTO system.something VALUES (1)", ErrClassificationRefused},
		{"database lifecycle", "DROP DATABASE mem", ErrClassificationRefused},
		{"another database", "INSERT INTO other.t VALUES (1)", ErrClassificationRefused},
		{"a table function", "INSERT INTO FUNCTION s3('...') SELECT * FROM t", ErrClassificationRefused},
		{"a file sink", "SELECT * FROM t INTO OUTFILE '/tmp/x'", ErrClassificationRefused},
		{"unparseable", "not sql at all", ErrClassificationRefused},
		{"a credential", "INSERT INTO t SELECT * FROM s3('u', 'k', 'secret_access_key')", ErrSecretRefused},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			_, err := obj.Execute(ctx, tc.sql)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Execute reported %q, want %q: %v", CategoryOf(err), tc.want.Category, err)
			}
			// The refusal must not echo the statement, or a secret_refused
			// message would carry the credential it exists to keep out.
			if strings.Contains(err.Error(), "secret_access_key") {
				t.Fatal("the error message quoted the statement")
			}
			if obj.PendingStatements() != 0 {
				t.Fatalf("a refused statement was buffered")
			}
		})
	}

	// Query is gated the same way, from the other side.
	for _, sql := range []string{
		"INSERT INTO t VALUES (1)",
		"SELECT 1; SELECT 2",
		"SET max_threads = 1",
		"not sql at all",
	} {
		if _, err := obj.Query(ctx, sql, ""); !errors.Is(err, ErrClassificationRefused) {
			t.Errorf("Query accepted %q, or reported %q", sql, CategoryOf(err))
		}
	}

	// A read-only statement carrying a credential runs — it never reaches the
	// WAL, so it is a logging concern rather than a durability one.
	if _, err := obj.Query(ctx, "SELECT * FROM s3('u', 'k', 'secret_access_key')", ""); err != nil {
		t.Errorf("a secret-bearing read should be allowed: %v", err)
	}
}

// A statement the engine refuses must not be logged: replay would run
// something that never took effect.
func TestAFailedStatementIsNotLogged(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	engine := newFakeEngine()
	engine.runErr = func(sql string) error {
		if strings.Contains(sql, "(2)") {
			return newError(CategoryEngine, "durable: engine says no")
		}
		return nil
	}
	ns, err := NewNamespace("file:///unused", NamespaceOptions{
		EngineFactory:  engine.factory(),
		BackendFactory: func(context.Context, string) (Backend, error) { return f.backend, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	obj, _, err := ns.Open(ctx, "orders", OpenOptions{Database: "mem", ScratchRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer obj.Close(ctx)

	mustExecute(t, obj, "INSERT INTO events VALUES (1)")
	if _, err := obj.Execute(ctx, "INSERT INTO events VALUES (2)"); err == nil {
		t.Fatal("the engine refused this statement")
	}
	if obj.PendingStatements() != 1 {
		t.Fatalf("the buffer holds %d statements, want only the one that ran",
			obj.PendingStatements())
	}
}

// ------------------------------------------------------------------ barriers

func TestFlushThroughCoalescesOntoOneCommit(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	obj, _, _ := f.mustOpen(OpenOptions{})
	defer obj.Close(ctx)

	first := mustExecute(t, obj, "INSERT INTO events VALUES (1)")
	second := mustExecute(t, obj, "INSERT INTO events VALUES (2)")

	if err := obj.FlushThrough(ctx, second); err != nil {
		t.Fatalf("flush through the second ticket: %v", err)
	}
	// Both are covered by that one segment, so the earlier watermark is
	// already met and costs no round-trip.
	if err := obj.FlushThrough(ctx, first); err != nil {
		t.Fatalf("flush through the first ticket: %v", err)
	}
	if got := len(f.head().Manifest.WAL); got != 1 {
		t.Fatalf("%d segments were published, want 1", got)
	}
	if obj.Stats().CommittedStatements != 2 {
		t.Fatalf("committed %d statements", obj.Stats().CommittedStatements)
	}
}

func TestCloseFlushesWhatIsPending(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	obj, _, _ := f.mustOpen(OpenOptions{})
	mustExecute(t, obj, "INSERT INTO events VALUES (1)")
	if err := obj.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := len(f.head().Manifest.WAL); got != 1 {
		t.Fatalf("close published %d segments, want 1", got)
	}
}

// A close that cannot reach its durability barrier must say so — and still
// give back the engine and the scratch directory.
func TestCloseReportsAFailedFlushAndStillReleasesResources(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	obj, engine, _ := f.mustOpen(OpenOptions{Tuning: Tuning{
		CommitDeadline: time.Second, MaxCommitAttempts: 2,
	}})
	mustExecute(t, obj, "INSERT INTO events VALUES (1)")

	f.backend.mu.Lock()
	f.backend.onReplace = func(key string, _ int) (ReplaceOutcome, error, bool) {
		if key == HeadKey {
			return ReplaceOutcome{Status: ReplaceNotMatched}, nil, true
		}
		return ReplaceOutcome{}, nil, false
	}
	f.backend.mu.Unlock()

	scratch := obj.ScratchPath()
	if err := obj.Close(ctx); err == nil {
		t.Fatal("close swallowed a failed flush")
	}
	if engine.isStarted() {
		t.Error("close left the engine open")
	}
	if _, err := os.Stat(scratch); err == nil {
		t.Error("close left the scratch directory behind")
	}
	if obj.Stats().State != "closed" {
		t.Errorf("state is %q after close", obj.Stats().State)
	}
}

// ------------------------------------------------------------- engine gating

func TestAnEngineBelowMinReaderIsRefusedAtOpen(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	obj, _, _ := f.mustOpen(OpenOptions{})
	if err := obj.Close(ctx); err != nil {
		t.Fatal(err)
	}

	older := newFakeEngine()
	older.version = "26.7.0"
	ns, err := NewNamespace("file:///unused", NamespaceOptions{
		EngineFactory:  older.factory(),
		BackendFactory: func(context.Context, string) (Backend, error) { return f.backend, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = ns.Open(ctx, "orders", OpenOptions{ScratchRoot: t.TempDir()})
	if !errors.Is(err, ErrEngineIncompatible) {
		t.Fatalf("category is %q, want engine_incompatible: %v", CategoryOf(err), err)
	}
	// The refusal costs nothing: no lease was taken, so the object is still
	// free for a build that can read it.
	if f.head().Lease.Owner != nil {
		t.Fatalf("the refused open took the lease: %+v", f.head().Lease)
	}
}

// A later release opens an object an earlier one wrote, and records itself as
// the producer while leaving the floor alone.
func TestALaterEngineOpensAnEarlierObject(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	obj, _, _ := f.mustOpen(OpenOptions{})
	mustExecute(t, obj, "INSERT INTO events VALUES (1)")
	if _, err := obj.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := obj.Close(ctx); err != nil {
		t.Fatal(err)
	}

	newer := newFakeEngine()
	newer.version = "26.9.1"
	ns, err := NewNamespace("file:///unused", NamespaceOptions{
		EngineFactory:  newer.factory(),
		BackendFactory: func(context.Context, string) (Backend, error) { return f.backend, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	reopened, _, err := ns.Open(ctx, "orders", OpenOptions{ScratchRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("a later engine should open an earlier object: %v", err)
	}
	if len(newer.statements()) != 1 {
		t.Fatalf("the later engine restored %v", newer.statements())
	}
	if err := reopened.Close(ctx); err != nil {
		t.Fatal(err)
	}

	head := f.head()
	if head.Engine.Version != "26.9.1" {
		t.Errorf("engine.version is %q; it records who wrote last", head.Engine.Version)
	}
	if head.Engine.MinReader != "26.9.1" {
		// The new base has not been written yet by this engine, but every head
		// write raises min_reader to the writer's own version, because that
		// writer's WAL may use anything it supports.
		t.Errorf("min_reader is %q, want the writer's own version", head.Engine.MinReader)
	}
	// And an object whose floor has risen refuses the older engine.
	if err := assertEngineCompatible(head, runningEngine{version: "26.7.2-rc.2", backupFormat: 1}); !errors.Is(err, ErrEngineIncompatible) {
		t.Errorf("the raised floor does not refuse an older reader: %v", err)
	}
}

// ---------------------------------------------------------------- protocol

func TestAnUnknownWriterFeatureAllowsReadingButNotWriting(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	// Publish a head that declares a writer feature this build has never
	// heard of, the way a future revision would.
	head := coldHead("mem", "26.7.2-rc.2", BackupFormatBaseline)
	head.Protocol.WriterFeatures = []string{"data-wal"}
	data, err := serializeHead(head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.backend.PutBytesIfAbsent(ctx, HeadKey, data); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := f.open(OpenOptions{}); !errors.Is(err, ErrProtocolUnsupported) {
		t.Fatalf("taking the lease reported %q, want protocol_unsupported", CategoryOf(err))
	}
	reader, _, _, err := f.open(OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("an unknown writer feature must still allow a read-only open: %v", err)
	}
	reader.Close(ctx)
}

func TestAFutureProtocolVersionRefusesEvenAReader(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	head := coldHead("mem", "26.7.2-rc.2", BackupFormatBaseline)
	head.Protocol.Version = ProtocolVersion + 1
	data, err := serializeHead(head, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.backend.PutBytesIfAbsent(ctx, HeadKey, data); err != nil {
		t.Fatal(err)
	}
	for _, readOnly := range []bool{false, true} {
		if _, _, _, err := f.open(OpenOptions{ReadOnly: readOnly}); !errors.Is(err, ErrProtocolUnsupported) {
			t.Fatalf("readOnly=%v reported %q, want protocol_unsupported", readOnly, CategoryOf(err))
		}
	}
}

// Unknown fields must survive a whole session: open, execute, flush,
// checkpoint, close.
func TestUnknownFieldsSurviveASession(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	head := coldHead("mem", "26.7.2-rc.2", BackupFormatBaseline)
	base, err := serializeHead(head, nil)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(base, &raw); err != nil {
		t.Fatal(err)
	}
	raw["future_top_level"] = "keep me"
	raw["manifest"].(map[string]any)["future_manifest"] = []any{1.0, 2.0}
	withUnknowns, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.backend.PutBytesIfAbsent(ctx, HeadKey, withUnknowns); err != nil {
		t.Fatal(err)
	}

	obj, _, _ := f.mustOpen(OpenOptions{})
	mustExecute(t, obj, "INSERT INTO events VALUES (1)")
	if _, err := obj.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := obj.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if err := obj.Close(ctx); err != nil {
		t.Fatal(err)
	}

	final, _, err := f.backend.GetBytes(ctx, HeadKey)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(final, &out); err != nil {
		t.Fatal(err)
	}
	if out["future_top_level"] != "keep me" {
		t.Errorf("a top-level unknown field was lost: %v", out)
	}
	if _, ok := out["manifest"].(map[string]any)["future_manifest"]; !ok {
		t.Errorf("an unknown manifest field was lost: %v", out["manifest"])
	}
}

// ------------------------------------------------------------------ tuning

func TestTuningIsValidated(t *testing.T) {
	f := newFixture(t)
	cases := []Tuning{
		{LeaseTTL: -time.Second},
		{HeartbeatInterval: -time.Second},
		// Heartbeat has to leave room for a retry before expiry.
		{LeaseTTL: 10 * time.Second, HeartbeatInterval: 9 * time.Second},
		{MaxCommitAttempts: -1},
	}
	for _, tuning := range cases {
		if _, _, _, err := f.open(OpenOptions{Tuning: tuning}); err == nil {
			t.Errorf("tuning %+v should have been refused", tuning)
		}
	}
}

func TestStatsDescribeOneConsistentMoment(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	obj, _, _ := f.mustOpen(OpenOptions{Database: "mem", Owner: "worker-1"})
	defer obj.Close(ctx)

	mustExecute(t, obj, "INSERT INTO events VALUES (1)")
	if _, err := obj.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	mustExecute(t, obj, "INSERT INTO events VALUES (2)")

	stats := obj.Stats()
	if stats.ObjectID != "orders" || stats.Database != "mem" || stats.Owner != "worker-1" {
		t.Errorf("stats identify the object as %+v", stats)
	}
	if stats.State != "open" || stats.Fenced {
		t.Errorf("state is %q fenced=%v", stats.State, stats.Fenced)
	}
	if stats.ExecutedStatements != 2 || stats.CommittedStatements != 1 || stats.PendingStatements != 1 {
		t.Errorf("counters are executed=%d committed=%d pending=%d",
			stats.ExecutedStatements, stats.CommittedStatements, stats.PendingStatements)
	}
	if stats.PendingBytes <= 0 {
		t.Errorf("pending bytes is %d", stats.PendingBytes)
	}
	if stats.CommittedSeq != 1 || stats.WALSegments != 1 {
		t.Errorf("seq=%d segments=%d", stats.CommittedSeq, stats.WALSegments)
	}
	if stats.LeaseExpiresAt.IsZero() {
		t.Error("a writer should report when it stops believing its lease")
	}
}

func TestNamespaceRefusesAnUnknownScheme(t *testing.T) {
	if _, err := NewNamespace("gs://bucket/prefix", NamespaceOptions{}); err == nil {
		t.Fatal("an unregistered scheme should be refused at construction")
	}
	// And a registered one is accepted without touching the network.
	if _, err := NewNamespace("s3://bucket/prefix?region=us-east-1", NamespaceOptions{
		BackendFactory: func(context.Context, string) (Backend, error) { return nil, nil },
	}); err != nil {
		t.Fatalf("s3 should be registered: %v", err)
	}
}

func TestNamespaceRefusesAHierarchicalObjectID(t *testing.T) {
	f := newFixture(t)
	ns, err := NewNamespace("file:///unused", NamespaceOptions{
		EngineFactory:  newFakeEngine().factory(),
		BackendFactory: func(context.Context, string) (Backend, error) { return f.backend, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ns.Open(context.Background(), "tenant/user", OpenOptions{}); err == nil {
		t.Fatal("a hierarchical object id should be refused")
	}
}
