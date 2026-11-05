package durable

import (
	"fmt"
	"os"
	"testing"

	"github.com/chdb-io/chdb-go/v2/chdb"
)

// TestMain stops the engine once the package's tests are done, for the reason
// chdb's own TestMain does: the engine should be given an orderly stop before
// Go's exit path starts, and a connection still open at exit is what makes
// `go test -race` segfault after every test has already reported PASS.
//
// Most tests here run against fakes and never touch the engine; the ones that
// do open a chdb.Session through ChdbEngine. Shutdown loads nothing when
// nothing was loaded, so the fake-only case costs nothing.
//
// A durable object that was not closed leaves its session open and fails the
// run. So does a refusal from the engine itself: up to chdb-core v26.11.0-rc.1
// the engine stopped joining threads at all once a MergeTree table had been
// created, and that was only reported, but from v26.11.0-rc.2 it stops those
// threads too, so a refusal means a chDB thread really could not be stopped.
func TestMain(m *testing.M) {
	code := m.Run()
	if err := chdb.Shutdown(); err != nil {
		fmt.Println("engine shutdown failed:", err)
		os.Exit(1)
	}
	os.Exit(code)
}
