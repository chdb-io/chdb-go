package chdb

import (
	"errors"
	"fmt"

	chdbpurego "github.com/chdb-io/chdb-go/v2/chdb-purego"
)

// ErrSessionsOpen reports that Shutdown was called while a Session was still
// open. It is separate from every other shutdown failure because it is the one
// the caller caused and the one the caller can fix: a Session that was never
// closed, or a *sql.DB still holding pooled connections. The engine's own
// refusals say nothing about which.
var ErrSessionsOpen = errors.New("chdb: a session is still open")

// Shutdown stops the engine, joining the threads chDB started. Call it once,
// when the program is finished with chDB, before it tears itself down.
//
// Why a program would bother: closing the last Session does not stop the
// engine's thread pools, and anything that runs after that — a sanitizer's
// exit handler, a C++ global destructor, a host runtime's finalizers — runs
// alongside threads that can still wake. Measured on chdb-core
// v26.11.0-rc.2, linux/amd64: 6 threads before the first session, 19 with one
// open, 22 after closing it, and 6 again after Shutdown.
//
// Every Session must be closed first. Shutdown refuses while one is open and
// returns an error wrapping ErrSessionsOpen, naming how many, rather than
// asking the engine and relaying its bare error code — the registry here
// already knows the answer and can say something actionable.
//
// The engine can also refuse on its own, and that is not the same thing: it
// means a chDB thread could not be stopped, which a caller can do nothing
// about, while ErrSessionsOpen means the program has a leak and can fix it.
// Up to chdb-core v26.11.0-rc.1 the engine refused for the rest of the process
// once a MergeTree table had been created, joining nothing at all; from
// v26.11.0-rc.2 it stops the MergeTree background threads, and jemalloc's, as
// well.
//
// Shutdown is terminal, and that is the engine's contract rather than this
// package's choice: after it, NewSession fails for the rest of the process.
// So this is not something to call between two units of work, and nothing in
// this package calls it for the caller — "the last Session just closed" is
// where a pool sits between requests, and shutting down there would break the
// next one permanently.
//
// A process that never opened a Session has no engine to stop, and Shutdown
// returns nil. Asking for the engine's version counts as never opening one:
// that maps the library and reads a constant, it starts no engine, and
// shutting one down that was never running would make the process terminal
// for nothing.
//
// Not calling it is as safe as it has always been for a process that simply
// exits: the threads are reaped by process exit. A binary built with -race is
// the exception, because on a clean exit it leaves through libc's exit(),
// which runs the engine's exit teardown while engine threads are still
// running, so exiting it with a Session open (MergeTree merges in flight
// especially) can crash; close every Session and call Shutdown first.
func Shutdown() error {
	// Held across the native call, so a NewSession arriving mid-shutdown
	// cannot slip a connection in behind the refcount check. The engine
	// serializes connect against shutdown itself, so that connect would be
	// refused rather than corrupt anything — but it would be refused with the
	// engine's error, in a caller that did nothing wrong, and the lock turns
	// that race into ordinary waiting.
	sessMu.Lock()
	defer sessMu.Unlock()
	if activeRefs > 0 {
		return fmt.Errorf("%w: cannot shut the engine down with %d still open; "+
			"close every Session first", ErrSessionsOpen, activeRefs)
	}
	return chdbpurego.Shutdown()
}
