package chdbpurego

import (
	"errors"
	"sync/atomic"
)

// Stopping the engine.
//
// libchdb starts process-global ClickHouse thread pools, and closing the last
// connection does not stop them. Measured on chdb-core v26.7.3, linux/arm64:
// a process at 6 threads reaches 19 once a session runs a SELECT, and is
// still at 19 after that session is closed. A process that just exits never
// notices — the kernel reaps them. A process that runs a teardown sequence of
// its own does: global destructors, a finalizing runtime, or a sanitizer exit
// handler all run while those threads can still wake and execute code.
//
// chdb_shutdown(), added in chdb-core v26.7.2-rc.2, joins them: 19 threads
// down to 10 in that same measurement.
//
// Its reach is narrower than its documentation suggests. Once a MergeTree
// table has been created it returns CHDBError for the rest of the process and
// joins nothing at all — 27 threads before the call and 27 after, on every
// attempt, with retries and delays making no difference. So on v26.7.3 a
// caller gets an orderly stop only if it never created a MergeTree table, and
// this belongs upstream in chdb-core rather than here.
//
// It is deliberately not called for the caller. The C ABI's contract is that
// shutdown is terminal: "once it starts, the library is closed for business
// for the rest of the process, whether or not it manages to stop every
// thread: chdb_connect() then fails". So there is no safe place for this
// package to trigger it on its own — "the last connection just closed" is
// exactly the state a connection pool reaches between two requests, and
// shutting down there would turn the next request into a permanent failure.
// Only the program knows it is finished with chDB, so only the program can
// say so.

// ErrShutdownUnavailable reports that the loaded libchdb predates
// chdb_shutdown. Its message names the release that introduced it, because
// the fix is always to move the engine.
var ErrShutdownUnavailable = errors.New(
	"chdb: the loaded libchdb does not export chdb_shutdown; it was added in " +
		"chdb-core v26.7.2-rc.2, so install or point CHDB_LIB_PATH at that release or later")

// engineLoaded records that bindSymbols has run: the library is mapped and its
// symbols are bound. Reading loadedPath instead would be a data race, since it
// is written inside loadOnce.Do and a caller that never goes through
// ensureLoaded never synchronizes with it.
var engineLoaded atomic.Bool

// engineStarted records that a connection was opened, which is a different
// question and the one Shutdown has to ask.
//
// Loading the library starts nothing. Version() and LoadedLibraryPath() map
// libchdb and read a compile-time constant; no engine exists afterwards and no
// thread has been created. Gating Shutdown on engineLoaded therefore let a
// program that only asked for the version put the process into the terminal
// state anyway: chdb_shutdown() succeeded, and every later NewConnection
// failed for the rest of the process, over an engine that had never run. A
// program that probes the version at startup and calls Shutdown from a
// defensive cleanup path would break itself that way.
var engineStarted atomic.Bool

// Shutdown stops the engine, joining the threads chDB started. Call it once,
// when the program is finished with chDB and before it starts tearing itself
// down. How much it manages to join is up to the engine; see above.
//
// Every connection must be closed and every result freed first. While one is
// still open the engine refuses, and this returns an error saying so.
//
// Shutdown is terminal. After it, this process cannot open another connection
// — NewConnection fails, and there is no way back short of restarting. That is
// the engine's contract, not this binding's choice.
//
// It never loads the library. A program that imported this package but never
// opened a connection has no engine to stop, and Shutdown returns nil rather
// than dlopen-ing several hundred megabytes in order to shut it down again.
//
// Calling it more than once is harmless: the engine reports success once it is
// already stopped. It is not worth retrying after an error, though — the
// engine documents a retry and does not perform one.
//
// Skipping it entirely is as safe as it has always been for a process that
// just exits — the threads are reaped by process exit. It matters when
// something runs after main: a sanitizer's exit handler, a C++ global
// destructor, a host runtime's finalizers.
func Shutdown() error {
	// Nothing was ever connected, so there is nothing to join and nothing to
	// gain from making the process terminal.
	if !engineStarted.Load() {
		return nil
	}
	if chdbShutdown == nil {
		return ErrShutdownUnavailable
	}
	// The same guard connect uses. chdb_shutdown is not documented to touch
	// the sigaction table, and with handlers disabled at load time it should
	// not — but "should not" is what the connect path assumed too, before the
	// first call turned out to reset five signals to SIG_DFL anyway (issue
	// #30). Snapshotting six handlers costs microseconds once per process.
	defer guardSignalHandlers()()
	if chdbShutdown() != 0 {
		// CHDBError means a connection is still open, or a thread would not
		// stop. The engine reports no detail beyond the code, and on v26.7.3
		// it has done nothing when it says this.
		return errors.New("chdb: chdb_shutdown() failed; either a connection is " +
			"still open or a chDB thread could not be stopped")
	}
	return nil
}

// ShutdownAvailable reports whether the loaded engine exports chdb_shutdown,
// without loading the library: false when nothing is loaded yet. A caller that
// wants to know before it depends on an orderly stop can ask.
func ShutdownAvailable() bool {
	return engineLoaded.Load() && chdbShutdown != nil
}
