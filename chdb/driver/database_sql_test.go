package chdbdriver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"math"
	"testing"
	"time"
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

func queryStrings(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("query %q: %s", query, err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var v string
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

func countRows(t *testing.T, db *sql.DB, table string) uint64 {
	t.Helper()
	var n uint64
	if err := db.QueryRow("SELECT count() FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %s", table, err)
	}
	return n
}

// Issue #27: the clickhouse-go database/sql batch idiom — Begin, Prepare an
// INSERT, Exec once per row, Commit — failed at Begin.
func TestTxBatchInsert(t *testing.T) {
	db := openSessionDB(t)
	if _, err := db.Exec("CREATE TABLE TestTxBatchInsert (id UInt64, name String, ts DateTime) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE TestTxBatchInsert") })

	for _, query := range []string{
		"INSERT INTO TestTxBatchInsert",
		"INSERT INTO TestTxBatchInsert (id, name, ts)",
		"INSERT INTO TestTxBatchInsert VALUES (?, ?, ?)",
		"insert into TestTxBatchInsert (id, name, ts) values (?, ?, ?);",
		"INSERT INTO TestTxBatchInsert;",
		"INSERT INTO TestTxBatchInsert VALUES (?, ?, ?) -- trailing comment",
		"INSERT INTO TestTxBatchInsert VALUES (?, ?, ?); -- trailing comment\n",
		"INSERT INTO TestTxBatchInsert -- the target\nVALUES (?, ?, ?)",
		"INSERT INTO /* t */ TestTxBatchInsert (id, name, ts) /* ? */ VALUES (?, ?, ?)",
		"INSERT INTO TestTxBatchInsert VALUES (?, ?, ?) // trailing comment",
		"INSERT INTO TestTxBatchInsert // the target\nVALUES (?, ?, ?)",
		"INSERT INTO TestTxBatchInsert VALUES (?, ?, ?) #! trailing comment",
	} {
		t.Run(query, func(t *testing.T) {
			if _, err := db.Exec("TRUNCATE TABLE TestTxBatchInsert"); err != nil {
				t.Fatal(err)
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatalf("begin: %s", err)
			}
			st, err := tx.Prepare(query)
			if err != nil {
				t.Fatalf("prepare: %s", err)
			}
			ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			for i, name := range []string{"a", "b's", "c"} {
				if _, err := st.Exec(uint64(math.MaxUint64-2)+uint64(i), name, ts); err != nil {
					t.Fatalf("exec row %d: %s", i, err)
				}
			}
			if n := countRows(t, db, "TestTxBatchInsert"); n != 0 {
				t.Fatalf("rows visible before Commit: %d", n)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit: %s", err)
			}
			assertUint64s(t, queryUint64s(t, db, "SELECT id FROM TestTxBatchInsert ORDER BY id"),
				[]uint64{math.MaxUint64 - 2, math.MaxUint64 - 1, math.MaxUint64})
			got := queryStrings(t, db, "SELECT concat(name, ' ', toString(ts)) FROM TestTxBatchInsert ORDER BY id")
			if want := "[a 2026-01-02 03:04:05 b's 2026-01-02 03:04:05 c 2026-01-02 03:04:05]"; fmt.Sprint(got) != want {
				t.Fatalf("got %v, want %s", got, want)
			}
		})
	}
}

// A /* */ inside a heredoc is data. Preparing the INSERT used to strip it as a
// comment, so the batch inserted "a b".
func TestTxBatchInsertKeepsHeredoc(t *testing.T) {
	db := openSessionDB(t)
	if _, err := db.Exec("CREATE TABLE TestTxBatchHeredoc (id UInt64, name String) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE TestTxBatchHeredoc") })

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %s", err)
	}
	st, err := tx.Prepare("INSERT INTO TestTxBatchHeredoc VALUES (?, $h$a/*keep*/b -- ?$h$)")
	if err != nil {
		t.Fatalf("prepare: %s", err)
	}
	for i := uint64(1); i <= 2; i++ {
		if _, err := st.Exec(i); err != nil {
			t.Fatalf("exec row %d: %s", i, err)
		}
	}
	if n := countRows(t, db, "TestTxBatchHeredoc"); n != 0 {
		t.Fatalf("rows visible before Commit: %d", n)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %s", err)
	}
	got := queryStrings(t, db, "SELECT name FROM TestTxBatchHeredoc ORDER BY id")
	if want := "[a/*keep*/b -- ? a/*keep*/b -- ?]"; fmt.Sprint(got) != want {
		t.Fatalf("got %v, want %s", got, want)
	}
}

// Removing what scanQuery calls comments must not change what a query
// returns, so nothing it strips is data to chDB.
func TestStripCommentsKeepsResult(t *testing.T) {
	db := openSessionDB(t)
	for _, query := range []string{
		"SELECT $h$a/*keep*/b$h$, $$-- x # y$$",
		"SELECT 6 // 2\n, 1",
		"SELECT 1 -- c\r, 2\n, 3",
		"SELECT 1 # c\n, 2 #! c\n, 3",
		"SELECT /* a /* b */ c */ 1, '/* d */', \"x/*\" FROM (SELECT 2 AS `x/*`)",
		"SELECT ‘-- a’, x'2D2D', 0x1e--c\n, 1e-1--c\n, tuple(1, 2).1--c\n, (.5e-1--c\n)",
	} {
		want := queryStrings(t, db, "SELECT toString(tuple(*)) FROM ("+query+"\n)")
		got := queryStrings(t, db, "SELECT toString(tuple(*)) FROM ("+stripComments(query)+"\n)")
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%q: stripped query returned %v, the query returned %v", query, got, want)
		}
	}
}

func TestTxRollbackDiscardsBatch(t *testing.T) {
	db := openSessionDB(t)
	if _, err := db.Exec("CREATE TABLE TestTxRollback (id UInt64) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE TestTxRollback") })

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %s", err)
	}
	st, err := tx.Prepare("INSERT INTO TestTxRollback")
	if err != nil {
		t.Fatalf("prepare: %s", err)
	}
	if _, err := st.Exec(uint64(1)); err != nil {
		t.Fatalf("exec: %s", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %s", err)
	}
	if n := countRows(t, db, "TestTxRollback"); n != 0 {
		t.Fatalf("rolled-back rows were inserted: %d", n)
	}
	if _, err := st.Exec(uint64(2)); err == nil {
		t.Fatalf("exec after rollback should fail")
	}
}

// Statements that are not a batchable INSERT run immediately inside a
// transaction, and Commit/Rollback do not touch them.
func TestTxNonBatchStatementsRunImmediately(t *testing.T) {
	db := openSessionDB(t)
	if _, err := db.Exec("CREATE TABLE TestTxImmediate (id UInt64) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE TestTxImmediate") })

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %s", err)
	}
	if _, err := tx.Exec("INSERT INTO TestTxImmediate VALUES (?)", uint64(1)); err != nil {
		t.Fatalf("tx exec: %s", err)
	}
	sel, err := tx.Prepare("INSERT INTO TestTxImmediate SELECT number + 10 FROM numbers(2)")
	if err != nil {
		t.Fatalf("prepare: %s", err)
	}
	if _, err := sel.Exec(); err != nil {
		t.Fatalf("exec insert-select: %s", err)
	}
	var n uint64
	if err := tx.QueryRow("SELECT count() FROM TestTxImmediate").Scan(&n); err != nil || n != 3 {
		t.Fatalf("count inside tx: n=%d err=%v", n, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %s", err)
	}
	assertUint64s(t, queryUint64s(t, db, "SELECT id FROM TestTxImmediate ORDER BY id"), []uint64{1, 10, 11})
}

func TestBeginTxRejectsUnsupportedOptions(t *testing.T) {
	db := openSessionDB(t)
	for _, opts := range []*sql.TxOptions{
		{Isolation: sql.LevelSerializable},
		{ReadOnly: true},
	} {
		if tx, err := db.BeginTx(context.Background(), opts); err == nil {
			tx.Rollback()
			t.Fatalf("BeginTx(%+v) should fail", *opts)
		}
	}
}

func TestTxEndsWhenConnectionCloses(t *testing.T) {
	cn, err := Driver{}.Open(fmt.Sprintf("session=%s", session.ConnStr()))
	if err != nil {
		t.Fatal(err)
	}
	c := cn.(*conn)
	txn, err := c.Begin()
	if err != nil {
		t.Fatal(err)
	}
	st, err := c.Prepare("INSERT INTO TestTxEndsWhenConnectionCloses")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Exec([]driver.Value{int64(1)}); err == nil {
		t.Fatalf("exec on a batch whose connection closed should fail")
	}
	if err := txn.Commit(); err == nil {
		t.Fatalf("commit after the connection closed should fail")
	}
	if err := txn.Rollback(); err != nil {
		t.Fatalf("rollback after the connection closed: %s", err)
	}
}

func createUint64Table(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	if _, err := db.Exec("CREATE TABLE " + name + " (id UInt64) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE " + name) })
}

// An argument without a placeholder used to be dropped without an error, so
// the row was inserted with the remaining values.
func TestArgumentCountMustMatchPlaceholders(t *testing.T) {
	db := openSessionDB(t)
	createUint64Table(t, db, "TestArgumentCount")

	st, err := db.Prepare("INSERT INTO TestArgumentCount VALUES (?)")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Exec(uint64(1), uint64(2)); err == nil {
		t.Errorf("prepared Exec with a surplus argument should fail")
	}
	if _, err := st.Exec(); err == nil {
		t.Errorf("prepared Exec with a missing argument should fail")
	}
	if _, err := db.Exec("INSERT INTO TestArgumentCount VALUES (?)", uint64(3), uint64(4)); err == nil {
		t.Errorf("db.Exec with a surplus argument should fail")
	}
	if _, err := db.Exec("INSERT INTO TestArgumentCount VALUES (1)", uint64(5)); err == nil {
		t.Errorf("db.Exec with an argument and no placeholder should fail")
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Stmt(st).Exec(uint64(6), uint64(7)); err == nil {
		t.Errorf("batched Exec with a surplus argument should fail")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, "TestArgumentCount"); n != 0 {
		t.Fatalf("calls with the wrong number of arguments inserted %d rows", n)
	}
}

// A ? inside a string, a quoted identifier or a comment is not a placeholder.
func TestPlaceholdersIgnoreQuotesAndComments(t *testing.T) {
	db := openSessionDB(t)
	for _, tc := range []struct {
		query string
		args  []any
		want  string
	}{
		{"SELECT concat('?', ?, '\\'?')", []any{"x"}, "?x'?"},
		{"SELECT ? AS `col?` -- trailing ? comment\n", []any{"a"}, "a"},
		{"SELECT /* ? /* nested ? */ ? */ ?", []any{"b"}, "b"},
		{"SELECT concat(?, \"?\") FROM (SELECT 'z' AS `?`)", []any{"c"}, "cz"},
		{"SELECT ? # hash ? comment\n", []any{"d"}, "d"},
		{"SELECT ? #! shebang ? comment\n", []any{"e"}, "e"},
		{"SELECT ? // slash ? comment\n", []any{"f"}, "f"},
		{"SELECT concat($h$?/*keep*/'$h$, $$?$$, ?)", []any{"g"}, "?/*keep*/'?g"},
		{"SELECT concat(‘?’, ?) AS “?”", []any{"h"}, "?h"},
		{"SELECT concat(x'3F', toString(0x1e--?\n), ?)", []any{"i"}, "?30i"},
	} {
		var got string
		if err := db.QueryRow(tc.query, tc.args...).Scan(&got); err != nil {
			t.Errorf("%q: %v", tc.query, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.query, got, tc.want)
		}
	}
}

// tx.Stmt on a statement prepared on the same connection reuses the driver
// statement database/sql cached there, so the batch decision cannot be made
// when the statement is prepared.
func TestTxStmtOnSameConnectionIsBatched(t *testing.T) {
	db := openSessionDB(t)
	db.SetMaxOpenConns(1)
	createUint64Table(t, db, "TestTxStmtSameConn")

	st, err := db.Prepare("INSERT INTO TestTxStmtSameConn VALUES (?)")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Stmt(st).Exec(uint64(1)); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, "TestTxStmtSameConn"); n != 0 {
		t.Fatalf("rolled-back row was inserted: %d rows", n)
	}

	if _, err := st.Exec(uint64(2)); err != nil {
		t.Fatalf("exec outside a transaction: %v", err)
	}
	assertUint64s(t, queryUint64s(t, db, "SELECT id FROM TestTxStmtSameConn ORDER BY id"), []uint64{2})
}

// tx.Stmt on a statement prepared on another connection prepares it again on
// the transaction's connection and caches the result on the *sql.Stmt, so the
// same driver statement is used outside the transaction afterwards.
func TestTxStmtPreparedOnOtherConnectionIsReusableAfterTx(t *testing.T) {
	ctx := context.Background()
	db := openSessionDB(t)
	createUint64Table(t, db, "TestTxStmtOtherConn")

	st, err := db.Prepare("INSERT INTO TestTxStmtOtherConn VALUES (?)")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// The pool hands out the most recently freed connection first: pin the one
	// st was prepared on so the transaction gets a new one.
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Stmt(st).Exec(uint64(1)); err != nil {
		t.Fatalf("exec in tx: %v", err)
	}
	// Free the pinned connection first so the transaction's connection is the
	// one handed out next.
	pinned.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := st.Exec(uint64(2)); err != nil {
		t.Fatalf("exec after the transaction: %v", err)
	}
	assertUint64s(t, queryUint64s(t, db, "SELECT id FROM TestTxStmtOtherConn ORDER BY id"), []uint64{1, 2})
}

// A bare INSERT INTO t prepared outside a transaction inserts one row per Exec.
func TestBareInsertStatementOutsideTx(t *testing.T) {
	db := openSessionDB(t)
	createUint64Table(t, db, "TestBareInsertOutsideTx")

	st, err := db.Prepare("INSERT INTO TestBareInsertOutsideTx")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, v := range []uint64{1, 2} {
		if _, err := st.Exec(v); err != nil {
			t.Fatalf("exec %d: %v", v, err)
		}
	}
	assertUint64s(t, queryUint64s(t, db, "SELECT id FROM TestBareInsertOutsideTx ORDER BY id"), []uint64{1, 2})
}
