package chdb

import (
	"fmt"
	"os"
	"testing"
)

// TestMain stops the engine once the package's tests are done.
//
// Two things come out of that. The engine gets an orderly stop before Go's own
// exit path starts, which is what the -race step used to need a wrapper for —
// .github/scripts/race-test.sh, deleted along with the need for it. And a
// session the tests forgot to close fails the run, at the one moment when
// "how many sessions are open" has an unambiguous right answer: zero. The
// second is the load-bearing half — leaving a connection open until exit is
// what actually crashed the chdb/driver tests.
//
// A refusal from the engine itself fails the run too. Up to chdb-core
// v26.11.0-rc.1 the engine stopped joining threads at all once a MergeTree
// table had been created, so a refusal said nothing about this package and was
// only reported. From v26.11.0-rc.2 it stops those threads as well, and a
// refusal means a chDB thread really could not be stopped.
func TestMain(m *testing.M) {
	code := m.Run()
	if err := Shutdown(); err != nil {
		fmt.Println("engine shutdown failed:", err)
		os.Exit(1)
	}
	os.Exit(code)
}
