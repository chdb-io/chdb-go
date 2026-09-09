package durable

import (
	"context"
	"strings"
	"sync"

	"github.com/chdb-io/chdb-go/v2/chdb"
	chdbpurego "github.com/chdb-io/chdb-go/v2/chdb-purego"
)

// The real engine: a chdb.Session on the object's scratch directory.
//
// Going through chdb.Session rather than opening a native connection directly
// is deliberate. The session registry already models the constraint the
// contract states in §3.6 — chDB binds one data path per process — so a second
// durable object in the same process is refused by the registry with an error
// naming both paths, instead of failing somewhere deeper and less legibly.
// Nothing here maintains a second path state machine.

// ChdbEngine drives one durable object through a chdb.Session.
type ChdbEngine struct {
	// extraArgs are appended to the connection arguments, for a caller that
	// needs to tune the engine for its workload. They cannot be used to
	// weaken the synchronous-write settings below: those are applied after,
	// and the public surface cannot change them either, since SET classifies
	// as CONTROL.
	extraArgs []string

	mu      sync.Mutex
	session *chdb.Session
}

// NewChdbEngine returns the engine factory a namespace uses by default.
//
// extraArgs are extra connection-string parameters in "key=value" form, as
// chdb.NewSession takes them.
func NewChdbEngine(extraArgs ...string) EngineFactory {
	return func(context.Context) (Engine, error) {
		return &ChdbEngine{extraArgs: extraArgs}, nil
	}
}

// Version implements Engine.
//
// It also settles whether this engine can serve a durable object at all. An
// engine without the management ABI cannot back up, restore or classify, so it
// is refused here — before the lease, before the scratch directory, before
// anything is claimed. Version is the first thing an open asks for, which is
// what makes it the right place: the contract requires an engine that cannot
// read an object to cost nothing but two strings (§5.2).
func (e *ChdbEngine) Version(context.Context) (string, error) {
	version, err := chdb.EngineVersion()
	if err != nil {
		return "", wrapError(CategoryEngine, err, "durable: cannot read the engine version")
	}
	available, err := chdbpurego.AdminABIAvailable()
	if err != nil {
		return "", wrapError(CategoryEngine, err, "durable: cannot load the engine")
	}
	if !available {
		return "", wrapError(CategoryEngineIncompatible, chdbpurego.ErrAdminABIUnavailable,
			"durable: chdb %s cannot serve a durable object", version)
	}
	return version, nil
}

// BackupFormat implements Engine.
//
// The C ABI exposes no accessor for the archive-format generation, so every
// release reports the V1 baseline. When core adds one, this is the single
// place that changes, and the reader gate starts refusing a future generation
// instead of assuming it can restore it.
func (e *ChdbEngine) BackupFormat(context.Context) (int, error) {
	return BackupFormatBaseline, nil
}

// Start implements Engine.
func (e *ChdbEngine) Start(_ context.Context, options EngineStartOptions) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session != nil {
		return newError(CategoryEngine, "durable: the engine is already started")
	}

	// The settings here are the ones a durable writer cannot leave to chance.
	// Asynchronous inserts and non-synchronous mutations both mean "the
	// statement returned before its effect landed", which would put a
	// statement in the WAL whose local effect is not yet in the database the
	// next checkpoint archives.
	params := []string{
		"backups.allowed_path=" + options.BackupsAllowedPath,
		"async_insert=0",
		"wait_for_async_insert=1",
		"mutations_sync=2",
		"alter_sync=2",
	}
	params = append(params, e.extraArgs...)

	session, err := chdb.NewSession(options.DataPath + "?" + strings.Join(params, "&"))
	if err != nil {
		// chDB binds one data path per process, so a second durable object in
		// the same process arrives here. The session registry's message names
		// both paths, which is more use than anything this layer could add.
		return wrapError(CategoryEngine, err, "durable: cannot open an engine on %s", options.DataPath)
	}
	e.session = session
	return nil
}

// borrow returns the live session, or the reason there is not one.
func (e *ChdbEngine) borrow() (*chdb.Session, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session == nil {
		return nil, newError(CategoryEngine, "durable: the engine is not started")
	}
	return e.session, nil
}

// quoteIdentifier backtick-quotes a ClickHouse identifier, so a database name
// holding a dash — or anything else needing quoting — is valid in DDL and
// cannot break out of the statement.
//
// This is only for the two statements core has no argument-taking entry point
// for, CREATE DATABASE and USE. Backup and restore take the name as an
// argument and quote it themselves, which is why neither appears here.
func quoteIdentifier(name string) string {
	return "`" + strings.NewReplacer("\\", "\\\\", "`", "\\`").Replace(name) + "`"
}

// CreateDatabase implements Engine.
func (e *ChdbEngine) CreateDatabase(ctx context.Context, database string) error {
	return e.Run(ctx, "CREATE DATABASE IF NOT EXISTS "+quoteIdentifier(database))
}

// UseDatabase implements Engine.
func (e *ChdbEngine) UseDatabase(ctx context.Context, database string) error {
	return e.Run(ctx, "USE "+quoteIdentifier(database))
}

// Analyze implements Engine.
func (e *ChdbEngine) Analyze(_ context.Context, sql, targetDatabase string) (chdbpurego.QueryAnalysis, error) {
	session, err := e.borrow()
	if err != nil {
		return chdbpurego.QueryAnalysis{}, err
	}
	analysis, err := session.ClassifyQuery(sql, targetDatabase)
	if err != nil {
		// The message deliberately does not include the SQL: analysis runs on
		// statements that may embed a credential, and that is exactly the case
		// the caller is about to be told about.
		return chdbpurego.QueryAnalysis{}, wrapError(CategoryEngine, err,
			"durable: the engine could not analyse the statement")
	}
	return analysis, nil
}

// Query implements Engine.
func (e *ChdbEngine) Query(_ context.Context, sql, format string) (string, error) {
	session, err := e.borrow()
	if err != nil {
		return "", err
	}
	result, err := session.Query(sql, format)
	if err != nil {
		return "", wrapError(CategoryEngine, err, "durable: the engine refused a read")
	}
	if result == nil {
		return "", nil
	}
	defer result.Free()
	return result.String(), nil
}

// Run implements Engine.
func (e *ChdbEngine) Run(_ context.Context, sql string) error {
	session, err := e.borrow()
	if err != nil {
		return err
	}
	result, err := session.Query(sql, "CSV")
	if err != nil {
		return wrapError(CategoryEngine, err, "durable: the engine refused a statement")
	}
	if result != nil {
		result.Free()
	}
	return nil
}

// BackupDatabase implements Engine.
func (e *ChdbEngine) BackupDatabase(_ context.Context, database, filePath string) error {
	session, err := e.borrow()
	if err != nil {
		return err
	}
	if err := session.BackupDatabase(database, filePath); err != nil {
		return wrapError(CategoryEngine, err, "durable: backing up %q failed", database)
	}
	return nil
}

// RestoreDatabase implements Engine.
func (e *ChdbEngine) RestoreDatabase(_ context.Context, database, filePath string) error {
	session, err := e.borrow()
	if err != nil {
		return err
	}
	if err := session.RestoreDatabase(database, filePath); err != nil {
		return wrapError(CategoryEngine, err, "durable: restoring %q failed", database)
	}
	return nil
}

// Close implements Engine. It tolerates never having started, because an open
// that fails partway through still has to release whatever was acquired.
func (e *ChdbEngine) Close(context.Context) error {
	e.mu.Lock()
	session := e.session
	e.session = nil
	e.mu.Unlock()
	if session != nil {
		session.Close()
	}
	return nil
}
