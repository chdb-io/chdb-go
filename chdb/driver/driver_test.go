package chdbdriver

import (
	"database/sql"
	"fmt"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/chdb-io/chdb-go/v2/chdb"
)

var (
	session *chdb.Session
)

func globalSetup() error {
	sess, err := chdb.NewSession()
	if err != nil {
		return err
	}
	session = sess
	return nil
}

func globalTeardown() {
	session.Cleanup()
	session.Close()
}

func TestMain(m *testing.M) {
	if err := globalSetup(); err != nil {
		fmt.Println("Global setup failed:", err)
		os.Exit(1)
	}
	// Run all tests.
	exitCode := m.Run()

	// Global teardown: clean up any resources here.
	globalTeardown()

	// Exit with the code returned by m.Run().
	os.Exit(exitCode)
}

func TestDb(t *testing.T) {
	db, err := sql.Open("chdb", "")
	if err != nil {
		t.Fatalf("open db fail, err:%s", err)
	}
	defer db.Close()
	if db.Ping() != nil {
		t.Fatalf("ping db fail")
	}
	rows, err := db.Query(`SELECT 1,'abc'`)
	if err != nil {
		t.Fatalf("run Query fail, err:%s", err)
	}
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("get result columns fail, err: %s", err)
	}
	if len(cols) != 2 {
		t.Fatalf("select result columns length should be 2")
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

func TestDbWithCompiledArgs(t *testing.T) {
	db, err := sql.Open("chdb", "")
	if err != nil {
		t.Errorf("open db fail, err:%s", err)
	}
	defer db.Close()
	if db.Ping() != nil {
		t.Errorf("ping db fail")
	}
	rows, err := db.Query(`SELECT ?, ?`, 1, "abc")
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
	// chDB allows only one data path per process, so every session option must
	// point at the same path the global test session already opened. These cases
	// exercise connection-string parsing, not multiple data paths.
	sp := session.ConnStr()
	for _, kv := range []struct {
		opt       string
		condition bool
	}{
		{"", false},
		{"udfPath=qq", false},
		{fmt.Sprintf("udfPath=qq;session=%s", sp), false},
		{fmt.Sprintf("session=%s", sp), false},
		{fmt.Sprintf("session=%s;udfPath=u1", sp), false},
		{fmt.Sprintf("session=%s;udfPath=u2;fooobar=ssss", sp), false},
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
		db.Close()
	}
}

func TestDbWithSession(t *testing.T) {

	session.Query(
		"CREATE TABLE IF NOT EXISTS TestDbWithSession (id UInt32) ENGINE = MergeTree() ORDER BY id;")

	session.Query("INSERT INTO TestDbWithSession VALUES (1), (2), (3);")

	ret, err := session.Query("SELECT * FROM TestDbWithSession;")
	if err != nil {
		t.Fatalf("Query fail, err: %s", err)
	}
	if string(ret.Buf()) != "1\n2\n3\n" {
		t.Errorf("Query result should be 1\n2\n3\n, got %s", string(ret.Buf()))
	}
	db, err := sql.Open("chdb", fmt.Sprintf("session=%s", session.ConnStr()))
	if err != nil {
		t.Fatalf("open db fail, err: %s", err)
	}
	defer db.Close()
	if db.Ping() != nil {
		t.Fatalf("ping db fail, err: %s", err)
	}
	rows, err := db.Query("select * from TestDbWithSession;")
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

func TestDbWithConnection(t *testing.T) {

	session.Query(
		"CREATE TABLE IF NOT EXISTS TestDbWithConnection (id UInt32) ENGINE = MergeTree() ORDER BY id;")

	session.Query("INSERT INTO TestDbWithConnection VALUES (1), (2), (3);")

	ret, err := session.Query("SELECT * FROM TestDbWithConnection;")
	if err != nil {
		t.Fatalf("Query fail, err: %s", err)
	}
	if string(ret.Buf()) != "1\n2\n3\n" {
		t.Errorf("Query result should be 1\n2\n3\n, got %s", string(ret.Buf()))
	}
	db, err := sql.Open("chdb", fmt.Sprintf("session=%s", session.ConnStr()))
	if err != nil {
		t.Fatalf("open db fail, err: %s", err)
	}
	defer db.Close()
	if db.Ping() != nil {
		t.Fatalf("ping db fail, err: %s", err)
	}
	rows, err := db.Query("select * from TestDbWithConnection;")
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

func TestDbWithConnectionSqlDriverOnly(t *testing.T) {
	db, err := sql.Open("chdb", fmt.Sprintf("session=%s", session.ConnStr()))
	if err != nil {
		t.Fatalf("open db fail, err: %s", err)
	}
	defer db.Close()
	if db.Ping() != nil {
		t.Fatalf("ping db fail, err: %s", err)
	}

	_, err = db.Exec(
		"CREATE TABLE IF NOT EXISTS TestDbWithConnectionSqlDriverOnly (id UInt32) ENGINE = MergeTree() ORDER BY id;")
	if err != nil {
		t.Fatalf("could not create database & table: %s", err)
	}
	_, err = db.Exec("INSERT INTO TestDbWithConnectionSqlDriverOnly VALUES (1), (2), (3);")
	if err != nil {
		t.Fatalf("could not insert rows in the table: %s", err)
	}

	rows, err := db.Query("select * from TestDbWithConnectionSqlDriverOnly;")
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

func TestQueryRow(t *testing.T) {

	session.Query(
		"CREATE TABLE IF NOT EXISTS TestQueryRow (id UInt32) ENGINE = MergeTree() ORDER BY id;")

	session.Query(" INSERT INTO TestQueryRow VALUES (1), (2), (3);")

	ret, err := session.Query("SELECT * FROM TestQueryRow;")
	if err != nil {
		t.Fatalf("Query fail, err: %s", err)
	}
	if string(ret.Buf()) != "1\n2\n3\n" {
		t.Errorf("Query result should be 1\n2\n3\n, got %s", string(ret.Buf()))
	}
	db, err := sql.Open("chdb", fmt.Sprintf("session=%s", session.ConnStr()))
	if err != nil {
		t.Fatalf("open db fail, err: %s", err)
	}
	defer db.Close()
	if db.Ping() != nil {
		t.Fatalf("ping db fail, err: %s", err)
	}
	rows := db.QueryRow("select * from TestQueryRow;")

	var bar = 0
	var count = 1
	err = rows.Scan(&bar)
	if err != nil {
		t.Fatalf("scan fail, err: %s", err)
	}
	if bar != count {
		t.Fatalf("result is not match, want: %d actual: %d", count, bar)
	}
	err2 := rows.Scan(&bar)
	if err2 == nil {
		t.Fatalf("QueryRow method should return only one item")
	}

}

func TestExec(t *testing.T) {

	session.Query(
		"CREATE TABLE IF NOT EXISTS TestExec (id UInt32) ENGINE = MergeTree() ORDER BY id;")

	db, err := sql.Open("chdb", fmt.Sprintf("session=%s", session.ConnStr()))
	if err != nil {
		t.Fatalf("open db fail, err: %s", err)
	}
	defer db.Close()
	if db.Ping() != nil {
		t.Fatalf("ping db fail, err: %s", err)
	}

	tables, err := db.Query("SHOW TABLES;")
	if err != nil {
		t.Fatalf(err.Error())
	}
	defer tables.Close()
	for tables.Next() {
		var tblName string
		if err := tables.Scan(&tblName); err != nil {
			t.Fatal(err)
		}
		t.Log(tblName)
		fmt.Printf("tblName: %v\n", tblName)
	}

	_, err = db.Exec("INSERT INTO TestExec VALUES (1), (2), (3);")
	if err != nil {
		t.Fatalf("exec failed, err: %s", err)
	}
	rows := db.QueryRow("select * from TestExec;")

	var bar = 0
	var count = 1
	err = rows.Scan(&bar)
	if err != nil {
		t.Fatalf("scan fail, err: %s", err)
	}
	if bar != count {
		t.Fatalf("result is not match, want: %d actual: %d", count, bar)
	}
	err2 := rows.Scan(&bar)
	if err2 == nil {
		t.Fatalf("QueryRow method should return only one item")
	}

}

// TestConcurrentQueries verifies that many goroutines can run queries in
// parallel through a single *sql.DB with MaxOpenConns > 1. Each pooled
// connection is an independent native chDB connection, so this exercises real
// concurrent execution rather than serialization on one connection.
func TestConcurrentQueries(t *testing.T) {
	db, err := sql.Open("chdb", fmt.Sprintf("session=%s", session.ConnStr()))
	if err != nil {
		t.Fatalf("open db fail, err: %s", err)
	}
	defer db.Close()

	maxConns := runtime.NumCPU()
	if maxConns < 2 {
		maxConns = 2
	}
	db.SetMaxOpenConns(maxConns)

	const (
		workers   = 8
		perWorker = 25
	)
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				var n int
				if err := db.QueryRow("SELECT count() FROM numbers(1000)").Scan(&n); err != nil {
					errCh <- fmt.Errorf("query failed: %w", err)
					return
				}
				if n != 1000 {
					errCh <- fmt.Errorf("got count %d, want 1000", n)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Fatalf("concurrent query error: %v", e)
	}
}

// TestConcurrentParallelism proves that queries on separate pooled connections
// run in parallel rather than serialized. It uses sleep() so wall-clock time
// reflects scheduling rather than CPU work: N concurrent sleeps should finish in
// roughly one sleep interval, while running them serially takes ~N intervals.
func TestConcurrentParallelism(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-based parallelism test in -short mode")
	}
	db, err := sql.Open("chdb", fmt.Sprintf("session=%s", session.ConnStr()))
	if err != nil {
		t.Fatalf("open db fail, err: %s", err)
	}
	defer db.Close()

	const (
		n        = 4
		sleepStr = "SELECT sleep(0.5)"
	)

	// Serial baseline: a single connection runs the queries back-to-back.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(sleepStr); err != nil { // warm up engine/connection
		t.Fatalf("warmup failed: %s", err)
	}
	startSerial := time.Now()
	for i := 0; i < n; i++ {
		if _, err := db.Exec(sleepStr); err != nil {
			t.Fatalf("serial exec failed: %s", err)
		}
	}
	serial := time.Since(startSerial)

	// Parallel: n connections run the queries concurrently.
	db.SetMaxOpenConns(n)
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	startParallel := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := db.Exec(sleepStr); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Fatalf("parallel exec failed: %v", e)
	}
	parallel := time.Since(startParallel)

	t.Logf("serial=%s parallel=%s speedup=%.2fx (n=%d)", serial, parallel, float64(serial)/float64(parallel), n)

	// True parallelism makes the concurrent run finish far faster than serial.
	// Require a clear margin to stay robust against scheduling jitter.
	if float64(parallel) > float64(serial)*0.6 {
		t.Errorf("queries did not run in parallel: serial=%s parallel=%s", serial, parallel)
	}
}
