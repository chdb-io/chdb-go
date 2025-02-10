package chdbdriver

import (
	"database/sql"
	"fmt"
	"testing"
)

func TestDbWithParquet(t *testing.T) {

	db, err := sql.Open("chdb", fmt.Sprintf("driverType=%s", "PARQUET"))
	if err != nil {
		t.Errorf("open db fail, err:%s", err)
	}
	if db.Ping() != nil {
		t.Errorf("ping db fail")
	}
	rows, err := db.Query(`SELECT 1,'abc'`)
	if err != nil {
		t.Errorf("run Query fail, err:%s", err)
	}
	cols, err := rows.Columns()
	if err != nil {
		t.Errorf("get result columns fail, err: %s", err)
	}
	if len(cols) != 2 {
		t.Errorf("select result columns length should be 2")
	}
	var (
		bar int
		foo string
	)
	defer rows.Close()
	for rows.Next() {
		err := rows.Scan(&bar, &foo)
		if err != nil {
			t.Errorf("scan fail, err: %s", err)
		}
		if bar != 1 {
			t.Errorf("expected error")
		}
		if foo != "abc" {
			t.Errorf("expected error")
		}
	}
}

func TestDBWithParquetSession(t *testing.T) {

	session.Query(
		"CREATE TABLE IF NOT EXISTS TestDBWithParquetSession (id UInt32) ENGINE = MergeTree() ORDER BY id;")

	session.Query("INSERT INTO TestDBWithParquetSession VALUES (1), (2), (3);")

	ret, err := session.Query("SELECT * FROM TestDBWithParquetSession;")
	if err != nil {
		t.Fatalf("Query fail, err: %s", err)
	}
	if string(ret.Buf()) != "1\n2\n3\n" {
		t.Errorf("Query result should be 1\n2\n3\n, got %s", string(ret.Buf()))
	}
	db, err := sql.Open("chdb", fmt.Sprintf("session=%s;driverType=%s", session.ConnStr(), "PARQUET"))
	if err != nil {
		t.Fatalf("open db fail, err: %s", err)
	}
	if db.Ping() != nil {
		t.Fatalf("ping db fail, err: %s", err)
	}
	rows, err := db.Query("select * from TestDBWithParquetSession;")
	if err != nil {
		t.Fatalf("exec create function fail, err: %s", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("get result columns fail, err: %s", err)
	}
	if len(cols) != 1 {
		t.Fatalf("result columns length shoule be 3, actual: %d", len(cols))
	}
	var bar = 0
	var count = 1
	for rows.Next() {
		err = rows.Scan(&bar)
		if err != nil {
			t.Fatalf("scan fail, err: %s", err)
		}
		if bar != count {
			t.Fatalf("result is not match, want: %d actual: %d", count, bar)
		}
		count++
	}
}

func TestDBWithParquetConnection(t *testing.T) {

	session.Query(
		"CREATE TABLE IF NOT EXISTS TestDBWithParquetConnection (id UInt32) ENGINE = MergeTree() ORDER BY id;")

	session.Query("INSERT INTO TestDBWithParquetConnection VALUES (1), (2), (3);")

	ret, err := session.Query("SELECT * FROM TestDBWithParquetConnection;")
	if err != nil {
		t.Fatalf("Query fail, err: %s", err)
	}
	if string(ret.Buf()) != "1\n2\n3\n" {
		t.Errorf("Query result should be 1\n2\n3\n, got %s", string(ret.Buf()))
	}
	db, err := sql.Open("chdb", fmt.Sprintf("session=%s;driverType=%s", session.ConnStr(), "PARQUET"))
	if err != nil {
		t.Fatalf("open db fail, err: %s", err)
	}
	if db.Ping() != nil {
		t.Fatalf("ping db fail, err: %s", err)
	}
	rows, err := db.Query("select * from TestDBWithParquetConnection;")
	if err != nil {
		t.Fatalf("exec create function fail, err: %s", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("get result columns fail, err: %s", err)
	}
	if len(cols) != 1 {
		t.Fatalf("result columns length shoule be 3, actual: %d", len(cols))
	}
	var bar = 0
	var count = 1
	for rows.Next() {
		err = rows.Scan(&bar)
		if err != nil {
			t.Fatalf("scan fail, err: %s", err)
		}
		if bar != count {
			t.Fatalf("result is not match, want: %d actual: %d", count, bar)
		}
		count++
	}
}
