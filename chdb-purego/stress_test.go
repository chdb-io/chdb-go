package chdbpurego

import (
	"bytes"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

// TestSignalHandlersPreservedAcrossConnect is the regression guard for issue
// #30. It snapshots the kernel sigaction state for SIGSEGV/SIGABRT/SIGBUS/
// SIGILL/SIGFPE/SIGURG before and after opening a chdb connection and fails
// if libchdb manages to overwrite any of them.
//
// Without the fix, libchdb's BaseDaemon code overwrites these handlers with
// its own crash-reporting code (and chdb_set_signal_handlers_enabled(0) makes
// it worse — it wipes the handlers to SIG_DFL as a side effect). Either case
// breaks Go's stack growth and panic recovery; under load it surfaces as the
// rare std::mutex::unlock crash reported on macOS arm64.
func TestSignalHandlersPreservedAcrossConnect(t *testing.T) {
	if !signalProtectionAvailable() {
		t.Skip("libchdb does not export chdb_set_signal_handlers_enabled; protection disabled on this build")
	}
	before := snapshotSignalHandlers()

	conn, err := NewConnectionFromConnString(":memory:")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	res, err := conn.Query("SELECT 1", "CSV")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if res != nil {
		res.Free()
	}
	conn.Close()

	after := snapshotSignalHandlers()

	// Only compare the handler pointer (first 8 bytes of struct sigaction
	// on every platform we target). The rest of the struct contains
	// sa_mask / sa_flags / sa_restorer which libchdb is free to touch via
	// sigprocmask without breaking Go.
	for i, sig := range signalsToProtect {
		if !bytes.Equal(before[i][:8], after[i][:8]) {
			t.Errorf("sig=%d: handler pointer changed across chdb_connect\n  before=% x\n  after =% x",
				sig, before[i][:8], after[i][:8])
		}
	}
}

// TestSignalHandlersRestoredAfterPanic models the failure mode flagged in PR
// review: if the section between snapshot and restore panics, the inline
// "restore" call would be skipped and the rest of the process would be left
// with the wiped sigaction state. Using `defer restoreSignalHandlers(saved)`
// makes the restore unconditional. This test wipes a handler, panics, and
// asserts that on unwinding the handler is back to what we snapshotted.
func TestSignalHandlersRestoredAfterPanic(t *testing.T) {
	if !signalProtectionAvailable() {
		t.Skip("libchdb does not export chdb_set_signal_handlers_enabled; protection disabled on this build")
	}
	initial := snapshotSignalHandlers()

	recovered := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				recovered = true
			}
		}()

		saved := snapshotSignalHandlers()
		defer restoreSignalHandlers(saved)

		// Simulate libchdb wiping every protected handler to SIG_DFL.
		var dflBuf [sigactionBufSize]byte // zeroed = SIG_DFL on every POSIX
		for _, sig := range signalsToProtect {
			libcSigaction(sig, unsafe.Pointer(&dflBuf[0]), nil)
		}

		// Confirm the simulated wipe took effect — otherwise the test is
		// vacuously passing.
		wiped := snapshotSignalHandlers()
		for i, sig := range signalsToProtect {
			if bytes.Equal(saved[i][:8], wiped[i][:8]) {
				t.Fatalf("sig=%d: simulated wipe didn't change handler; libcSigaction call may be broken", sig)
			}
		}

		panic("simulating a C++ exception propagating out of chdb_connect")
	}()

	if !recovered {
		t.Fatal("expected panic to be recovered by outer defer")
	}

	after := snapshotSignalHandlers()
	for i, sig := range signalsToProtect {
		if !bytes.Equal(initial[i][:8], after[i][:8]) {
			t.Errorf("sig=%d: handler NOT restored after panic\n  initial=% x\n  after  =% x",
				sig, initial[i][:8], after[i][:8])
		}
	}
}

// TestParallelQueriesStress runs many goroutines that each fire queries in a
// tight loop with varying call-stack depths. It is the regression guard for
// issue #30 (https://github.com/chdb-io/chdb-go/issues/30): the original crash
// at std::mutex::unlock is believed to come from libchdb installing
// process-wide signal handlers that clobber the Go runtime's own handlers for
// SIGSEGV (stack growth) and SIGURG (async preemption). Driving many parallel
// goroutines with frequent stack growth maximises the likelihood of an async
// preempt or stack-grow signal arriving while a query is in flight.
//
// Skipped under `go test -short`. By default runs for 5 s and uses 4×NumCPU
// goroutines; tunable via the flags below.
func TestParallelQueriesStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in -short mode")
	}

	const (
		duration  = 5 * time.Second
		gPerCPU   = 4
		recurseTo = 64 // deepen the Go stack to force growth events
	)

	conn, err := NewConnectionFromConnString(":memory:")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	var (
		wg       sync.WaitGroup
		queries  atomic.Uint64
		failures atomic.Uint64
		stop     atomic.Bool
	)

	workers := runtime.NumCPU() * gPerCPU
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runStress(conn, id, recurseTo, &queries, &failures, &stop)
		}(i)
	}

	time.Sleep(duration)
	stop.Store(true)
	wg.Wait()

	t.Logf("workers=%d duration=%s queries=%d failures=%d (%.0f qps)",
		workers, duration, queries.Load(), failures.Load(),
		float64(queries.Load())/duration.Seconds())

	if failures.Load() != 0 {
		t.Fatalf("%d queries failed under stress", failures.Load())
	}
}

// runStress recurses Go-side before issuing the query so each iteration walks a
// deep stack, increasing the chance that the runtime needs to grow the stack
// (which goes through a SIGSEGV on the guard page on most platforms).
func runStress(conn ChdbConn, id, depth int, queries, failures *atomic.Uint64, stop *atomic.Bool) {
	if depth > 0 {
		runStress(conn, id, depth-1, queries, failures, stop)
		return
	}
	for !stop.Load() {
		res, err := conn.Query("SELECT 1 + 1", "CSV")
		if err != nil {
			failures.Add(1)
			continue
		}
		if res != nil {
			res.Free()
		}
		queries.Add(1)
	}
}

// TestMultiConnectionStress opens several connections to the same (in-memory)
// data path and drives queries on all of them concurrently. It guards the
// multi-connection path (refcounted EmbeddedServer + per-connection clients)
// against crashes and regressions in the issue-#30 signal handling under
// concurrent connect/query load. Skipped under `go test -short`.
func TestMultiConnectionStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in -short mode")
	}

	const (
		duration = 5 * time.Second
		nConns   = 4
		gPerConn = 4
		recurse  = 16
	)

	conns := make([]ChdbConn, 0, nConns)
	for i := 0; i < nConns; i++ {
		c, err := NewConnectionFromConnString(":memory:")
		if err != nil {
			t.Fatalf("connect %d: %v", i, err)
		}
		defer c.Close()
		conns = append(conns, c)
	}

	var (
		wg       sync.WaitGroup
		queries  atomic.Uint64
		failures atomic.Uint64
		stop     atomic.Bool
	)

	for i := 0; i < nConns; i++ {
		for g := 0; g < gPerConn; g++ {
			wg.Add(1)
			go func(c ChdbConn) {
				defer wg.Done()
				runStress(c, 0, recurse, &queries, &failures, &stop)
			}(conns[i])
		}
	}

	time.Sleep(duration)
	stop.Store(true)
	wg.Wait()

	t.Logf("conns=%d goroutines=%d queries=%d failures=%d (%.0f qps)",
		nConns, nConns*gPerConn, queries.Load(), failures.Load(),
		float64(queries.Load())/duration.Seconds())

	if failures.Load() != 0 {
		t.Fatalf("%d queries failed under multi-connection stress", failures.Load())
	}
}
