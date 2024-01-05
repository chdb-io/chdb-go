package chdbdriver

import (
	"database/sql"
	"fmt"
	"os"
	"testing"

	"github.com/chdb-io/chdb-go/chdb"
)

func TestDb(t *testing.T) {
	db, err := sql.Open("chdb", "")
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

func TestDbWithOpt(t *testing.T) {
	for _, kv := range []struct {
		opt       string
		condition bool
	}{
		{"", false},
		{"udfPath=qq", false},
		{"udfPath=qq;session=ss", false},
		{"session=sssss", false},
		{"session=s2;udfPath=u1", false},
		{"session=s3;udfPath=u2;fooobar=ssss", false},
		{"foo;bar", true},
	} {
		db, err := sql.Open("chdb", kv.opt)
		if (err != nil) != kv.condition {
			t.Errorf("open db fail, err: %s", err)
		}
		if db == nil {
			continue
		}
		if (db.Ping() != nil) != kv.condition {
			t.Errorf("ping db fail")
		}
	}
}

func TestDbWithSession(t *testing.T) {
	sessionDir, err := os.MkdirTemp("", "unittest-sessiondata")
	if err != nil {
		t.Fatalf("create temp directory fail, err: %s", err)
	}
	defer os.RemoveAll(sessionDir)
	session, err := chdb.NewSession(sessionDir)
	if err != nil {
		t.Fatalf("new session fail, err: %s", err)
	}
	defer session.Cleanup()

	session.Query("CREATE DATABASE IF NOT EXISTS testdb; " +
		"CREATE TABLE IF NOT EXISTS testdb.testtable (id UInt32) ENGINE = MergeTree() ORDER BY id;")

	session.Query("USE testdb; INSERT INTO testtable VALUES (1), (2), (3);")

	ret := session.Query("SELECT * FROM testtable;")
	if string(ret.Buf()) != "1\n2\n3\n" {
		t.Errorf("Query result should be 1\n2\n3\n, got %s", string(ret.Buf()))
	}
	db, err := sql.Open("chdb", fmt.Sprintf("session=%s", sessionDir))
	if err != nil {
		t.Fatalf("open db fail, err: %s", err)
	}
	if db.Ping() != nil {
		t.Fatalf("ping db fail, err: %s", err)
	}
	rows, err := db.Query("select * from testtable;")
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
