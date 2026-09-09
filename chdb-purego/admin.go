package chdbpurego

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"
)

// The management corner of the C ABI: full database backup, restore, and
// statement analysis, plus the engine's own version string.
//
// These three calls exist because a caller that manages a database cannot get
// the answers anywhere else. Backup and restore take an identifier and a path
// as separate arguments so that chDB builds the `BACKUP` / `RESTORE` AST with
// its own quoting — a binding that concatenated the statement would be one
// unusual database name away from running something else. Analysis is
// ClickHouse's parser answering questions only a parser can answer: how many
// executable statements a piece of text holds, and whether every persistent
// write it performs lands in one named database. A prefix check or a regular
// expression cannot see through `INSERT ... FORMAT` inline data and cannot
// resolve an unqualified table name against the session's current database.
//
// They arrived in chdb-core v26.7.2-rc.2. On an older engine the symbols are
// absent, and every entry point here returns ErrAdminABIUnavailable rather
// than panicking inside purego at load time — the rest of the binding keeps
// working, which is the whole reason the symbols are probed instead of
// declared.

// ErrAdminABIUnavailable reports that the loaded libchdb predates the backup,
// restore and query-analysis symbols. Its message names the release that
// introduced them, because the fix is always to move the engine.
var ErrAdminABIUnavailable = errors.New(
	"chdb: the loaded libchdb does not export chdb_backup_database_n, " +
		"chdb_restore_database_n and chdb_classify_query_n; they were added in " +
		"chdb-core v26.7.2-rc.2, so install or point CHDB_LIB_PATH at that release or later")

// QueryClass says what a statement does to state that outlives it, as decided
// by the ClickHouse parser. Values match chdb_query_class in the C ABI and
// ascend by how restricted the statement is, so a batch classifies as the
// maximum over its members.
type QueryClass uint32

const (
	// QueryReadOnly covers SELECT, SHOW, DESCRIBE, EXPLAIN: leaves no trace.
	QueryReadOnly QueryClass = 0
	// QueryMutating covers INSERT, CREATE, ALTER, DROP and friends: changes a
	// database, and `BACKUP DATABASE` captures the change.
	QueryMutating QueryClass = 1
	// QueryMutatingGlobal covers global UDFs, named collections, access
	// entities and writes into `system`: persistent and replayable, but
	// outside every database a checkpoint could capture.
	QueryMutatingGlobal QueryClass = 2
	// QueryControl covers USE, SET, SYSTEM, BACKUP, RESTORE, and statements
	// writing outside the engine altogether.
	QueryControl QueryClass = 3
	// QueryUnknown means the text did not parse, or parsed into something this
	// engine does not classify. A caller gating writes on the class must treat
	// it as a refusal.
	QueryUnknown QueryClass = 4
)

func (c QueryClass) String() string {
	switch c {
	case QueryReadOnly:
		return "READ_ONLY"
	case QueryMutating:
		return "MUTATING"
	case QueryMutatingGlobal:
		return "MUTATING_GLOBAL"
	case QueryControl:
		return "CONTROL"
	case QueryUnknown:
		return "UNKNOWN"
	}
	return fmt.Sprintf("class %d", uint32(c))
}

// Analysis flag bits, matching chdb_query_analysis_flag.
const (
	analysisHasSecrets               uint32 = 1 << 0
	analysisWritesOnlyTargetDatabase uint32 = 1 << 1
	analysisChangesDatabaseLifecycle uint32 = 1 << 2
)

// QueryAnalysis is what ClassifyQuery reports: chdb_query_analysis_v1 with the
// flag word already unpacked into named booleans.
type QueryAnalysis struct {
	// StatementCount is how many executable statements the text holds. Zero
	// for empty input or text that did not parse; `a PARALLEL WITH b` counts
	// as two, because both arms execute.
	StatementCount uint32

	// Class is what the statement does to state that outlives it.
	Class QueryClass

	// HasSecrets reports that the text carries a credential: a password, a
	// named collection's key, an access key handed to a table function. Never
	// set when Class is QueryUnknown, since nothing was proven about text that
	// did not parse.
	HasSecrets bool

	// WritesOnlyTargetDatabase reports that every persistent write the
	// statement performs lands in the database named in the call. Set only
	// when the parser can prove it: a write to another database, to `system`,
	// to a table function or to a file clears it, and a statement that writes
	// nothing sets it vacuously. Never set when no target database was named.
	WritesOnlyTargetDatabase bool

	// ChangesDatabaseLifecycle reports that the statement creates, drops or
	// renames a database rather than acting inside one.
	ChangesDatabaseLifecycle bool
}

// queryAnalysisV1 mirrors chdb_query_analysis_v1. Its layout is the ABI, so
// the fields stay in this order and at this width; the size the engine is told
// about is sizeof this struct, which is what lets a newer engine fill only the
// fields this build has room for.
type queryAnalysisV1 struct {
	structSize     uint32
	statementCount uint32
	flags          uint32
	queryClass     uint32
}

// ChdbAdminConn is the management surface a connection offers when the engine
// exports it.
//
// It is deliberately a second interface rather than three more methods on
// ChdbConn: an interface in this package is something callers may implement
// (a fake in a test, a wrapper that adds tracing), and widening ChdbConn
// would break every one of them. Callers reach these methods with a type
// assertion, which is also how they find out an engine is too old.
type ChdbAdminConn interface {
	// BackupDatabase writes a full archive of database to filePath.
	//
	// filePath must be absolute, its directory must already exist, and it must
	// be inside the connection's `backups.allowed_path` — a connection that
	// never set that option cannot write a backup anywhere. An existing
	// destination is never overwritten; the call fails instead.
	//
	// The archive is always full. The C ABI also accepts a base archive to
	// make the backup incremental, and that is deliberately not offered here:
	// an incremental archive records the base's path as given, so it only
	// restores on a machine where that path still holds the base.
	BackupDatabase(database, filePath string) error

	// RestoreDatabase restores database from an archive written by
	// BackupDatabase. The path constraints match BackupDatabase's, and the
	// archive must exist.
	//
	// RESTORE appends to an existing table rather than replacing it, so
	// restore into a database that does not already hold the archive's tables.
	// The connection's current database is left alone.
	RestoreDatabase(database, filePath string) error

	// ClassifyQuery says what sql would do, without running it. Nothing is
	// executed and the session is untouched: no current database change, no
	// settings change, no query log entry.
	//
	// targetDatabase names the database the caller considers its own and is
	// what QueryAnalysis.WritesOnlyTargetDatabase is judged against; pass ""
	// to skip that judgement, in which case the flag is never set.
	//
	// SQL that does not parse is reported as QueryUnknown with a statement
	// count of zero and no error — what it is, is the answer.
	ClassifyQuery(sql, targetDatabase string) (QueryAnalysis, error)
}

// Version returns the exact chdb_version() of the loaded engine, loading the
// library if that has not happened yet.
//
// This is a property of the library rather than of a session, which is why it
// takes no connection: a caller can establish whether an engine is new enough
// before it opens anything.
func Version() (string, error) {
	if err := ensureLoaded(); err != nil {
		return "", err
	}
	if chdbVersion == nil {
		// chdb_version has been exported for far longer than the management
		// symbols, so reaching this means something stranger than an old
		// engine — say a stripped or partial build.
		return "", errors.New("chdb: the loaded libchdb does not export chdb_version")
	}
	return chdbVersion(), nil
}

// AdminABIAvailable reports whether the loaded engine exports the backup,
// restore and query-analysis symbols. It loads the library if needed.
func AdminABIAvailable() (bool, error) {
	if err := ensureLoaded(); err != nil {
		return false, err
	}
	return adminABIAvailable(), nil
}

func adminABIAvailable() bool {
	return chdbBackupDatabaseN != nil && chdbRestoreDatabaseN != nil && chdbClassifyQueryN != nil
}

// cString copies s into a NUL-terminated buffer and returns a pointer to it
// with the length of s in bytes.
//
// Both halves are handed to the engine: the `_n` entry points take an explicit
// length and read exactly that many bytes, so SQL holding a NUL byte survives.
// The terminator is there only so the buffer is also a valid C string for
// anything that treats it as one.
//
// An empty s yields a nil pointer and a zero length, which is how the ABI
// spells "not given" for an optional argument.
func cString(s string) (*byte, uint, []byte) {
	if s == "" {
		return nil, 0, nil
	}
	buf := make([]byte, len(s)+1)
	copy(buf, s)
	return &buf[0], uint(len(s)), buf
}

// resultError turns the chdb_result a management call returns into an error and
// destroys it. The result carries the engine's own message, and it has to be
// destroyed on both paths — a failed backup still allocates one.
func resultError(res *chdb_result, what string) error {
	if res == nil {
		return fmt.Errorf("chdb: %s returned no result", what)
	}
	defer chdbDestroyQueryResult(res)
	if msg := chdbResultError(res); msg != "" {
		return fmt.Errorf("chdb: %s failed: %s", what, msg)
	}
	return nil
}

// Each of the three holds the connection's read lock for the whole native
// call, the same as Query does. All of them dereference c.conn to reach the
// engine, and a Close racing one of them would free that connection while the
// engine is still inside it — a backup or a restore is long enough for the
// window to be wide rather than theoretical.

// BackupDatabase implements ChdbAdminConn.
func (c *connection) BackupDatabase(database, filePath string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.conn == nil {
		return fmt.Errorf("invalid connection")
	}
	if !adminABIAvailable() {
		return ErrAdminABIUnavailable
	}
	db, dbLen, dbBuf := cString(database)
	fp, fpLen, fpBuf := cString(filePath)
	// NULL base: V1 backups are always full. See BackupDatabase's contract.
	res := chdbBackupDatabaseN(c.conn.internal_data, db, dbLen, fp, fpLen, nil, 0)
	runtime.KeepAlive(dbBuf)
	runtime.KeepAlive(fpBuf)
	return resultError(res, "chdb_backup_database_n")
}

// RestoreDatabase implements ChdbAdminConn.
func (c *connection) RestoreDatabase(database, filePath string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.conn == nil {
		return fmt.Errorf("invalid connection")
	}
	if !adminABIAvailable() {
		return ErrAdminABIUnavailable
	}
	db, dbLen, dbBuf := cString(database)
	fp, fpLen, fpBuf := cString(filePath)
	res := chdbRestoreDatabaseN(c.conn.internal_data, db, dbLen, fp, fpLen)
	runtime.KeepAlive(dbBuf)
	runtime.KeepAlive(fpBuf)
	return resultError(res, "chdb_restore_database_n")
}

// ClassifyQuery implements ChdbAdminConn.
func (c *connection) ClassifyQuery(sql, targetDatabase string) (QueryAnalysis, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.conn == nil {
		return QueryAnalysis{}, fmt.Errorf("invalid connection")
	}
	if !adminABIAvailable() {
		return QueryAnalysis{}, ErrAdminABIUnavailable
	}
	out := queryAnalysisV1{structSize: uint32(unsafe.Sizeof(queryAnalysisV1{}))}
	q, qLen, qBuf := cString(sql)
	db, dbLen, dbBuf := cString(targetDatabase)
	state := chdbClassifyQueryN(c.conn.internal_data, q, qLen, db, dbLen, &out)
	runtime.KeepAlive(qBuf)
	runtime.KeepAlive(dbBuf)
	if state != 0 {
		// CHDBError here means the call was malformed or the connection is
		// closed, not that the SQL was bad — unparseable SQL succeeds and
		// reports UNKNOWN.
		return QueryAnalysis{}, fmt.Errorf("chdb: chdb_classify_query_n could not analyse the statement")
	}
	return QueryAnalysis{
		StatementCount:           out.statementCount,
		Class:                    QueryClass(out.queryClass),
		HasSecrets:               out.flags&analysisHasSecrets != 0,
		WritesOnlyTargetDatabase: out.flags&analysisWritesOnlyTargetDatabase != 0,
		ChangesDatabaseLifecycle: out.flags&analysisChangesDatabaseLifecycle != 0,
	}, nil
}
