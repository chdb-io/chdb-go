package chdb

import (
	"fmt"

	chdbpurego "github.com/chdb-io/chdb-go/v2/chdb-purego"
)

// Management operations on a session: full database backup, restore, and
// saying what a statement would do without running it.
//
// They are separate from Query for the same reason they are separate in the C
// ABI: each one takes its arguments as values rather than as SQL text, so the
// engine builds the statement and does its own quoting. A caller managing a
// database it did not name itself — a durable object, a snapshot tool, a
// tenant-per-database service — cannot safely interpolate that name into
// `BACKUP DATABASE ...`, and does not have to.
//
// All three need an engine from chdb-core v26.7.2-rc.2 or later. On anything
// older they return chdbpurego.ErrAdminABIUnavailable, whose message names
// that release.

// EngineVersion returns the exact chdb_version() of the loaded engine, loading
// libchdb if that has not happened yet. It needs no session, because the
// version belongs to the library rather than to a connection.
func EngineVersion() (string, error) {
	return chdbpurego.Version()
}

// admin returns this session's management surface, or an error explaining why
// there is not one. The caller holds no lock; each method takes the read lock
// itself, the same as Query.
func (s *Session) admin() (chdbpurego.ChdbAdminConn, error) {
	if s.closed || s.conn == nil {
		return nil, fmt.Errorf("chdb: management operation on a closed session")
	}
	admin, ok := s.conn.(chdbpurego.ChdbAdminConn)
	if !ok {
		return nil, chdbpurego.ErrAdminABIUnavailable
	}
	return admin, nil
}

// BackupDatabase writes a full archive of database to filePath.
//
// filePath must be absolute and its parent directory must already exist, and
// it must sit inside the `backups.allowed_path` this session was opened with —
// a session that never set it cannot write a backup anywhere:
//
//	sess, _ := chdb.NewSession("/var/lib/app/data?backups.allowed_path=/var/lib/app/backups")
//	err := sess.BackupDatabase("orders", "/var/lib/app/backups/orders.tar.gz")
//
// An existing destination is never overwritten; the call fails instead. Give
// every archive its own name.
//
// The archive is always a full backup. chDB can also write one incrementally
// against an existing archive, and that is not offered here: such an archive
// records the base's path as it was given, so it restores only where that path
// still holds the base — which rules out moving it to another machine or into
// object storage.
func (s *Session) BackupDatabase(database, filePath string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	admin, err := s.admin()
	if err != nil {
		return err
	}
	return admin.BackupDatabase(database, filePath)
}

// RestoreDatabase restores database from an archive written by BackupDatabase.
//
// An archive names the database it was taken from, and restores under that
// name and no other — restoring an archive of `orders` as `orders_copy` fails
// rather than renaming it. RESTORE also appends to a table that already
// exists, so the target must not already hold the archive's tables; the usual
// shape is to restore into a fresh data directory. The session's current
// database is left alone.
func (s *Session) RestoreDatabase(database, filePath string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	admin, err := s.admin()
	if err != nil {
		return err
	}
	return admin.RestoreDatabase(database, filePath)
}

// ClassifyQuery says what sql would do, without running it: how many
// executable statements it holds, what class they fall into, whether the text
// carries a credential, and whether every persistent write lands in
// targetDatabase.
//
// The answers come from ClickHouse's own parser with this session's settings
// and current database, which is the only thing that can give them. A prefix
// check cannot see through `INSERT ... FORMAT` inline data, and no amount of
// pattern matching resolves an unqualified table name.
//
// Nothing is executed and the session is untouched: no current database
// change, no settings change, no query log entry. Pass "" for targetDatabase
// to skip the write-target judgement, in which case
// QueryAnalysis.WritesOnlyTargetDatabase is never set. SQL that does not parse
// is reported as chdbpurego.QueryUnknown with a statement count of zero and no
// error — what it is, is the answer.
func (s *Session) ClassifyQuery(sql, targetDatabase string) (chdbpurego.QueryAnalysis, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	admin, err := s.admin()
	if err != nil {
		return chdbpurego.QueryAnalysis{}, err
	}
	return admin.ClassifyQuery(sql, targetDatabase)
}
