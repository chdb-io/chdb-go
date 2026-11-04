package chdbdriver

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/chdb-io/chdb-go/v2/chdb"
)

func TestDbWithParquetStreaming(t *testing.T) {

	db, err := sql.Open("chdb", fmt.Sprintf("driverType=%s", "PARQUET_STREAMING"))
	if err != nil {
		t.Errorf("open db fail, err:%s", err)
	}
	defer db.Close()
	if db.Ping() != nil {
		t.Errorf("ping db fail")
	}
	rows, err := db.Query(`SELECT 1,number from system.numbers limit 100000`)
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
		foo int
	)
	defer rows.Close()
	// Counted, because this sessionless path streams through chdb.QueryStream:
	// when that helper closed its session too early the engine answered with no
	// rows at all (or crashed), and a loop that only checks the rows it does see
	// stayed green.
	scanned := 0
	for rows.Next() {
		err := rows.Scan(&bar, &foo)
		if err != nil {
			t.Errorf("scan fail, err: %s", err)
		}
		if bar != 1 {
			t.Errorf("expected error")
		}
		scanned++
	}
	if err := rows.Err(); err != nil {
		t.Errorf("iterate rows fail, err: %s", err)
	}
	if scanned != 100000 {
		t.Errorf("scanned %d rows, want 100000", scanned)
	}

}

func TestDBWithParquetStreamingSession(t *testing.T) {

	session.Query(
		"CREATE TABLE IF NOT EXISTS TestDBWithParquetSessionStreaming (id UInt32) ENGINE = MergeTree() ORDER BY id;")

	session.Query("INSERT INTO TestDBWithParquetSessionStreaming VALUES (1), (2), (3);")

	ret, err := session.Query("SELECT * FROM TestDBWithParquetSessionStreaming;")
	if err != nil {
		t.Fatalf("Query fail, err: %s", err)
	}
	if string(ret.Buf()) != "1\n2\n3\n" {
		t.Errorf("Query result should be 1\n2\n3\n, got %s", string(ret.Buf()))
	}
	db, err := sql.Open("chdb", fmt.Sprintf("session=%s;driverType=%s", session.ConnStr(), "PARQUET_STREAMING"))
	if err != nil {
		t.Fatalf("open db fail, err: %s", err)
	}
	defer db.Close()
	if db.Ping() != nil {
		t.Fatalf("ping db fail, err: %s", err)
	}
	rows, err := db.Query("select * from TestDBWithParquetSessionStreaming;")
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

func TestDBWithParquetStreamingConnection(t *testing.T) {

	session.Query(
		"CREATE TABLE IF NOT EXISTS TestDBWithParquetConnectionStreaming (id UInt32) ENGINE = MergeTree() ORDER BY id;")

	session.Query("INSERT INTO TestDBWithParquetConnectionStreaming VALUES (1), (2), (3);")

	ret, err := session.Query("SELECT * FROM TestDBWithParquetConnectionStreaming;")
	if err != nil {
		t.Fatalf("Query fail, err: %s", err)
	}
	if string(ret.Buf()) != "1\n2\n3\n" {
		t.Errorf("Query result should be 1\n2\n3\n, got %s", string(ret.Buf()))
	}
	db, err := sql.Open("chdb", fmt.Sprintf("session=%s;driverType=%s", session.ConnStr(), "PARQUET_STREAMING"))
	if err != nil {
		t.Fatalf("open db fail, err: %s", err)
	}
	defer db.Close()
	if db.Ping() != nil {
		t.Fatalf("ping db fail, err: %s", err)
	}

	rows, err := db.Query("select * from TestDBWithParquetConnectionStreaming;")
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

// A failing query on a streaming handle used to panic: the engine reports the
// error on the first chunk, which carries no bytes, and that chunk went straight
// to the Parquet reader ("reading magic header of parquet file: EOF"). The panic
// also skipped the cleanup that frees the stream, so each failure left a session
// open and the Shutdown in globalTeardown refused with ErrSessionsOpen.
func TestQueryErrorOnStreamingConnection(t *testing.T) {
	before := chdb.ActiveSessionRefs()
	db, err := sql.Open("chdb", "driverType=PARQUET_STREAMING")
	if err != nil {
		t.Fatalf("open db fail, err: %s", err)
	}
	defer db.Close()

	// Both fail before the query produces output. An error raised while the
	// query runs takes the same path here, but chdb-core v26.11.0-rc.1 leaves
	// that query's partial Parquet output in the buffer the next result is
	// written to, which would corrupt the query below and the tests after this.
	for _, query := range []string{
		"SELECT * FROM TestQueryErrorOnStreamingConnection_missing",
		"SELECT TestQueryErrorOnStreamingConnection_missing()",
	} {
		err := func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panic: %v", r)
				}
			}()
			rows, err := db.Query(query)
			if err == nil {
				rows.Close()
			}
			return err
		}()
		if err == nil {
			t.Fatalf("%q: expected an error", query)
		}
		if !strings.Contains(err.Error(), "DB::Exception") {
			t.Fatalf("%q: expected the engine's error, got: %s", query, err)
		}
	}

	// The handle still works afterwards.
	var n int
	if err := db.QueryRow("SELECT 42").Scan(&n); err != nil || n != 42 {
		t.Fatalf("query after the failures: n=%d err=%v", n, err)
	}

	// Shutdown refuses while any session is open, so a leaked stream shows here.
	db.Close()
	if after := chdb.ActiveSessionRefs(); after != before {
		t.Fatalf("open sessions went from %d to %d; a failed query leaked its stream", before, after)
	}
}
