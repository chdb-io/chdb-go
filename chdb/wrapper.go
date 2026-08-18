package chdb

import (
	"runtime"
	"sync"

	chdbpurego "github.com/chdb-io/chdb-go/v2/chdb-purego"
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
//
// The returned stream owns the session it runs on, because a streaming result is
// computed as it is read: every chunk is fetched through the connection that
// started the query. The session is closed when the stream reaches its end, or
// when Free/Cancel is called — so a caller that stops reading early must call
// one of them (a dropped stream is closed by a finalizer, eventually).
func QueryStream(queryStr string, outputFormats ...string) (result chdbpurego.ChdbStreamResult, err error) {
	outputFormat := "CSV" // Default value
	if len(outputFormats) > 0 {
		outputFormat = outputFormats[0]
	}
	sess, err := ephemeralSession()
	if err != nil {
		return nil, err
	}
	stream, err := sess.QueryStream(queryStr, outputFormat)
	if err != nil {
		sess.Close()
		return nil, err
	}
	return newSessionStream(stream, sess), nil
}

// sessionStream keeps the session that QueryStream opened alive for as long as
// the stream it produced can still be read.
//
// Closing the session as soon as QueryStream returned — what this used to do —
// handed back a stream that reads through a freed connection: the stream handle
// is only a cursor, and the engine state it points at belongs to the connection.
// The symptom followed the engine rather than chdb-go, which is what made it
// easy to miss: chdb-core v26.5 answered the first fetch with "Unexpected null
// connection", v26.7 segfaulted.
type sessionStream struct {
	// mu serializes release against the reads, so a Free racing the last GetNext
	// cannot close the session mid-fetch.
	mu       sync.Mutex
	stream   chdbpurego.ChdbStreamResult
	sess     *Session
	err      error // stream-level error, read before the stream is released
	released bool
}

func newSessionStream(stream chdbpurego.ChdbStreamResult, sess *Session) *sessionStream {
	s := &sessionStream{stream: stream, sess: sess}
	// Backstop for a caller that abandons the stream without draining it: the
	// session holds a native connection and pins the process-wide data path, so
	// leaking it would also keep a later session on another path from opening.
	runtime.SetFinalizer(s, (*sessionStream).release)
	return s
}

// GetNext implements chdbpurego.ChdbStreamResult.
func (s *sessionStream) GetNext() chdbpurego.ChdbResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return nil
	}
	chunk := s.stream.GetNext()
	if chunk == nil {
		// End of data: nothing more will be fetched through the connection, so
		// release the session now rather than waiting for a Free the caller has
		// no reason to make.
		s.releaseLocked()
	}
	return chunk
}

// Error implements chdbpurego.ChdbStreamResult.
func (s *sessionStream) Error() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return s.err
	}
	return s.stream.Error()
}

// Free implements chdbpurego.ChdbStreamResult.
func (s *sessionStream) Free() { s.release() }

// Cancel implements chdbpurego.ChdbStreamResult.
func (s *sessionStream) Cancel() { s.release() }

func (s *sessionStream) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseLocked()
}

// releaseLocked frees the stream first and only then closes the session: the
// stream handle is released through the connection that produced it, so the
// order matters.
func (s *sessionStream) releaseLocked() {
	if s.released {
		return
	}
	s.released = true
	s.err = s.stream.Error()
	s.stream.Free()
	s.sess.Close()
	runtime.SetFinalizer(s, nil)
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
		// The in-memory open may have lost a race with a path-based session
		// opened concurrently between the check above and this connect (chDB
		// allows only one data path per process). Only in that case retry by
		// attaching to whatever path is now active; otherwise the failure is
		// unrelated (e.g. a native connection error) and must be surfaced
		// rather than masked by the retry.
		sessMu.Lock()
		conflict := activeRefs > 0
		sessMu.Unlock()
		if !conflict {
			return nil, err
		}
		return NewSession()
	}
	return sess, nil
}

func initConnection(connStr string) (result chdbpurego.ChdbConn, err error) {
	return chdbpurego.NewConnectionFromConnString(connStr)
}
