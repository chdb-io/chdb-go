package chdb

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

// TestMain stops the engine once the package's tests are done.
//
// Two things come out of that. The engine gets an orderly stop before Go's own
// exit path starts, which is what the -race step used to need
// .github/scripts/race-test.sh for. And a session the tests forgot to close
// fails the run, at the one moment when "how many sessions are open" has an
// unambiguous right answer: zero. The second is the load-bearing half —
// leaving a connection open until exit is what actually crashed the
// chdb/driver tests.
//
// A refusal from the engine itself is reported rather than failed on. As of
// chdb-core v26.7.3 it stops joining threads at all once a MergeTree table has
// been created, which is a property of the engine and not of a test in this
// package.
func TestMain(m *testing.M) {
	code := m.Run()
	if err := Shutdown(); err != nil {
		if errors.Is(err, ErrSessionsOpen) {
			fmt.Println("engine shutdown failed:", err)
			os.Exit(1)
		}
		fmt.Println("engine shutdown incomplete:", err)
	}
	os.Exit(code)
}
