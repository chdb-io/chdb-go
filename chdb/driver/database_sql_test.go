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

// Issue #27: db.Prepare returned "does not support prepare statement".
func TestPreparedStatement(t *testing.T) {
	db := openSessionDB(t)
	if _, err := db.Exec("CREATE TABLE TestPreparedStatement (id UInt64, name String) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE TestPreparedStatement") })

	ins, err := db.Prepare("INSERT INTO TestPreparedStatement VALUES (?, ?)")
	if err != nil {
		t.Fatalf("prepare insert: %s", err)
	}
	defer ins.Close()
	for i, name := range []string{"a", "b's", "c"} {
		if _, err := ins.Exec(uint64(i+1), name); err != nil {
			t.Fatalf("exec %d: %s", i, err)
		}
	}

	sel, err := db.Prepare("SELECT name FROM TestPreparedStatement WHERE id = ?")
	if err != nil {
		t.Fatalf("prepare select: %s", err)
	}
	defer sel.Close()
	for id, want := range map[uint64]string{1: "a", 2: "b's", 3: "c"} {
		var got string
		if err := sel.QueryRow(id).Scan(&got); err != nil {
			t.Fatalf("query %d: %s", id, err)
		}
		if got != want {
			t.Fatalf("id %d: got %q, want %q", id, got, want)
		}
	}
}
