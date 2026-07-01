package chdbdriver

import (
	"database/sql"
	"testing"

	"github.com/chdb-io/chdb-go/v2/chdb"
)

// TestDriverOpenNoKeeperLeak verifies that the legacy Driver.Open path does not
// leak the connector's keeper session. database/sql owns the connector on the
// sql.Open path and calls connector.Close(), but a direct Driver.Open(name)
// discards the connector, so closing the returned conn must release the keeper
// too. Pre-fix the keeper session (a native connection + a registry refcount)
// leaks on every Open call.
func TestDriverOpenNoKeeperLeak(t *testing.T) {
	baseline := chdb.ActiveSessionRefs()

	c, err := Driver{}.Open("session=" + session.ConnStr())
	if err != nil {
		t.Fatalf("Driver.Open failed: %s", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("conn.Close failed: %s", err)
	}

	if got := chdb.ActiveSessionRefs(); got != baseline {
		t.Fatalf("Driver.Open leaked sessions: refs=%d, baseline=%d (keeper not released on conn.Close)", got, baseline)
	}
}

// TestDbCloseReleasesRefs verifies the database/sql path balances refcounts:
// opening a *sql.DB, running queries across multiple pooled connections, and
// closing it returns the registry to its baseline (keeper + per-conn sessions
// all released).
func TestDbCloseReleasesRefs(t *testing.T) {
	baseline := chdb.ActiveSessionRefs()

	db, err := sql.Open("chdb", "session="+session.ConnStr())
	if err != nil {
		t.Fatalf("open db failed: %s", err)
	}
	db.SetMaxOpenConns(4)

	for i := 0; i < 8; i++ {
		var n int
		if err := db.QueryRow("SELECT count() FROM numbers(10)").Scan(&n); err != nil {
			t.Fatalf("query failed: %s", err)
		}
		if n != 10 {
			t.Fatalf("got %d want 10", n)
		}
	}

	if err := db.Close(); err != nil {
		t.Fatalf("db.Close failed: %s", err)
	}
	if got := chdb.ActiveSessionRefs(); got != baseline {
		t.Fatalf("db.Close left refs leaked: refs=%d, baseline=%d", got, baseline)
	}
}
