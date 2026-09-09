package durable

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The Durable V1 object state machine (contract §5).
//
// Everything the protocol calls hard lives here: lease acquisition and
// fencing, the operation queue, WAL publication, checkpoint, and the reconcile
// that decides whether a request whose response vanished actually committed.
// None of it touches a native library — the engine arrives as an Engine, the
// object store as a Backend.
//
// A few invariants are worth stating up front, because most of the code below
// exists to hold one of them:
//
//   - **Execute succeeding does not mean the write is durable.** It means the
//     statement ran locally and joined the buffer. Durability is Flush. A
//     product that answers a client before flushing is choosing to lose that
//     write on a crash, and it should choose that knowingly.
//   - **Nothing is ever reported as committed without proof.** Every commit
//     path can end in commit_ambiguous, which is an honest answer. Reporting a
//     lost response as success would be the one failure mode a caller cannot
//     defend against.
//   - **A writer that cannot confirm its lease stops writing.** Not on the
//     next error — at the moment its locally believed validity window lapses.
//     The alternative is two processes each convinced they are the only
//     writer.
//   - **Local resources are always released.** A close that fails to flush
//     still closes the connection and removes the scratch directory, and still
//     reports the failure.

// Tuning holds the lease and commit parameters. The contract lets a binding
// choose these but requires the defaults, their units and their rules to be
// documented — so they are, here, and the same values drive the tests.
//
// A zero field takes its default, so a caller overriding one leaves the rest
// alone.
type Tuning struct {
	// LeaseTTL is how long a lease stays valid after a successful head write.
	// Default 30s.
	LeaseTTL time.Duration

	// HeartbeatInterval is how often the writer renews. It must be at most a
	// third of the TTL, so two consecutive heartbeat failures still leave a
	// window to notice and fence. Default 10s.
	HeartbeatInterval time.Duration

	// ClockSkewAllowance is how far past a recorded expiry another writer
	// waits before treating a lease as abandoned. This is a bound on
	// disagreement between two machines' clocks, not a grace period for a slow
	// writer. Default 5s.
	ClockSkewAllowance time.Duration

	// CommitDeadline is how long a single commit may spend retrying and
	// reconciling. Default 30s.
	CommitDeadline time.Duration

	// MaxCommitAttempts is how many attempts a commit makes inside that
	// deadline before giving up. Default 5.
	MaxCommitAttempts int
}

// DefaultTuning is what an object uses when a caller says nothing.
var DefaultTuning = Tuning{
	LeaseTTL:           30 * time.Second,
	HeartbeatInterval:  10 * time.Second,
	ClockSkewAllowance: 5 * time.Second,
	CommitDeadline:     30 * time.Second,
	MaxCommitAttempts:  5,
}

func (t Tuning) withDefaults() Tuning {
	out := t
	if out.LeaseTTL == 0 {
		out.LeaseTTL = DefaultTuning.LeaseTTL
	}
	if out.HeartbeatInterval == 0 {
		out.HeartbeatInterval = DefaultTuning.HeartbeatInterval
	}
	if out.ClockSkewAllowance == 0 {
		out.ClockSkewAllowance = DefaultTuning.ClockSkewAllowance
	}
	if out.CommitDeadline == 0 {
		out.CommitDeadline = DefaultTuning.CommitDeadline
	}
	if out.MaxCommitAttempts == 0 {
		out.MaxCommitAttempts = DefaultTuning.MaxCommitAttempts
	}
	return out
}

func (t Tuning) validate() error {
	// Every one of these ends up in arithmetic that decides whether this
	// process still owns the object, so a nonsensical value is refused rather
	// than propagated into an expiry nobody can take over.
	fields := []struct {
		name  string
		value time.Duration
	}{
		{"LeaseTTL", t.LeaseTTL},
		{"HeartbeatInterval", t.HeartbeatInterval},
		{"ClockSkewAllowance", t.ClockSkewAllowance},
		{"CommitDeadline", t.CommitDeadline},
	}
	for _, f := range fields {
		if f.value <= 0 {
			return newError(CategoryBackend, "durable: Tuning.%s must be positive, got %s",
				f.name, f.value)
		}
	}
	if t.MaxCommitAttempts <= 0 {
		return newError(CategoryBackend, "durable: Tuning.MaxCommitAttempts must be positive, got %d",
			t.MaxCommitAttempts)
	}
	if t.HeartbeatInterval*3 > t.LeaseTTL {
		return newError(CategoryBackend,
			"durable: Tuning.HeartbeatInterval (%s) must be at most a third of LeaseTTL (%s); "+
				"the contract requires room for a retry before expiry",
			t.HeartbeatInterval, t.LeaseTTL)
	}
	return nil
}

// OpenOptions configures one open.
type OpenOptions struct {
	// ReadOnly opens without a writer lease. A read-only handle serves the
	// manifest as it stood at open.
	ReadOnly bool

	// Force takes an unexpired lease from its current holder. This is an
	// administrator action: the previous writer's unflushed local work is
	// lost, and it learns this only when its next commit is fenced.
	Force bool

	// ExistingOnly refuses to create the object if it does not exist.
	ExistingOnly bool

	// Owner is the visible writer name recorded in the lease. Observability
	// only. Defaults to a name derived from the process id.
	Owner string

	// Database is the database this object holds. Only used when creating a
	// cold object; an existing object's head is authoritative. Defaults to
	// "default".
	Database string

	// ScratchRoot is the parent directory for the scratch tree. Defaults to
	// the system temporary directory.
	ScratchRoot string

	// OnRestoreProgress is called as the open restores base and replays WAL.
	// It is called synchronously and best-effort: a panic from it is not
	// caught, but it is never given a chance to fail the recovery, because it
	// returns nothing.
	OnRestoreProgress func(RestoreProgress)

	// Tuning overrides the lease and commit defaults, field by field.
	Tuning Tuning
}

// RestorePhase names a stage of the restore an open performs.
type RestorePhase string

const (
	PhaseCreatingDatabase RestorePhase = "creating-database"
	PhaseRestoringBase    RestorePhase = "restoring-base"
	PhaseReplayingWAL     RestorePhase = "replaying-wal"
	PhaseReady            RestorePhase = "ready"
)

// RestoreProgress reports progress through an open's restore, for a caller
// that has to show one. Recovering a large object is not instantaneous, and
// "starting" with no further detail is indistinguishable from "stuck" — which
// is the state an operator most needs to tell apart.
type RestoreProgress struct {
	Phase RestorePhase

	// Database is set during PhaseCreatingDatabase.
	Database string

	// Key and Size describe the object being fetched, during
	// PhaseRestoringBase and PhaseReplayingWAL.
	Key  string
	Size int64

	// Segment is the 1-based index of the WAL segment being replayed, and
	// Segments how many there are.
	Segment  int
	Segments int

	// Statements is how many statements have been replayed so far, across all
	// segments.
	Statements int64
}

// Stats is a consistent snapshot of an object's runtime state.
//
// It is taken as a whole rather than field by field, so what it reports is
// internally consistent: reading a generation and a sequence number through
// separate accessors can straddle a commit and describe a state that never
// existed.
//
// It carries no credentials and no SQL, so it is safe to log verbatim. Object
// ids and keys are safe; the statements that produced them are not, and are
// deliberately absent.
type Stats struct {
	ObjectID string
	Database string
	ReadOnly bool

	// State is "open", "closing" or "closed".
	State string

	// Fenced is true once this writer has lost, or given up on, its lease.
	Fenced bool

	Generation int64
	Owner      string
	Instance   string

	// LeaseExpiresAt is when this writer stops believing its lease. Zero for a
	// read-only open.
	LeaseExpiresAt time.Time

	// CommittedSeq is manifest.seq as last committed.
	CommittedSeq int64

	BaseKey     string
	WALSegments int

	// ExecutedStatements is how many statements this handle has run since it
	// opened, and CommittedStatements how many of those are in a committed WAL
	// segment.
	ExecutedStatements  int64
	CommittedStatements int64

	PendingStatements int
	PendingBytes      int64

	LastFlushAt      time.Time
	LastCheckpointAt time.Time
}

// WriteTicket is a watermark for one executed statement.
//
// FlushThrough turns it into a durability barrier, which is what lets a caller
// that expands one request into several statements answer the request as a
// whole without the protocol having to know about requests.
type WriteTicket struct {
	// Statement is the ordinal of the statement within this open session,
	// starting at 1.
	Statement int64
}

type scratch struct {
	root    string
	data    string
	backups string
	staging string
}

// Object is one open durable object.
type Object struct {
	id       string
	readOnly bool
	backend  Backend
	engine   Engine
	scratch  scratch
	tuning   Tuning
	instance string
	owner    string
	running  runningEngine
	progress func(RestoreProgress)

	// ops serializes whole logical operations; headGate serializes single head
	// compare-and-swaps, including heartbeat. See gate.go for why there are
	// two.
	ops      *gate
	headGate *gate

	// st guards every mutable field below. It is taken for short reads and
	// writes only, never held across storage or engine I/O — the two gates
	// provide that ordering.
	st                  sync.Mutex
	head                Head
	etag                string
	raw                 map[string]any
	walBuffer           []string
	walBufferBytes      int64
	statementCounter    int64
	committedStatements int64
	lastFlushAt         time.Time
	lastCheckpointAt    time.Time
	state               string
	fenced              bool
	leaseDeadline       time.Time

	heartbeatStop     chan struct{}
	heartbeatDone     chan struct{}
	heartbeatStopOnce sync.Once

	closeOnce sync.Once
	closeErr  error
}

// ---------------------------------------------------------------- accessors

// ID is the object's id within its namespace.
func (o *Object) ID() string { return o.id }

// ReadOnly reports whether this handle holds a lease.
func (o *Object) ReadOnly() bool { return o.readOnly }

// Database is the database this object holds, fixed for its lifetime.
func (o *Object) Database() string {
	o.st.Lock()
	defer o.st.Unlock()
	return o.head.Manifest.DB
}

// Generation is the lease generation currently recorded in the head.
func (o *Object) Generation() int64 {
	o.st.Lock()
	defer o.st.Unlock()
	return o.head.Lease.Generation
}

// Manifest returns a copy of the committed manifest. Observability; never
// authority.
func (o *Object) Manifest() Manifest {
	o.st.Lock()
	defer o.st.Unlock()
	return o.head.clone().Manifest
}

// ScratchPath is the absolute path of this object's private scratch tree.
func (o *Object) ScratchPath() string { return o.scratch.root }

// IsFenced reports whether this writer has lost, or given up on, its lease.
func (o *Object) IsFenced() bool {
	o.st.Lock()
	defer o.st.Unlock()
	return o.fenced
}

// PendingStatements is how many statements have run locally but are not yet in
// a published WAL segment.
func (o *Object) PendingStatements() int {
	o.st.Lock()
	defer o.st.Unlock()
	return len(o.walBuffer)
}

// PendingBytes is the encoded size of those statements.
//
// It is exposed so a caller can apply its own backpressure — flush on a size
// threshold rather than discovering the segment ceiling by hitting it.
func (o *Object) PendingBytes() int64 {
	o.st.Lock()
	defer o.st.Unlock()
	return o.walBufferBytes
}

// Stats returns a point-in-time snapshot for a status endpoint or a log line.
func (o *Object) Stats() Stats {
	o.st.Lock()
	defer o.st.Unlock()
	stats := Stats{
		ObjectID:            o.id,
		Database:            o.head.Manifest.DB,
		ReadOnly:            o.readOnly,
		State:               o.state,
		Fenced:              o.fenced,
		Generation:          o.head.Lease.Generation,
		Owner:               o.owner,
		Instance:            o.instance,
		CommittedSeq:        o.head.Manifest.Seq,
		WALSegments:         len(o.head.Manifest.WAL),
		ExecutedStatements:  o.statementCounter,
		CommittedStatements: o.committedStatements,
		PendingStatements:   len(o.walBuffer),
		PendingBytes:        o.walBufferBytes,
		LastFlushAt:         o.lastFlushAt,
		LastCheckpointAt:    o.lastCheckpointAt,
	}
	if !o.readOnly {
		stats.LeaseExpiresAt = o.leaseDeadline
	}
	if o.head.Manifest.Base != nil {
		stats.BaseKey = o.head.Manifest.Base.Key
	}
	return stats
}

// --------------------------------------------------------------- public API

// Query runs a read-only statement and returns its formatted result.
//
// It is refused unless core proves the text is exactly one READ_ONLY
// statement: the method name is not the gate, the analysis is. format is a
// ClickHouse output format; "" means JSONEachRow.
func (o *Object) Query(ctx context.Context, sql, format string) (string, error) {
	if err := o.ops.acquire(ctx); err != nil {
		return "", err
	}
	defer o.ops.release()

	if err := o.assertUsable(); err != nil {
		return "", err
	}
	analysis, err := o.engine.Analyze(ctx, sql, o.Database())
	if err != nil {
		return "", err
	}
	if err := assertQueryAllowed(analysis); err != nil {
		return "", err
	}
	if format == "" {
		format = "JSONEachRow"
	}
	return o.engine.Query(ctx, sql, format)
}

// Execute runs one mutating statement and buffers it for the WAL.
//
// The statement is executed first and buffered only on success, so a statement
// that failed is never replayed. The returned ticket is the watermark to pass
// to FlushThrough when the caller needs the write to be durable before it
// answers someone.
func (o *Object) Execute(ctx context.Context, sql string) (WriteTicket, error) {
	if err := o.ops.acquire(ctx); err != nil {
		return WriteTicket{}, err
	}
	defer o.ops.release()

	if err := o.assertWriter(); err != nil {
		return WriteTicket{}, err
	}
	database := o.Database()
	analysis, err := o.engine.Analyze(ctx, sql, database)
	if err != nil {
		return WriteTicket{}, err
	}
	if err := assertExecuteAllowed(analysis, database); err != nil {
		return WriteTicket{}, err
	}

	// Both limits are checked here rather than at flush, and before the
	// statement runs rather than after.
	//
	// The ordering is the point. A statement that has executed cannot be
	// un-executed, so a buffer that has grown past what a segment can hold can
	// no longer be flushed at all — every flush would fail encoding while the
	// local database has already moved on. Checkpoint can still rescue it,
	// since it archives the database rather than the buffer, but a caller has
	// to know that, and nothing would have warned it. Worse, that state is
	// reachable from a single transient failure: a flush that fails leaves the
	// buffer intact by design, and a caller that keeps writing walks straight
	// into it.
	//
	// Refusing here instead turns an unrecoverable flush into a recoverable
	// execute. Nothing has run, so the refusal costs only the statement.
	if err := assertStatementWithinLimit(sql); err != nil {
		return WriteTicket{}, err
	}
	lineBytes, err := walLineBytes(sql)
	if err != nil {
		return WriteTicket{}, err
	}
	o.st.Lock()
	projected := o.walBufferBytes + lineBytes
	o.st.Unlock()
	if projected > MaxWALSegmentBytes {
		return WriteTicket{}, &Error{
			Category: CategoryLimitExceeded,
			Message: fmt.Sprintf("durable: this statement would take the unflushed buffer to %d "+
				"bytes, over the %d-byte WAL segment limit; Flush or Checkpoint first",
				projected, MaxWALSegmentBytes),
			Limit:    MaxWALSegmentBytes,
			Observed: projected,
		}
	}

	if err := o.engine.Run(ctx, sql); err != nil {
		return WriteTicket{}, err
	}

	o.st.Lock()
	o.walBuffer = append(o.walBuffer, sql)
	o.walBufferBytes += lineBytes
	o.statementCounter++
	ticket := WriteTicket{Statement: o.statementCounter}
	o.st.Unlock()
	return ticket, nil
}

// Flush publishes the buffered statements as one immutable WAL segment and
// commits the reference.
//
// It returns the published reference, or a nil reference when there was
// nothing buffered.
func (o *Object) Flush(ctx context.Context) (*ObjectRef, error) {
	if err := o.ops.acquire(ctx); err != nil {
		return nil, err
	}
	defer o.ops.release()
	return o.flushLocked(ctx)
}

// FlushThrough is a durability barrier for one ticket.
//
// It returns immediately if that statement is already committed, which is what
// makes concurrent callers coalesce onto a single head write: the first
// through the queue publishes the segment covering all of them, and the rest
// find their watermark already met.
func (o *Object) FlushThrough(ctx context.Context, ticket WriteTicket) error {
	o.st.Lock()
	done := ticket.Statement <= o.committedStatements
	o.st.Unlock()
	if done {
		return nil
	}
	if err := o.ops.acquire(ctx); err != nil {
		return err
	}
	defer o.ops.release()

	o.st.Lock()
	done = ticket.Statement <= o.committedStatements
	o.st.Unlock()
	if done {
		return nil
	}
	_, err := o.flushLocked(ctx)
	return err
}

// Checkpoint replaces the base with a full backup of the current local
// database and clears the WAL list.
//
// It holds the operation queue for its whole duration, so nothing new is
// executed into a database that is being archived. Heartbeat is unaffected: it
// contends only for the head gate, which this takes just for the final commit.
func (o *Object) Checkpoint(ctx context.Context) (*ObjectRef, error) {
	if err := o.ops.acquire(ctx); err != nil {
		return nil, err
	}
	defer o.ops.release()
	return o.checkpointLocked(ctx)
}

// Close drains, flushes, releases the lease, then releases local resources.
//
// Local cleanup happens whether or not the remote steps worked, and a remote
// failure is still returned. A close that swallowed a failed flush would be
// reporting a durability barrier it did not reach.
//
// Close is idempotent: a second call returns the first one's error.
func (o *Object) Close(ctx context.Context) error {
	o.closeOnce.Do(func() { o.closeErr = o.closeInner(ctx) })
	return o.closeErr
}

// ------------------------------------------------------------ open sequence

// openObject runs the writer and read-only open sequences of contract §5.2.
func openObject(ctx context.Context, id string, backend Backend, factory EngineFactory, options OpenOptions) (*Object, bool, error) {
	tuning := options.Tuning.withDefaults()
	if err := tuning.validate(); err != nil {
		return nil, false, err
	}

	owner := options.Owner
	if owner == "" {
		owner = fmt.Sprintf("chdb-go-%d", os.Getpid())
	}
	instance := newInstanceID()

	engine, err := factory(ctx)
	if err != nil {
		return nil, false, err
	}

	var (
		created     scratch
		haveScratch bool
		leaseTaken  *headSnapshot
	)
	// Unwind in the reverse order of acquisition, and never let a cleanup
	// failure mask the error that caused it.
	fail := func(cause error) (*Object, bool, error) {
		_ = engine.Close(ctx)
		if leaseTaken != nil {
			_ = releaseLease(ctx, backend, instance)
		}
		if haveScratch {
			_ = os.RemoveAll(created.root)
		}
		return nil, false, cause
	}

	// Compatibility is settled before anything is created or claimed: an
	// object this engine cannot read should cost nothing but two strings.
	version, err := engine.Version(ctx)
	if err != nil {
		return fail(err)
	}
	backupFormat, err := engine.BackupFormat(ctx)
	if err != nil {
		return fail(err)
	}
	running := runningEngine{version: version, backupFormat: backupFormat}

	existing, found, err := readHead(ctx, backend)
	if err != nil {
		return fail(err)
	}
	if found {
		if err := assertReadable(existing.head); err != nil {
			return fail(err)
		}
		if err := assertEngineCompatible(existing.head, running); err != nil {
			return fail(err)
		}
		if !options.ReadOnly {
			if err := assertWritable(existing.head); err != nil {
				return fail(err)
			}
		}
	} else if options.ReadOnly || options.ExistingOnly {
		return fail(newError(CategoryNotFound, "durable: object %s does not exist at %s",
			id, backend.Describe()))
	}

	if !options.ReadOnly {
		// Checked before anything is published: an empty name would produce a
		// head that fails to parse, created with a conditional create that V1
		// gives no way to undo. A bad argument should be a bad argument, not a
		// permanently corrupt object.
		database := options.Database
		if database == "" {
			database = "default"
		}
		if found {
			leaseTaken, err = acquireLease(ctx, backend, existing, leaseParams{
				instance: instance, owner: owner, tuning: tuning, force: options.Force,
			})
		} else {
			leaseTaken, err = createCold(ctx, backend, coldParams{
				id: id, database: database, running: running,
				instance: instance, owner: owner, tuning: tuning,
			})
		}
		if err != nil {
			return fail(err)
		}
	}

	snapshot := existing
	if leaseTaken != nil {
		snapshot = *leaseTaken
	}

	created, err = makeScratch(options.ScratchRoot)
	if err != nil {
		return fail(wrapError(CategoryBackend, err, "durable: cannot create a scratch directory"))
	}
	haveScratch = true

	if err := engine.Start(ctx, EngineStartOptions{
		DataPath:           created.data,
		BackupsAllowedPath: created.backups,
	}); err != nil {
		return fail(err)
	}

	object := &Object{
		id:       id,
		readOnly: options.ReadOnly,
		backend:  backend,
		engine:   engine,
		scratch:  created,
		tuning:   tuning,
		instance: instance,
		owner:    owner,
		running:  running,
		progress: options.OnRestoreProgress,
		ops:      newGate(),
		headGate: newGate(),
		head:     snapshot.head,
		etag:     snapshot.etag,
		raw:      snapshot.raw,
		state:    "open",
	}

	if err := object.restore(ctx); err != nil {
		return fail(err)
	}

	if options.ReadOnly {
		// No lease, no heartbeat: the manifest read above is the snapshot this
		// handle serves for its whole life. Immutable references make that
		// safe even while a writer keeps committing.
		return object, found, nil
	}

	object.st.Lock()
	object.leaseDeadline = leaseDeadlineFrom(snapshot.head.Lease, tuning.LeaseTTL)
	object.st.Unlock()

	// Restore can outlast a lease. Confirming ownership before the handle
	// escapes is what stops a writer from starting work on a database someone
	// else has already taken over (contract §5.2 step 7).
	if err := object.renewLease(ctx); err != nil {
		return fail(err)
	}
	object.startHeartbeat()
	leaseTaken = nil
	return object, found, nil
}

// leaseDeadlineFrom is when this writer stops believing a lease it just wrote.
//
// It is the earlier of what is stored and what the local clock allows. Trusting
// only the stored value would extend the deadline past the TTL if a remote
// clock runs fast; trusting only the local clock would extend it past what
// another writer will honour.
func leaseDeadlineFrom(lease Lease, ttl time.Duration) time.Time {
	local := time.Now().Add(ttl)
	if lease.ExpiresAt == nil {
		return time.Time{}
	}
	stored := time.UnixMicro(int64(*lease.ExpiresAt * 1e6))
	if stored.Before(local) {
		return stored
	}
	return local
}

func makeScratch(root string) (scratch, error) {
	if root == "" {
		root = os.TempDir()
	}
	base, err := os.MkdirTemp(root, "chdb-durable-")
	if err != nil {
		return scratch{}, err
	}
	out := scratch{
		root:    base,
		data:    filepath.Join(base, "data"),
		backups: filepath.Join(base, "backups"),
		staging: filepath.Join(base, "staging"),
	}
	// The engine validates that a backup target's parent directory exists, and
	// its allowed-path guard resolves a relative value somewhere nobody wants,
	// so all three are created up front and always absolute.
	for _, dir := range []string{out.data, out.backups, out.staging} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			os.RemoveAll(base)
			return scratch{}, err
		}
	}
	return out, nil
}

func readHead(ctx context.Context, backend Backend) (headSnapshot, bool, error) {
	data, etag, found, err := backend.GetBytesWithETag(ctx, HeadKey)
	if err != nil {
		return headSnapshot{}, false, err
	}
	if !found {
		return headSnapshot{}, false, nil
	}
	head, raw, err := parseHead(data)
	if err != nil {
		return headSnapshot{}, false, err
	}
	return headSnapshot{head: head, etag: etag, raw: raw}, true, nil
}

// ------------------------------------------------------------ internal work

func (o *Object) report(progress RestoreProgress) {
	if o.progress != nil {
		o.progress(progress)
	}
}

// restore brings the manifest's state into the scratch engine: base, then WAL
// in order.
//
// Both are verified against length and SHA-256 before use, and a missing or
// mismatched object stops the open rather than yielding a partially recovered
// database (contract §4.5).
func (o *Object) restore(ctx context.Context) error {
	head := o.head
	db := head.Manifest.DB

	if head.Manifest.Base == nil {
		o.report(RestoreProgress{Phase: PhaseCreatingDatabase, Database: db})
		if err := o.engine.CreateDatabase(ctx, db); err != nil {
			return err
		}
	} else {
		base := *head.Manifest.Base
		o.report(RestoreProgress{Phase: PhaseRestoringBase, Key: base.Key, Size: base.Size})
		reader, found, err := o.backend.OpenReader(ctx, base.Key)
		if err != nil {
			return err
		}
		if !found {
			return newError(CategoryCorrupt,
				"durable: manifest names base %s, which is not present at %s",
				base.Key, o.backend.Describe())
		}
		staged := filepath.Join(o.scratch.staging, "base-"+uuid8()+".part")
		archive := filepath.Join(o.scratch.backups, "base-"+uuid8()+".tar.gz")
		err = streamToVerifiedFile(reader, base, staged, archive, "base checkpoint")
		reader.Close()
		if err != nil {
			return err
		}
		// A restore is where an incompatible archive actually surfaces. The
		// gate already established that this engine is allowed to read the
		// object, so a RESTORE that fails anyway means the compatibility
		// promise was violated rather than that the engine hit an ordinary
		// error. The distinction is the caller's next move: upgrade the
		// engine, or report a core defect. Reporting it as a plain engine
		// error hides both.
		if err := o.engine.RestoreDatabase(ctx, db, archive); err != nil {
			return &Error{
				Category: CategoryEngineIncompatible,
				Message: fmt.Sprintf("durable: restoring base %s into %q failed on chdb %s. The "+
					"archive was produced by %s and the object declares min_reader %s at archive "+
					"format %d, so this restore was expected to be supported",
					base.Key, db, o.running.version, head.Engine.Version,
					head.Engine.MinReader, head.Engine.BackupFormat),
				Err:      err,
				Expected: head.Engine.Version,
				Actual:   o.running.version,
			}
		}
		os.Remove(archive)
	}

	if err := o.engine.UseDatabase(ctx, db); err != nil {
		return err
	}

	var replayed int64
	for i, ref := range head.Manifest.WAL {
		o.report(RestoreProgress{
			Phase: PhaseReplayingWAL, Key: ref.Key, Size: ref.Size,
			Segment: i + 1, Segments: len(head.Manifest.WAL), Statements: replayed,
		})
		data, found, err := o.backend.GetBytes(ctx, ref.Key)
		if err != nil {
			return err
		}
		if !found {
			return newError(CategoryCorrupt,
				"durable: manifest names WAL segment %s, which is not present at %s",
				ref.Key, o.backend.Describe())
		}
		if err := assertDigest(ref, digestOf(data), "WAL segment"); err != nil {
			return err
		}
		statements, err := decodeWALSegment(data, ref.Key)
		if err != nil {
			return err
		}
		for _, sql := range statements {
			// Replay goes straight to the engine. Routing it back through the
			// public Execute would re-analyse statements core already accepted
			// and, worse, append every one of them to the WAL a second time.
			if err := o.engine.Run(ctx, sql); err != nil {
				return err
			}
			replayed++
		}
	}
	o.report(RestoreProgress{Phase: PhaseReady, Statements: replayed})
	return nil
}

// assertUsable refuses an operation on a closed or fenced object.
func (o *Object) assertUsable() error {
	o.st.Lock()
	defer o.st.Unlock()
	if o.state == "closed" {
		return newError(CategoryClosed, "durable: object %s is closed", o.id)
	}
	if o.fenced {
		return newError(CategoryLeaseFenced,
			"durable: object %s lost its lease (generation %d); this handle cannot be used again",
			o.id, o.head.Lease.Generation)
	}
	return nil
}

// assertWriter additionally refuses a read-only handle and self-fences a
// writer whose lease has lapsed.
func (o *Object) assertWriter() error {
	if err := o.assertUsable(); err != nil {
		return err
	}
	if o.readOnly {
		return newError(CategoryClassificationRefused,
			"durable: object %s is open read-only; only READ_ONLY statements are accepted", o.id)
	}
	o.st.Lock()
	lapsed := !time.Now().Before(o.leaseDeadline)
	o.st.Unlock()
	if lapsed {
		// Self-fence. The lease may in fact still be ours, but we cannot show
		// that it is, and "probably still the writer" is not a state to write
		// from.
		o.fence()
		return newError(CategoryLeaseFenced,
			"durable: object %s could not confirm its lease before it lapsed; the writer has "+
				"fenced itself", o.id)
	}
	return nil
}

// fence marks this writer as no longer the writer and stops renewing.
//
// It signals the heartbeat rather than waiting for it, because a failed
// renewal is one of the ways an object gets fenced — and that runs *on* the
// heartbeat goroutine, which cannot wait for itself to finish.
func (o *Object) fence() {
	o.st.Lock()
	o.fenced = true
	o.st.Unlock()
	o.signalHeartbeatStop()
}

func (o *Object) flushLocked(ctx context.Context) (*ObjectRef, error) {
	if err := o.assertWriter(); err != nil {
		return nil, err
	}

	o.st.Lock()
	statements := append([]string(nil), o.walBuffer...)
	generation := o.head.Lease.Generation
	nextSeq := o.head.Manifest.Seq + 1
	o.st.Unlock()

	if len(statements) == 0 {
		return nil, nil
	}

	data, err := encodeWALSegment(statements)
	if err != nil {
		return nil, err
	}
	digest := digestOf(data)
	ref := ObjectRef{Key: walKey(generation, nextSeq), Size: digest.Size, SHA256: digest.SHA256}

	if err := o.publishBytes(ctx, ref, data); err != nil {
		return nil, err
	}
	// The upload is network I/O and can outlast the lease. Checking only at
	// entry would let a writer that was required to self-fence mid-upload go on
	// to commit the manifest anyway.
	if err := o.assertWriter(); err != nil {
		return nil, err
	}
	err = o.commitHead(ctx, commitParams{
		key: ref.Key,
		build: func(current Head) Head {
			next := current.clone()
			next.Manifest.WAL = append(next.Manifest.WAL, ref)
			next.Manifest.Seq = current.Manifest.Seq + 1
			return next
		},
		committed: func(observed Head) bool {
			for _, w := range observed.Manifest.WAL {
				if w.Key == ref.Key {
					return true
				}
			}
			return false
		},
	})
	if err != nil {
		return nil, err
	}

	o.st.Lock()
	o.walBuffer = o.walBuffer[len(statements):]
	o.walBufferBytes = 0
	for _, sql := range o.walBuffer {
		n, _ := walLineBytes(sql)
		o.walBufferBytes += n
	}
	o.committedStatements += int64(len(statements))
	o.lastFlushAt = time.Now()
	o.st.Unlock()
	return &ref, nil
}

func (o *Object) checkpointLocked(ctx context.Context) (*ObjectRef, error) {
	if err := o.assertWriter(); err != nil {
		return nil, err
	}

	o.st.Lock()
	database := o.head.Manifest.DB
	generation := o.head.Lease.Generation
	nextSeq := o.head.Manifest.Seq + 1
	o.st.Unlock()

	archive := filepath.Join(o.scratch.backups, "checkpoint-"+uuid8()+".tar.gz")
	if err := o.engine.BackupDatabase(ctx, database, archive); err != nil {
		os.Remove(archive)
		return nil, err
	}
	defer os.Remove(archive)

	digest, err := digestFile(archive)
	if err != nil {
		return nil, wrapError(CategoryEngine, err, "durable: cannot read the archive just written")
	}
	ref := ObjectRef{Key: checkpointKey(generation, nextSeq), Size: digest.Size, SHA256: digest.SHA256}

	if err := o.publishFile(ctx, ref, archive, digest); err != nil {
		return nil, err
	}
	// A full backup plus its upload is the longest thing this object does,
	// easily longer than a lease TTL. Ownership was checked before it started;
	// it has to hold now, when the commit actually happens.
	if err := o.assertWriter(); err != nil {
		return nil, err
	}

	o.st.Lock()
	covered := len(o.walBuffer)
	o.st.Unlock()

	err = o.commitHead(ctx, commitParams{
		key: ref.Key,
		build: func(current Head) Head {
			next := current.clone()
			base := ref
			next.Manifest.Base = &base
			next.Manifest.WAL = []ObjectRef{}
			next.Manifest.Seq = current.Manifest.Seq + 1
			return next
		},
		committed: func(observed Head) bool {
			return observed.Manifest.Base != nil && observed.Manifest.Base.Key == ref.Key
		},
	})
	if err != nil {
		return nil, err
	}

	// Only now. Until the head names this base, the old base plus the old WAL
	// is still the authoritative state, and these statements are only
	// recoverable from the buffer.
	o.st.Lock()
	o.walBuffer = o.walBuffer[covered:]
	o.walBufferBytes = 0
	for _, sql := range o.walBuffer {
		n, _ := walLineBytes(sql)
		o.walBufferBytes += n
	}
	o.committedStatements += int64(covered)
	o.lastCheckpointAt = time.Now()
	o.st.Unlock()
	return &ref, nil
}

func (o *Object) closeInner(ctx context.Context) error {
	o.st.Lock()
	o.state = "closing"
	o.st.Unlock()
	o.stopHeartbeat()

	var failure error
	if err := o.ops.seal(ctx, func() error {
		return newError(CategoryClosed, "durable: object %s is closing", o.id)
	}); err != nil {
		failure = err
	}

	o.st.Lock()
	readOnly, fenced, pending := o.readOnly, o.fenced, len(o.walBuffer)
	o.st.Unlock()

	if failure == nil && !readOnly && !fenced {
		if err := o.ops.acquireSealed(ctx); err != nil {
			failure = err
		} else {
			if pending > 0 {
				if _, err := o.flushLocked(ctx); err != nil {
					failure = err
				}
			}
			if failure == nil {
				failure = o.releaseLeaseLocked(ctx)
			}
			o.ops.release()
		}
	}

	// Native connection and scratch go back whatever happened above: a remote
	// failure is a durability problem, not a reason to leak a connection or a
	// temp tree.
	if err := o.engine.Close(ctx); err != nil && failure == nil {
		failure = err
	}
	if err := os.RemoveAll(o.scratch.root); err != nil && failure == nil {
		failure = wrapError(CategoryBackend, err, "durable: cannot remove the scratch directory")
	}

	o.st.Lock()
	o.state = "closed"
	o.st.Unlock()
	return failure
}

func (o *Object) releaseLeaseLocked(ctx context.Context) error {
	err := o.commitHead(ctx, commitParams{
		key: HeadKey,
		build: func(current Head) Head {
			next := current.clone()
			next.Lease = Lease{Generation: current.Lease.Generation}
			return next
		},
		committed: func(observed Head) bool { return observed.Lease.Instance == nil },
		// Releasing is the last thing this instance does; after it succeeds
		// the ownership check would fail by construction.
		skipOwnershipAfter: true,
	})
	if err != nil {
		return err
	}
	o.st.Lock()
	o.fenced = false
	o.leaseDeadline = time.Time{}
	o.st.Unlock()
	return nil
}

// ------------------------------------------------------------------- lease

func (o *Object) startHeartbeat() {
	if o.readOnly {
		return
	}
	o.st.Lock()
	o.heartbeatStop = make(chan struct{})
	o.heartbeatDone = make(chan struct{})
	stop, done := o.heartbeatStop, o.heartbeatDone
	o.st.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(o.tuning.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				// A renewal that fails is not fatal on its own — the next
				// attempt may succeed. What is fatal is reaching the locally
				// believed expiry without a confirmation, and assertWriter
				// checks exactly that.
				ctx, cancel := context.WithTimeout(context.Background(), o.tuning.CommitDeadline)
				_ = o.renewLease(ctx)
				cancel()
			}
		}
	}()
}

// signalHeartbeatStop tells the heartbeat goroutine to finish, without
// waiting for it.
func (o *Object) signalHeartbeatStop() {
	o.st.Lock()
	stop := o.heartbeatStop
	o.st.Unlock()
	if stop == nil {
		return
	}
	o.heartbeatStopOnce.Do(func() { close(stop) })
}

// stopHeartbeat signals the heartbeat goroutine and waits for it to return, so
// that no renewal is in flight once close starts its own head writes.
//
// Only close calls it. Calling it from the heartbeat goroutine would be a wait
// for itself.
func (o *Object) stopHeartbeat() {
	o.signalHeartbeatStop()
	o.st.Lock()
	done := o.heartbeatDone
	o.st.Unlock()
	if done != nil {
		<-done
	}
}

// renewLease extends the expiry, leaving generation and seq untouched.
func (o *Object) renewLease(ctx context.Context) error {
	o.st.Lock()
	skip := o.readOnly || o.fenced || o.state == "closed"
	o.st.Unlock()
	if skip {
		return nil
	}

	var written float64
	err := o.commitHead(ctx, commitParams{
		key: HeadKey,
		build: func(current Head) Head {
			// The expiry has to be computed inside the commit, not before
			// waiting for the head gate. Computed early and then delayed
			// behind a long checkpoint commit, the value written could already
			// be in the past — while the local deadline below was refreshed
			// from the current clock. The object would then keep writing under
			// a lease other writers are entitled to take.
			written = float64(time.Now().Add(o.tuning.LeaseTTL).UnixMicro()) / 1e6
			next := current.clone()
			next.Lease.Owner = stringPtr(o.owner)
			next.Lease.Instance = stringPtr(o.instance)
			next.Lease.ExpiresAt = floatPtr(written)
			return next
		},
		committed: func(observed Head) bool {
			return observed.Lease.instanceIs(o.instance) &&
				observed.Lease.ExpiresAt != nil && *observed.Lease.ExpiresAt >= written
		},
	})
	if err != nil {
		return err
	}

	// Believe what is actually stored, not what a fresh clock would allow.
	// Reconciliation can settle on a head written by an earlier attempt whose
	// expiry is older than this one's.
	o.st.Lock()
	o.leaseDeadline = leaseDeadlineFrom(o.head.Lease, o.tuning.LeaseTTL)
	o.st.Unlock()
	return nil
}

// -------------------------------------------------------- immutable publish

func (o *Object) publishBytes(ctx context.Context, ref ObjectRef, data []byte) error {
	outcome, err := o.backend.PutBytesIfAbsent(ctx, ref.Key, data)
	if err != nil {
		return err
	}
	if outcome == PutCreated {
		return nil
	}
	return o.reconcileUpload(ctx, ref, outcome)
}

func (o *Object) publishFile(ctx context.Context, ref ObjectRef, localPath string, digest Digest) error {
	outcome, err := o.backend.PutFileIfAbsent(ctx, ref.Key, localPath, digest)
	if err != nil {
		return err
	}
	if outcome == PutCreated {
		return nil
	}
	return o.reconcileUpload(ctx, ref, outcome)
}

// reconcileUpload settles an upload that did not cleanly create
// (contract §5.8).
//
// The key is unique to this attempt, so "already exists" can only mean an
// earlier try by this same writer landed. Re-reading and comparing the digest
// is what turns a lost response into a fact: matching bytes are the bytes we
// meant to publish, different bytes are corruption, and an absent object means
// the write genuinely did not happen.
func (o *Object) reconcileUpload(ctx context.Context, ref ObjectRef, outcome PutOutcome) error {
	reader, found, err := o.backend.OpenReader(ctx, ref.Key)
	if err != nil {
		return err
	}
	if !found {
		if outcome == PutAlreadyExists {
			return newError(CategoryCorrupt,
				"durable: %s was reported as existing but cannot be read from %s",
				ref.Key, o.backend.Describe())
		}
		return &Error{
			Category: CategoryCommitAmbiguous,
			Message: fmt.Sprintf("durable: could not determine whether %s was uploaded to %s",
				ref.Key, o.backend.Describe()),
			Key: ref.Key,
		}
	}
	defer reader.Close()
	observed, err := drainDigest(reader)
	if err != nil {
		return wrapError(CategoryBackend, err, "durable: cannot re-read %s", ref.Key)
	}
	return assertDigest(ref, observed, "published object")
}

// --------------------------------------------------------------- head commit

type commitParams struct {
	// key names what is being committed, for the message an unresolved commit
	// reports.
	key string
	// build produces the next head from whatever the current one turns out to
	// be.
	build func(current Head) Head
	// committed recognises this writer's own intent in a head it did not
	// write. This is what makes a lost response recoverable.
	committed func(observed Head) bool
	// skipOwnershipAfter allows a commit to be recognised as landed even
	// though this instance no longer owns the lease. Only lease release sets
	// it, because success there *is* the loss of ownership.
	skipOwnershipAfter bool
}

// commitHead is the single path through which this object writes head.json.
//
// Every caller supplies two things: how to build the next head, and how to
// recognise its own intent in a head it did not write. The second is what
// makes a lost response recoverable — after re-reading, either the intent is
// visible and this committed, or ownership is gone and this is fenced, or
// neither is true and it can try again inside the deadline.
func (o *Object) commitHead(ctx context.Context, params commitParams) error {
	if err := o.headGate.acquire(ctx); err != nil {
		return err
	}
	defer o.headGate.release()

	deadline := time.Now().Add(o.tuning.CommitDeadline)
	attempts := 0
	sawAmbiguous := false

	for {
		attempts++

		o.st.Lock()
		current := o.head.clone()
		etag := o.etag
		raw := o.raw
		generation := o.head.Lease.Generation
		o.st.Unlock()

		candidate, err := raiseCompatibilityFloor(params.build(current), o.running)
		if err != nil {
			return err
		}
		data, err := serializeHead(candidate, raw)
		if err != nil {
			return err
		}
		outcome, err := o.backend.ReplaceIfMatch(ctx, HeadKey, data, etag)
		if err != nil {
			return err
		}
		if outcome.Status == ReplaceDone {
			o.adopt(candidate, outcome.ETag, nil)
			return nil
		}
		if outcome.Status == ReplaceAmbiguous {
			sawAmbiguous = true
		}

		// Either someone else wrote (not-replaced) or the answer was lost
		// (ambiguous). Both are settled the same way: look at what is actually
		// there.
		fresh, found, err := readHead(ctx, o.backend)
		if err != nil {
			return err
		}
		if !found {
			return newError(CategoryCorrupt,
				"durable: %s disappeared from %s while committing", HeadKey, o.backend.Describe())
		}

		stillOurs := fresh.head.Lease.instanceIs(o.instance) &&
			fresh.head.Lease.Generation == generation

		if params.committed(fresh.head) && (stillOurs || params.skipOwnershipAfter) {
			o.adopt(fresh.head, fresh.etag, fresh.raw)
			return nil
		}

		if !stillOurs {
			o.fence()
			return newError(CategoryLeaseFenced,
				"durable: object %s was taken over (generation %d); this writer can no longer commit",
				o.id, fresh.head.Lease.Generation)
		}

		// Ownership intact and the intent is not there: our ETag was stale.
		// Adopt the current one and retry within the deadline.
		o.adopt(fresh.head, fresh.etag, fresh.raw)

		if attempts >= o.tuning.MaxCommitAttempts || !time.Now().Before(deadline) {
			// Two different answers, and the caller acts on them differently.
			// If every attempt came back as a definite refusal, the commit
			// provably did not happen and retrying later is safe. If any
			// attempt was ambiguous, it may have landed, and a blind retry
			// could publish twice.
			if sawAmbiguous {
				return &Error{
					Category: CategoryCommitAmbiguous,
					Message: fmt.Sprintf("durable: gave up committing %s after %d attempts without "+
						"proving the outcome", params.key, attempts),
					Key: params.key,
				}
			}
			return newError(CategoryTimeout,
				"durable: could not commit %s within %s (%d attempts, each definitively refused); "+
					"nothing was committed", params.key, o.tuning.CommitDeadline, attempts)
		}
	}
}

// adopt records a head as the committed one.
//
// When raw is nil the candidate was this build's own construction, so the raw
// document is re-derived from it — the same merge that produced the bytes just
// written, which keeps the unknown fields that went out with them.
//
// Neither step can fail here: these are the inputs serializeHead accepted a
// moment ago, and its output is what parseHead validates. If one somehow did,
// keeping the previous raw is harmless rather than lossy — every known field is
// patched from o.head on the next write, and the unknown fields in the older
// document are the same ones.
func (o *Object) adopt(head Head, etag string, raw map[string]any) {
	o.st.Lock()
	defer o.st.Unlock()
	o.head = head
	o.etag = etag
	if raw != nil {
		o.raw = raw
		return
	}
	if data, err := serializeHead(head, o.raw); err == nil {
		if _, parsed, err := parseHead(data); err == nil {
			o.raw = parsed
		}
	}
}
