package chdbpurego

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// adminConn opens a connection whose backups are allowed inside a temporary
// directory, and returns it with that directory. A connection that never set
// `backups.allowed_path` cannot write a backup anywhere, so the option is not
// optional for these tests.
func adminConn(t *testing.T) (ChdbAdminConn, string) {
	t.Helper()

	requireAdminABI(t)

	root := t.TempDir()
	backups := filepath.Join(root, "backups")
	if err := os.MkdirAll(backups, 0o755); err != nil {
		t.Fatal(err)
	}

	conn, err := NewConnectionFromConnString(
		filepath.Join(root, "data") + "?backups.allowed_path=" + backups)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)

	admin, ok := conn.(ChdbAdminConn)
	if !ok {
		t.Fatal("connection does not implement ChdbAdminConn")
	}
	return admin, backups
}

func run(t *testing.T, conn ChdbAdminConn, sql string) {
	t.Helper()
	res, err := conn.(ChdbConn).Query(sql, "CSV")
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	if res != nil {
		res.Free()
	}
}

func TestVersionIsAChdbRelease(t *testing.T) {
	requireAdminABI(t)
	version, err := Version()
	if err != nil {
		t.Fatalf("libchdb did not load: %v", err)
	}
	// Not compared against a pin: the point is that the engine answers with
	// something version-shaped, since it is what a durable object records as
	// its producer and its minimum reader.
	if version == "" || !strings.ContainsRune(version, '.') {
		t.Fatalf("chdb_version() returned %q, which is not a release", version)
	}
	t.Logf("engine version %s", version)
}

func TestClassifyQueryReportsWhatCoreDecides(t *testing.T) {
	conn, _ := adminConn(t)
	run(t, conn, "CREATE DATABASE IF NOT EXISTS mem")
	run(t, conn, "CREATE TABLE IF NOT EXISTS mem.t (a Int32) ENGINE = MergeTree ORDER BY a")

	cases := []struct {
		name string
		sql  string
		want QueryAnalysis
	}{
		{
			name: "select",
			sql:  "SELECT 1",
			want: QueryAnalysis{StatementCount: 1, Class: QueryReadOnly, WritesOnlyTargetDatabase: true},
		},
		{
			name: "insert into the target database",
			sql:  "INSERT INTO mem.t VALUES (1)",
			want: QueryAnalysis{StatementCount: 1, Class: QueryMutating, WritesOnlyTargetDatabase: true},
		},
		{
			name: "insert into another database",
			sql:  "INSERT INTO other.t VALUES (1)",
			want: QueryAnalysis{StatementCount: 1, Class: QueryMutating},
		},
		{
			name: "two statements",
			sql:  "INSERT INTO mem.t VALUES (1); INSERT INTO mem.t VALUES (2)",
			want: QueryAnalysis{StatementCount: 2, Class: QueryMutating, WritesOnlyTargetDatabase: true},
		},
		{
			name: "inline data holding a semicolon",
			sql:  "INSERT INTO mem.t FORMAT CSV 1;2\n",
			want: QueryAnalysis{StatementCount: 1, Class: QueryMutating, WritesOnlyTargetDatabase: true},
		},
		{
			name: "database lifecycle",
			sql:  "DROP DATABASE mem",
			want: QueryAnalysis{
				StatementCount: 1, Class: QueryMutating,
				WritesOnlyTargetDatabase: true, ChangesDatabaseLifecycle: true,
			},
		},
		{
			name: "session control",
			sql:  "SET max_threads = 1",
			want: QueryAnalysis{StatementCount: 1, Class: QueryControl},
		},
		{
			name: "global mutation",
			sql:  "CREATE FUNCTION plus_one AS (x) -> x + 1",
			want: QueryAnalysis{StatementCount: 1, Class: QueryMutatingGlobal},
		},
		{
			name: "does not parse",
			sql:  "this is not sql",
			want: QueryAnalysis{StatementCount: 0, Class: QueryUnknown},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := conn.ClassifyQuery(tc.sql, "mem")
			if err != nil {
				t.Fatalf("classify: %v", err)
			}
			if got != tc.want {
				t.Fatalf("analysis of %q\n got %+v\nwant %+v", tc.sql, got, tc.want)
			}
		})
	}
}

func TestClassifyQueryWithoutATargetNeverProvesTheDatabase(t *testing.T) {
	conn, _ := adminConn(t)
	got, err := conn.ClassifyQuery("SELECT 1", "")
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if got.WritesOnlyTargetDatabase {
		t.Fatal("no target database was named, so the flag must not be set")
	}
}

func TestBackupAndRestoreRoundTrip(t *testing.T) {
	conn, backups := adminConn(t)
	run(t, conn, "CREATE DATABASE mem")
	run(t, conn, "CREATE TABLE mem.t (a Int32) ENGINE = MergeTree ORDER BY a")
	run(t, conn, "INSERT INTO mem.t VALUES (1), (2), (3)")

	archive := filepath.Join(backups, "mem.tar.gz")
	if err := conn.BackupDatabase("mem", archive); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("backup produced no archive: %v", err)
	}

	// An archive names the database it came from, so it restores under that
	// name and no other: `RESTORE DATABASE copy` from an archive of `mem`
	// fails with BACKUP_ENTRY_NOT_FOUND. Dropping the database first is what
	// gives restore the empty target it needs — and is why a durable object
	// restores into a fresh scratch directory rather than over live state.
	run(t, conn, "DROP DATABASE mem")
	if err := conn.RestoreDatabase("mem", archive); err != nil {
		t.Fatalf("restore: %v", err)
	}
	res, err := conn.(ChdbConn).Query("SELECT sum(a) FROM mem.t", "CSV")
	if err != nil {
		t.Fatalf("query the restored database: %v", err)
	}
	defer res.Free()
	if got := strings.TrimSpace(res.String()); got != "6" {
		t.Fatalf("restored database holds %q, want 6", got)
	}
}

func TestBackupRefusesToOverwrite(t *testing.T) {
	conn, backups := adminConn(t)
	run(t, conn, "CREATE DATABASE mem")

	archive := filepath.Join(backups, "once.tar.gz")
	if err := conn.BackupDatabase("mem", archive); err != nil {
		t.Fatalf("first backup: %v", err)
	}
	if err := conn.BackupDatabase("mem", archive); err == nil {
		t.Fatal("a second backup to the same path must fail rather than overwrite")
	}
}

// A database name needing quotes is the case a binding that built the SQL
// itself would get wrong, so it is the case worth asserting core handles.
func TestBackupQuotesTheDatabaseName(t *testing.T) {
	conn, backups := adminConn(t)
	run(t, conn, "CREATE DATABASE `my-db`")
	run(t, conn, "CREATE TABLE `my-db`.t (a Int32) ENGINE = MergeTree ORDER BY a")
	run(t, conn, "INSERT INTO `my-db`.t VALUES (7)")

	archive := filepath.Join(backups, "quoted.tar.gz")
	if err := conn.BackupDatabase("my-db", archive); err != nil {
		t.Fatalf("backup: %v", err)
	}
	run(t, conn, "DROP DATABASE `my-db`")
	if err := conn.RestoreDatabase("my-db", archive); err != nil {
		t.Fatalf("restore: %v", err)
	}
	res, err := conn.(ChdbConn).Query("SELECT a FROM `my-db`.t", "CSV")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer res.Free()
	if got := strings.TrimSpace(res.String()); got != "7" {
		t.Fatalf("restored %q, want 7", got)
	}
}

// requireAdminABI reports whether the loaded engine has the management ABI,
// and decides what a missing one means.
//
// Absent, it is a skip: this package still works on an older libchdb and a
// developer who has one installed should not see failures for it. Under
// CHDB_REQUIRE_DURABLE_ABI it is a failure instead, which is what CI sets —
// there the engine version is pinned, so a skip would mean the installer
// quietly fell back to a release without the ABI. lib.chdb.io does exactly
// that on a failed download: it retries against releases/latest, and the
// pinned engine is a pre-release, so "latest" is an older one. A suite that is
// green whether or not it ran is not a check.
func requireAdminABI(t *testing.T) {
	t.Helper()
	available, err := AdminABIAvailable()
	required := os.Getenv("CHDB_REQUIRE_DURABLE_ABI") != ""
	if err != nil {
		if required {
			t.Fatalf("libchdb did not load: %v", err)
		}
		t.Skipf("libchdb did not load: %v", err)
	}
	if available {
		return
	}
	version, _ := Version()
	message := "the loaded libchdb predates the backup/restore/classify ABI " +
		"(added in chdb-core v26.7.2-rc.2); it reports " + version
	if required {
		t.Fatal("CHDB_REQUIRE_DURABLE_ABI is set but " + message)
	}
	t.Skip(message)
}
