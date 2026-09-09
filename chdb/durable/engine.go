package durable

import (
	"context"

	chdbpurego "github.com/chdb-io/chdb-go/v2/chdb-purego"
)

// The engine seam (contract §3).
//
// The durable control plane never touches a native library directly.
// Everything it needs from the engine goes through this interface, which is
// why the same state machine can drive a real chDB session or a fake in a unit
// test without either knowing about the other.
//
// Two rules shape the interface, and both are contract requirements rather
// than taste:
//
//  1. **The binding never builds management SQL.** BackupDatabase and
//     RestoreDatabase take an identifier and a path; quoting and AST
//     construction happen in core. A binding that concatenated
//     "BACKUP DATABASE " + name would be one unusual database name away from
//     injection, in four languages independently.
//  2. **The binding never classifies SQL itself.** No prefix lists, no
//     regular expressions. Analyze is ClickHouse's own parser answering
//     questions only it can answer — how many executable statements are in
//     this text, and does every persistent write land in the database we own.
//     A regex cannot see through INSERT ... FORMAT inline data, and it cannot
//     resolve an unqualified table name against the session's current
//     database.
//
// BackupDatabase deliberately has no incremental-base parameter even though
// the C ABI accepts one. V1 checkpoints are always full: an incremental
// archive records the *path* of its base, and that path does not exist on the
// machine that restores it (contract §3.2).

// EngineStartOptions is where an engine puts its state for one durable object.
type EngineStartOptions struct {
	// DataPath is a fresh, empty, private data directory for this object.
	DataPath string
	// BackupsAllowedPath is an absolute, already-created directory the engine
	// may read and write archives in.
	BackupsAllowedPath string
}

// Engine is what a durable object needs from chDB. Implementations own their
// native resources entirely; the control plane only calls these methods and
// Close.
type Engine interface {
	// Version is the exact chdb_version() of the loaded engine.
	//
	// It is recorded in the head as the producer version, and it is not an
	// exact-match gate: compatibility is checked with backup_format and
	// min_reader, so later chdb-core releases can restore earlier V1 full
	// backups. It must be answerable before Start — an incompatible engine is
	// refused before the object takes a lease or creates a scratch directory.
	Version(ctx context.Context) (string, error)

	// BackupFormat is the highest archive-format generation this engine can
	// restore. Every release so far is the V1 baseline, because the C ABI has
	// no accessor for it; once core exposes one, this starts refusing archives
	// from a future generation instead of assuming.
	BackupFormat(ctx context.Context) (int, error)

	// Start brings up a connection on a fresh scratch path. Called once,
	// before anything else.
	Start(ctx context.Context, options EngineStartOptions) error

	// CreateDatabase creates the object's database, with core doing the
	// quoting.
	CreateDatabase(ctx context.Context, database string) error

	// UseDatabase pins the connection's current database. Called once after
	// restore; the public surface can never change it, because USE classifies
	// as CONTROL.
	UseDatabase(ctx context.Context, database string) error

	// Analyze says what a statement would do, judged against targetDatabase,
	// without executing it.
	Analyze(ctx context.Context, sql, targetDatabase string) (chdbpurego.QueryAnalysis, error)

	// Query runs a read query and returns its formatted result.
	Query(ctx context.Context, sql, format string) (string, error)

	// Run runs a statement for effect.
	//
	// This is the *internal* path: it performs no analysis and appends nothing
	// to a WAL. Replay uses it, which is exactly why it must not be reachable
	// from the public surface — a replayed statement that re-entered Execute
	// would be logged a second time.
	Run(ctx context.Context, sql string) error

	// BackupDatabase writes a full archive to a new absolute path that must
	// not already exist.
	BackupDatabase(ctx context.Context, database, filePath string) error

	// RestoreDatabase restores an archive into a database that does not
	// already hold its tables.
	RestoreDatabase(ctx context.Context, database, filePath string) error

	// Close releases the native connection. It must be safe to call after a
	// failed Start.
	Close(ctx context.Context) error
}

// EngineFactory builds the engine for one durable object.
type EngineFactory func(ctx context.Context) (Engine, error)

// assertQueryAllowed applies the frozen Query gate (contract §3.4).
//
// Note what is *not* here: a secret check. A read-only statement never reaches
// the WAL, so a credential inside it is not a durability problem — only a
// logging one, which this package handles by never echoing SQL.
func assertQueryAllowed(analysis chdbpurego.QueryAnalysis) error {
	if analysis.StatementCount != 1 {
		return newError(CategoryClassificationRefused,
			"durable: Query takes exactly one statement, core counted %d", analysis.StatementCount)
	}
	if analysis.Class != chdbpurego.QueryReadOnly {
		return newError(CategoryClassificationRefused,
			"durable: Query accepts only READ_ONLY statements, core classified this as %s",
			analysis.Class)
	}
	return nil
}

// assertExecuteAllowed applies the frozen Execute gate (contract §3.4).
//
// The checks are ordered so the message names the most actionable fact first.
// The secret check is last and has its own category because it is the one
// failure a caller fixes by rewriting the statement rather than by using a
// different method — and because its message must describe the refusal without
// quoting the statement that triggered it.
func assertExecuteAllowed(analysis chdbpurego.QueryAnalysis, database string) error {
	if analysis.StatementCount != 1 {
		return newError(CategoryClassificationRefused,
			"durable: Execute takes exactly one statement, core counted %d. A WAL record is one "+
				"statement, so a batch has no replayable form in V1", analysis.StatementCount)
	}
	if analysis.Class != chdbpurego.QueryMutating {
		message := "durable: Execute accepts only MUTATING statements, core classified this as " +
			analysis.Class.String()
		if analysis.Class == chdbpurego.QueryMutatingGlobal {
			message += ". Global state lives outside every database, so a checkpoint cannot carry " +
				"it and V1 refuses it"
		}
		return newError(CategoryClassificationRefused, "%s", message)
	}
	if analysis.ChangesDatabaseLifecycle {
		return newError(CategoryClassificationRefused,
			"durable: Execute cannot create, drop or rename a database; the object owns %q and its "+
				"lifecycle is not a logged mutation", database)
	}
	if !analysis.WritesOnlyTargetDatabase {
		return newError(CategoryClassificationRefused,
			"durable: core could not prove every write lands in %q. A write to another database, "+
				"to system, to a table function or to a file is not captured by this object's "+
				"checkpoint", database)
	}
	if analysis.HasSecrets {
		return newError(CategorySecretRefused,
			"durable: refusing to log a mutation that embeds a credential; the WAL outlives the "+
				"statement, so the credential would outlive it too")
	}
	return nil
}
