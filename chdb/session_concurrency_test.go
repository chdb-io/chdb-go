package chdb

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// requireNoActiveSessions fails the test if the registry did not return to a
// clean state, catching refcount/temp-dir leaks between tests.
func requireNoActiveSessions(t *testing.T) {
	t.Helper()
	if n := ActiveSessionRefs(); n != 0 {
		t.Fatalf("expected 0 active session refs at start, got %d (leak from a previous test)", n)
	}
}

// TestSessionSamePathDifferentSpelling verifies that two sessions naming the
// SAME physical directory with different connection-string spellings (plain
// path vs the documented "file:" prefix) are recognized as the same data path
// and are allowed to share the engine, instead of being rejected with a
// spurious "conflicting path" error.
//
// Pre-fix this fails: resolvePathKey returned "file:..." verbatim while the
// native layer resolved both to the same absolute directory, so the keys
// diverged and the second NewSession was rejected.
func TestSessionSamePathDifferentSpelling(t *testing.T) {
	requireNoActiveSessions(t)
	dir := filepath.Join(t.TempDir(), "db")

	s1, err := NewSession(dir) // absolute path
	if err != nil {
		t.Fatalf("NewSession(%q) failed: %s", dir, err)
	}
	defer s1.Close()

	s2, err := NewSession("file:" + dir) // same directory, file: prefix
	if err != nil {
		t.Fatalf("NewSession(file:%s) on the same physical path was wrongly rejected: %s", dir, err)
	}
	defer s2.Close()

	if s1.Path() != s2.Path() {
		t.Fatalf("sessions should resolve to the same physical path, got %q and %q", s1.Path(), s2.Path())
	}
}

// TestSessionMemorySpellings verifies ":memory:" and "file::memory:" map to the
// same in-memory target and do not spuriously conflict.
func TestSessionMemorySpellings(t *testing.T) {
	requireNoActiveSessions(t)
	s1, err := NewSession(":memory:")
	if err != nil {
		t.Fatalf("NewSession(:memory:) failed: %s", err)
	}
	defer s1.Close()

	s2, err := NewSession("file::memory:")
	if err != nil {
		t.Fatalf("NewSession(file::memory:) on the same in-memory target was wrongly rejected: %s", err)
	}
	defer s2.Close()
}

// TestSessionCleanupRemovesResolvedDir verifies Cleanup() deletes the directory
// libchdb actually opened, even when the session was created with a "file:"
// connection string. Pre-fix Cleanup ran os.RemoveAll on the verbatim DSN
// ("file:<dir>"), leaving the real data directory on disk.
func TestSessionCleanupRemovesResolvedDir(t *testing.T) {
	requireNoActiveSessions(t)
	dir := filepath.Join(t.TempDir(), "data")

	s, err := NewSession("file:" + dir)
	if err != nil {
		t.Fatalf("NewSession failed: %s", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("expected data directory %q to exist after open: %s", dir, err)
	}
	s.Cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Cleanup should have removed the resolved data directory %q, stat err=%v", dir, err)
	}
}

// TestCleanupDoesNotRemoveSharedDir verifies that calling Cleanup() on one
// session does NOT delete the shared data directory while a sibling session on
// the same path is still open and usable.
//
// Pre-fix this fails: Cleanup ran os.RemoveAll(s.path) unconditionally, wiping
// the directory out from under the live sibling.
func TestCleanupDoesNotRemoveSharedDir(t *testing.T) {
	requireNoActiveSessions(t)

	s1, err := NewSession()
	if err != nil {
		t.Fatalf("NewSession failed: %s", err)
	}
	defer s1.Close()

	s2, err := NewSession() // reuses s1's (temp) path
	if err != nil {
		t.Fatalf("second NewSession failed: %s", err)
	}
	defer s2.Close()

	if s1.Path() != s2.Path() {
		t.Fatalf("expected shared path, got %q and %q", s1.Path(), s2.Path())
	}

	// Cleanup the first session while the second is still open.
	s1.Cleanup()

	// The shared directory must still exist and s2 must still be usable.
	if _, err := os.Stat(s2.Path()); err != nil {
		t.Fatalf("shared data dir %q was removed while a sibling session was open: %v", s2.Path(), err)
	}
	if _, err := s2.Query("SELECT 1"); err != nil {
		t.Fatalf("sibling session broke after the other session's Cleanup: %s", err)
	}
}

// TestRefcountedTempCleanup verifies the temp directory survives until the LAST
// session on it closes, and is removed afterwards.
func TestRefcountedTempCleanup(t *testing.T) {
	requireNoActiveSessions(t)

	s1, err := NewSession()
	if err != nil {
		t.Fatalf("NewSession failed: %s", err)
	}
	path := s1.Path()
	s2, err := NewSession()
	if err != nil {
		t.Fatalf("second NewSession failed: %s", err)
	}
	if ActiveSessionRefs() != 2 {
		t.Fatalf("expected 2 active refs, got %d", ActiveSessionRefs())
	}

	s1.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("temp dir %q removed after first close but a session is still open: %v", path, err)
	}
	if ActiveSessionRefs() != 1 {
		t.Fatalf("expected 1 active ref after first close, got %d", ActiveSessionRefs())
	}

	s2.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temp dir %q should be removed after the last close, stat err=%v", path, err)
	}
	if ActiveSessionRefs() != 0 {
		t.Fatalf("expected 0 active refs after last close, got %d", ActiveSessionRefs())
	}
}

// TestConflictThenRecover verifies that after a conflicting-path error the
// registry is left intact: the original session keeps working and a subsequent
// same-path session still succeeds.
func TestConflictThenRecover(t *testing.T) {
	requireNoActiveSessions(t)

	first, err := NewSession()
	if err != nil {
		t.Fatalf("NewSession failed: %s", err)
	}
	defer first.Close()

	other := filepath.Join(t.TempDir(), "other")
	if _, err := NewSession(other); err == nil {
		t.Fatalf("expected conflict error opening a different path")
	}
	// Registry must be intact: original still works, refcount unchanged.
	if ActiveSessionRefs() != 1 {
		t.Fatalf("conflict error perturbed the refcount, got %d want 1", ActiveSessionRefs())
	}
	if _, err := first.Query("SELECT 1"); err != nil {
		t.Fatalf("original session broke after a conflict error: %s", err)
	}
	// A same-path session must still succeed.
	s2, err := NewSession()
	if err != nil {
		t.Fatalf("same-path NewSession after conflict failed: %s", err)
	}
	s2.Close()
}

// TestSessionQueryAfterClose verifies a Query on a closed session returns a
// clean error instead of dereferencing a freed native connection.
//
// Pre-fix this crashes (use-after-free on the freed native connection).
func TestSessionQueryAfterClose(t *testing.T) {
	requireNoActiveSessions(t)

	s, err := NewSession()
	if err != nil {
		t.Fatalf("NewSession failed: %s", err)
	}
	s.Close()

	if _, err := s.Query("SELECT 1"); err == nil {
		t.Fatalf("expected an error querying a closed session, got nil")
	}
}

// TestSessionConcurrentQueryClose hammers a single session with concurrent
// queries while another goroutine closes it. With the per-session guard in
// place this must not crash or trip the race detector; queries either succeed
// or return a clean "closed" error.
//
// Pre-fix this is a use-after-free / data race between chdb_query and
// chdb_close_conn on the same connection.
func TestSessionConcurrentQueryClose(t *testing.T) {
	requireNoActiveSessions(t)

	s, err := NewSession()
	if err != nil {
		t.Fatalf("NewSession failed: %s", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				// Ignore errors: a "closed" error is an acceptable outcome.
				if _, err := s.Query("SELECT 1"); err != nil {
					return
				}
			}
		}()
	}

	// Close concurrently with the in-flight queries.
	s.Close()
	wg.Wait()

	// Idempotent close must be safe too.
	s.Close()
	s.Cleanup()
}
