package chdb

import (
	"errors"
	"testing"
)

// Shutdown's refusal path is the only part of it a test in this binary can
// exercise: the call is terminal, so an actual shutdown here would take the
// engine away from every test that runs after it. TestMain performs the real
// one, once, after everything else.
//
// The refusal is worth a test of its own because it is what catches a leaked
// session, and a leaked session is the failure mode this whole mechanism
// exists to surface. It also has to short-circuit before the engine is asked —
// the engine would refuse too, but with an error that says nothing about which
// caller still holds a connection.
func TestShutdownRefusesWhileSessionsAreOpen(t *testing.T) {
	sess, err := NewSession()
	if err != nil {
		t.Fatalf("could not open a session: %v", err)
	}
	defer func() {
		sess.Cleanup()
		sess.Close()
	}()

	err = Shutdown()
	if !errors.Is(err, ErrSessionsOpen) {
		t.Fatalf("Shutdown with a session open returned %v, want ErrSessionsOpen", err)
	}

	// Refusing has to leave the engine usable. If it had reached
	// chdb_shutdown, this query would fail and every later test with it.
	res, err := sess.Query("SELECT 1", "CSV")
	if err != nil {
		t.Fatalf("the session stopped working after a refused Shutdown: %v", err)
	}
	defer res.Free()
	if got := string(res.Buf()); got != "1\n" {
		t.Fatalf("query after a refused Shutdown returned %q, want \"1\\n\"", got)
	}
}
