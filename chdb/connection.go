package chdb

import (
	"fmt"
	"os"

	"github.com/chdb-io/chdb-go/chdbstable"
)

type Connection struct {
	conn    *chdbstable.ChdbConn
	connStr string
	path    string
	isTemp  bool
}

// NewConnection creates a new connection with the given path.
// If path is empty, a temporary directory is created.
// Note: The temporary directory is removed when Close is called.
func NewConnection(paths ...string) (*Connection, error) {
	path := ""
	if len(paths) > 0 {
		path = paths[0]
	}
	isTemp := false
	if path == "" {
		// Create a temporary directory
		tempDir, err := os.MkdirTemp("", "chdb_")
		if err != nil {
			return nil, err
		}
		path = tempDir
		isTemp = true

	}
	connStr := fmt.Sprintf("file:%s/chdb.db", path)

	conn, err := initConnection(connStr)
	if err != nil {
		return nil, err
	}
	return &Connection{connStr: connStr, path: path, isTemp: isTemp, conn: conn}, nil
}

// Query calls queryToBuffer with a default output format of "CSV" if not provided.
func (s *Connection) Query(queryStr string, outputFormats ...string) (result *chdbstable.LocalResult, err error) {
	outputFormat := "CSV" // Default value
	if len(outputFormats) > 0 {
		outputFormat = outputFormats[0]
	}

	return connQueryToBuffer(s.conn, queryStr, outputFormat)
}

// Close closes the connection and removes the temporary directory
//
//	temporary directory is created when Newconnection was called with an empty path.
func (s *Connection) Close() {
	// Remove the temporary directory if it starts with "chdb_"
	s.conn.Close()
	if s.isTemp {
		s.Cleanup()
	}
}

// Cleanup closes the connection and removes the directory.
func (s *Connection) Cleanup() {
	// Remove the connection directory, no matter if it is temporary or not
	_ = os.RemoveAll(s.path)
}

// Path returns the path of the connection.
func (s *Connection) Path() string {
	return s.path
}

// IsTemp returns whether the connection is temporary.
func (s *Connection) IsTemp() bool {
	return s.isTemp
}
