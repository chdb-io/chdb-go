package durable

import (
	"errors"
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
// run. A refusal from the engine itself is reported instead: chdb-core v26.7.3
// stops joining threads at all once a MergeTree table has been created, which
// nothing here can affect.
func TestMain(m *testing.M) {
	code := m.Run()
	if err := chdb.Shutdown(); err != nil {
		if errors.Is(err, chdb.ErrSessionsOpen) {
			fmt.Println("engine shutdown failed:", err)
			os.Exit(1)
		}
		fmt.Println("engine shutdown incomplete:", err)
	}
	os.Exit(code)
}
