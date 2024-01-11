package chdb

import (
	"os"
	"path/filepath"
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

// TestQuery tests the Query method of the session.
func TestQuery(t *testing.T) {
	path := filepath.Join(os.TempDir(), "chdb_test")
	defer os.RemoveAll(path)
	session, _ := NewSession(path)
	defer session.Cleanup()

	session.Query("CREATE DATABASE IF NOT EXISTS testdb; " +
		"CREATE TABLE IF NOT EXISTS testdb.testtable (id UInt32) ENGINE = MergeTree() ORDER BY id;")

	session.Query("USE testdb; INSERT INTO testtable VALUES (1), (2), (3);")

	ret, err := session.Query("SELECT * FROM testtable;")
	if err != nil {
		t.Errorf("Query failed: %s", err)
	}
	if string(ret.Buf()) != "1\n2\n3\n" {
		t.Errorf("Query result should be 1\n2\n3\n, got %s", string(ret.Buf()))
	}
}

func TestSessionPathAndIsTemp(t *testing.T) {
	// Create a new session and check its Path and IsTemp
	session, _ := NewSession()
	defer session.Cleanup()

	if session.Path() == "" {
		t.Errorf("Session path should not be empty")
	}

	if !session.IsTemp() {
		t.Errorf("Session should be temporary")
	}

	// Create a new session with a specific path and check its Path and IsTemp
	path := filepath.Join(os.TempDir(), "chdb_test2")
	defer os.RemoveAll(path)
	session, _ = NewSession(path)
	defer session.Cleanup()

	if session.Path() != path {
		t.Errorf("Session path should be %s, got %s", path, session.Path())
	}

	if session.IsTemp() {
		t.Errorf("Session should not be temporary")
	}
}
