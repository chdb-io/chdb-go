package chdb

import (
	"os"
	"testing"
)

// TestNewSession tests the creation of a new session.
func TestNewSession(t *testing.T) {
	session, err := NewSession()
	if err != nil {
		t.Fatalf("Failed to create new session: %s", err)
	}
	defer session.Cleanup()

	// Check if the session directory exists
	if _, err := os.Stat(session.Path()); os.IsNotExist(err) {
		t.Errorf("Session directory does not exist: %s", session.Path())
	}

	// Check if the session is temporary
	if !session.IsTemp() {
		t.Errorf("Expected session to be temporary")
	}
}

// TestSessionClose tests the Close method of the session.
func TestSessionClose(t *testing.T) {
	session, _ := NewSession()
	defer session.Cleanup() // Cleanup in case Close fails

	// Close the session
	session.Close()

	// Check if the session directory has been removed
	if _, err := os.Stat(session.Path()); !os.IsNotExist(err) {
		t.Errorf("Session directory should be removed after Close: %s", session.Path())
	}
}

// TestSessionCleanup tests the Cleanup method of the session.
func TestSessionCleanup(t *testing.T) {
	session, _ := NewSession()

	// Cleanup the session
	session.Cleanup()

	// Check if the session directory has been removed
	if _, err := os.Stat(session.Path()); !os.IsNotExist(err) {
		t.Errorf("Session directory should be removed after Cleanup: %s", session.Path())
	}
}
