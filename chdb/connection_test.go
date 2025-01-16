package chdb

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNewconnection tests the creation of a new connection.
func TestNewConnection(t *testing.T) {
	connection, err := NewConnection()
	if err != nil {
		t.Fatalf("Failed to create new connection: %s", err)
	}
	defer connection.Cleanup()

	// Check if the connection directory exists
	if _, err := os.Stat(connection.Path()); os.IsNotExist(err) {
		t.Errorf("connection directory does not exist: %s", connection.Path())
	}

	// Check if the connection is temporary
	if !connection.IsTemp() {
		t.Errorf("Expected connection to be temporary")
	}
}

// TestconnectionClose tests the Close method of the connection.
func TestConnectionClose(t *testing.T) {
	connection, _ := NewConnection()
	defer connection.Cleanup() // Cleanup in case Close fails

	// Close the connection
	connection.Close()

	// Check if the connection directory has been removed
	if _, err := os.Stat(connection.Path()); !os.IsNotExist(err) {
		t.Errorf("connection directory should be removed after Close: %s", connection.Path())
	}
}

// TestconnectionCleanup tests the Cleanup method of the connection.
func TestConnectionCleanup(t *testing.T) {
	connection, _ := NewConnection()

	// Cleanup the connection
	connection.Cleanup()

	// Check if the connection directory has been removed
	if _, err := os.Stat(connection.Path()); !os.IsNotExist(err) {
		t.Errorf("connection directory should be removed after Cleanup: %s", connection.Path())
	}
}

// TestQuery tests the Query method of the connection.
func TestQueryOnConnection(t *testing.T) {
	path := filepath.Join(os.TempDir(), "chdb_test")
	defer os.RemoveAll(path)
	connection, _ := NewConnection(path)
	defer connection.Cleanup()

	connection.Query("CREATE DATABASE IF NOT EXISTS testdb; " +
		"CREATE TABLE IF NOT EXISTS testdb.testtable (id UInt32) ENGINE = MergeTree() ORDER BY id;")

	connection.Query(" INSERT INTO testdb.testtable VALUES (1), (2), (3);")

	ret, err := connection.Query("SELECT * FROM testtable;")
	if err != nil {
		t.Errorf("Query failed: %s", err)
	}
	t.Errorf("result is: %s", string(ret.Buf()))
	if string(ret.Buf()) != "1\n2\n3\n" {
		t.Errorf("Query result should be 1\n2\n3\n, got %s", string(ret.Buf()))
	}
}

func TestQueryOnConnection2(t *testing.T) {
	path := filepath.Join(os.TempDir(), "chdb_test")
	defer os.RemoveAll(path)
	connection, _ := NewConnection(path)
	defer connection.Cleanup()

	ret, err := connection.Query("SELECT number+1 from system.numbers limit 3")
	if err != nil {
		t.Errorf("Query failed: %s", err)
	}
	if string(ret.Buf()) != "1\n2\n3\n" {
		t.Errorf("Query result should be 1\n2\n3\n, got %s", string(ret.Buf()))
	}
}

func TestConnectionPathAndIsTemp(t *testing.T) {
	// Create a new connection and check its Path and IsTemp
	connection, _ := NewConnection()
	defer connection.Cleanup()

	if connection.Path() == "" {
		t.Errorf("connection path should not be empty")
	}

	if !connection.IsTemp() {
		t.Errorf("connection should be temporary")
	}

	// Create a new connection with a specific path and check its Path and IsTemp
	path := filepath.Join(os.TempDir(), "chdb_test2")
	defer os.RemoveAll(path)
	connection, _ = NewConnection(path)
	defer connection.Cleanup()

	if connection.Path() != path {
		t.Errorf("connection path should be %s, got %s", path, connection.Path())
	}

	if connection.IsTemp() {
		t.Errorf("connection should not be temporary")
	}
}
