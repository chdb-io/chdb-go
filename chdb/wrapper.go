package chdb

import (
	chdbpurego "github.com/chdb-io/chdb-go/chdb-purego"
)

// Query runs a one-shot query and returns the materialized result. Output
// format defaults to "CSV" when not provided.
//
// chDB allows only one data path per process, so if a session is already open
// this helper attaches to that session's data path; otherwise it uses an
// in-memory database.
func Query(queryStr string, outputFormats ...string) (result chdbpurego.ChdbResult, err error) {
	outputFormat := "CSV" // Default value
	if len(outputFormats) > 0 {
		outputFormat = outputFormats[0]
	}
	sess, err := ephemeralSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	return sess.Query(queryStr, outputFormat)
}

// QueryStream is like Query but returns a streaming result that can be read in
// chunks, for large datasets that should not be fully materialized in memory.
func QueryStream(queryStr string, outputFormats ...string) (result chdbpurego.ChdbStreamResult, err error) {
	outputFormat := "CSV" // Default value
	if len(outputFormats) > 0 {
		outputFormat = outputFormats[0]
	}
	sess, err := ephemeralSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	return sess.QueryStream(queryStr, outputFormat)
}

// ephemeralSession returns a short-lived session for the package-level one-shot
// helpers. It reuses the already-open data path when a session exists (chDB
// permits only one data path per process), and otherwise falls back to an
// in-memory database.
func ephemeralSession() (*Session, error) {
	sessMu.Lock()
	reuse := activeRefs > 0
	sessMu.Unlock()
	if reuse {
		return NewSession()
	}
	sess, err := NewSession(":memory:")
	if err != nil {
		// A path-based session may have opened concurrently between the check
		// and the connect; attach to whatever is now active.
		return NewSession()
	}
	return sess, nil
}

func initConnection(connStr string) (result chdbpurego.ChdbConn, err error) {
	return chdbpurego.NewConnectionFromConnString(connStr)
}
