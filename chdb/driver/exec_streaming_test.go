package chdbdriver

import (
	"database/sql"
	"testing"
)

// Exec on a streaming connection used to be a nil-func call: SetupQueryFun wired
// only streamFun when the driver type streams, and ExecContext goes through
// QueryFun — so the first db.Exec on a PARQUET_STREAMING handle panicked with a
// nil pointer dereference. Creating a table and inserting into it is the first
// thing a caller does, streaming or not.
func TestExecOnStreamingConnection(t *testing.T) {
	db, err := sql.Open("chdb", "driverType=PARQUET_STREAMING")
	if err != nil {
		t.Fatalf("open db fail, err:%s", err)
	}
	defer db.Close()

	if _, err := db.Exec(
		"CREATE TABLE IF NOT EXISTS TestExecOnStreamingConnection (id UInt32) ENGINE = MergeTree() ORDER BY id;"); err != nil {
		t.Fatalf("create table fail, err:%s", err)
	}
	res, err := db.Exec("INSERT INTO TestExecOnStreamingConnection VALUES (1), (2), (3);")
	if err != nil {
		t.Fatalf("insert fail, err:%s", err)
	}
	if _, err := res.RowsAffected(); err != nil {
		t.Errorf("RowsAffected fail, err:%s", err)
	}

	// And the streaming read path still works on the same handle.
	rows, err := db.Query("SELECT id FROM TestExecOnStreamingConnection ORDER BY id;")
	if err != nil {
		t.Fatalf("run Query fail, err:%s", err)
	}
	defer rows.Close()
	var ids []uint32
	for rows.Next() {
		var id uint32
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan fail, err:%s", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rows fail, err:%s", err)
	}
	if len(ids) != 3 {
		t.Errorf("read back %v, want 3 rows", ids)
	}
}
