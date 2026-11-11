package chdbdriver

import (
	"database/sql"
	"fmt"
	"math"
	"testing"
)

func openSessionDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("chdb", fmt.Sprintf("session=%s", session.ConnStr()))
	if err != nil {
		t.Fatalf("open db fail, err: %s", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func queryUint64s(t *testing.T, db *sql.DB, query string, args ...any) []uint64 {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("query %q: %s", query, err)
	}
	defer rows.Close()
	var got []uint64
	for rows.Next() {
		var v uint64
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %s", err)
		}
		got = append(got, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %s", err)
	}
	return got
}

func assertUint64s(t *testing.T, got, want []uint64) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Issue #27: a UInt64 above math.MaxInt64 could not be passed as an argument.
func TestExecUint64ArgumentAboveMaxInt64(t *testing.T) {
	db := openSessionDB(t)
	if _, err := db.Exec("CREATE TABLE TestExecUint64Arg (id UInt64) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE TestExecUint64Arg") })

	for _, v := range []uint64{0, math.MaxInt64 + 1, math.MaxUint64} {
		if _, err := db.Exec("INSERT INTO TestExecUint64Arg VALUES (?)", v); err != nil {
			t.Fatalf("insert %d: %s", v, err)
		}
	}
	if _, err := db.Exec("INSERT INTO TestExecUint64Arg VALUES (?)", uint(7)); err != nil {
		t.Fatalf("insert uint: %s", err)
	}

	got := queryUint64s(t, db, "SELECT id FROM TestExecUint64Arg WHERE id >= ? ORDER BY id", uint64(math.MaxInt64+1))
	assertUint64s(t, got, []uint64{math.MaxInt64 + 1, math.MaxUint64})
	assertUint64s(t, queryUint64s(t, db, "SELECT id FROM TestExecUint64Arg ORDER BY id"),
		[]uint64{0, 7, math.MaxInt64 + 1, math.MaxUint64})
}
