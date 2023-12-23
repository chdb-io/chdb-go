package chdb

import (
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/chdb-io/chdb-go/chdbstable"
)

type Session struct {
	path string
	isTemp bool
}

// NewSession creates a new session with the given path.
// If path is empty, a temporary directory is created.
// Note: The temporary directory is removed when Close is called.
func NewSession(paths ...string) (*Session, error) {
	path := ""
	if len(paths) > 0 {
		path = paths[0]
	}

	if path == "" {
		// Create a temporary directory
		tempDir, err := ioutil.TempDir("", "chdb_")
		if err != nil {
			return nil, err
		}
		path = tempDir
		return &Session{path: path, isTemp: true}, nil
	}

	return &Session{path: path, isTemp: false}, nil
}

// Query calls queryToBuffer with a default output format of "CSV" if not provided.
func (s *Session) Query(queryStr string, outputFormats ...string) *chdbstable.LocalResult {
    outputFormat := "CSV" // Default value
    if len(outputFormats) > 0 {
        outputFormat = outputFormats[0]
    }
    return queryToBuffer(queryStr, outputFormat, s.path, "")
}

// Close closes the session and removes the temporary directory 
//  temporary directory is created when NewSession was called with an empty path.
func (s *Session) Close() {
	// Remove the temporary directory if it starts with "chdb_"
	if s.isTemp && filepath.Base(s.path)[:5] == "chdb_" {
		s.Cleanup()
	}
}

// Cleanup closes the session and removes the directory.
func (s *Session) Cleanup() {
	// Remove the session directory, no matter if it is temporary or not
	_ = os.RemoveAll(s.path)
}

// Path returns the path of the session.
func (s *Session) Path() string {
	return s.path
}

// IsTemp returns whether the session is temporary.
func (s *Session) IsTemp() bool {
	return s.isTemp
}
