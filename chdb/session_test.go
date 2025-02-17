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

// This test is currently flaky because of this: https://github.com/chdb-io/chdb/pull/299/commits/91b0aedd8c17e74a4bb213e885d89cc9a77c99ad
// func TestQuery(t *testing.T) {

// 	session, _ := NewSession()
// 	defer session.Cleanup()
// 	// time.Sleep(time.Second * 5)

// 	_, err := session.Query("CREATE TABLE IF NOT EXISTS TestQuery (id UInt32) ENGINE = MergeTree() ORDER BY id;")
// 	if err != nil {
// 		t.Fatal(err)
// 	}

// 	_, err = session.Query("INSERT INTO TestQuery VALUES (1), (2), (3);")
// 	if err != nil {
// 		t.Fatal(err)
// 	}
// 	ret, err := session.Query("SELECT * FROM TestQuery;")
// 	if err != nil {
// 		t.Fatalf("Query failed: %s", err)
// 	}

// 	if string(ret.Buf()) != "1\n2\n3\n" {
// 		t.Fatalf("Query result should be 1\n2\n3\n, got %s", string(ret.Buf()))
// 	}
// }

func TestSessionPathAndIsTemp(t *testing.T) {
	// Create a new session and check its Path and IsTemp
	session, _ := NewSession()

	if session.Path() == "" {
		t.Errorf("Session path should not be empty")
	}

	if !session.IsTemp() {
		t.Errorf("Session should be temporary")
	}
	session.Close()

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
