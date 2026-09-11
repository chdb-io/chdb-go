package chdbpurego

import "testing"

// Reading the version maps the library but starts no engine, so Shutdown has
// nothing to stop and must not put the process into the terminal state. The
// regression this guards against was silent: Shutdown returned nil and every
// later connect failed for the rest of the process.
func TestShutdownIsANoOpWhenNothingWasConnected(t *testing.T) {
	if _, err := Version(); err != nil {
		t.Skipf("libchdb did not load: %v", err)
	}
	if !engineLoaded.Load() {
		t.Fatal("Version() should have loaded the library")
	}
	if engineStarted.Load() {
		t.Skip("a connection was opened earlier in this process; cannot test the cold path")
	}

	if err := Shutdown(); err != nil {
		t.Fatalf("Shutdown() with nothing connected: %v", err)
	}

	// The engine must still be usable: this is the whole point.
	conn, err := NewConnectionFromConnString(":memory:")
	if err != nil {
		t.Fatalf("NewConnection after a no-op Shutdown: %v", err)
	}
	defer conn.Close()
	if !engineStarted.Load() {
		t.Fatal("a successful connect should record that the engine started")
	}
}
