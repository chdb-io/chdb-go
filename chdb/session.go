package chdb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	chdbpurego "github.com/chdb-io/chdb-go/chdb-purego"
)

// chDB embeds a single ClickHouse engine (EmbeddedServer) per process: the
// engine is a refcounted singleton bound to one data path, but it accepts
// multiple independent client connections that can run queries concurrently.
// The registry below mirrors that contract on the Go side:
//
//   - Multiple Sessions may be open at once, each holding its own native
//     connection (its own ClickHouse client), as long as they all share the
//     same data path. This is what enables real concurrency (e.g. via
//     database/sql with MaxOpenConns > 1).
//   - Opening a Session on a different path while another is still open
//     returns an error, because the engine allows only one data path per
//     process.
//   - A temp directory created for an empty-path Session is owned by the
//     registry and removed once the last Session on it is closed.
//
// Connection establishment is serialized by sessMu (it also guards the
// process-global signal-handler snapshot/restore done during connect); query
// execution is NOT serialized here and runs concurrently across Sessions.
var (
	sessMu     sync.Mutex
	activePath string // resolved path of the live engine; "" when none is open
	activeRefs int    // number of open Sessions on activePath
	activeTemp bool   // whether activePath is a temp dir owned by the registry
)

type Session struct {
	conn    chdbpurego.ChdbConn
	connStr string
	path    string
	isTemp  bool
	closed  bool
}

// resolvePathKey normalizes a requested path into a stable key used to detect
// whether two Sessions target the same data path. Plain filesystem paths are
// made absolute; ":memory:" and connection strings carrying a scheme or query
// params ("file:...", "...?param=val") are compared verbatim.
func resolvePathKey(p string) string {
	if p == "" || p == ":memory:" {
		return p
	}
	if strings.ContainsAny(p, ":?") {
		return p
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// NewSession creates a new session with the given path.
//
// If path is empty, the session reuses the data path of any already-open
// session, or creates a temporary directory when none is open. The temporary
// directory is removed when the last session using it is closed.
//
// Multiple sessions can be open concurrently as long as they share the same
// data path; each session owns an independent native connection, so they can
// execute queries in parallel. Opening a session on a different path while
// another is still open returns an error (chDB allows only one data path per
// process).
func NewSession(paths ...string) (*Session, error) {
	requested := ""
	if len(paths) > 0 {
		requested = paths[0]
	}

	sessMu.Lock()
	defer sessMu.Unlock()

	var path string
	var isTemp bool
	createdTemp := ""

	if requested == "" {
		if activeRefs > 0 {
			// Reuse the already-open data path so the new session attaches to
			// the same engine instead of trying to open a second path.
			path = activePath
			isTemp = activeTemp
		} else {
			tempDir, err := os.MkdirTemp("", "chdb_")
			if err != nil {
				return nil, err
			}
			path = tempDir
			isTemp = true
			createdTemp = tempDir
		}
	} else {
		path = resolvePathKey(requested)
		isTemp = false
	}

	if activeRefs > 0 && path != activePath {
		if createdTemp != "" {
			_ = os.RemoveAll(createdTemp)
		}
		return nil, fmt.Errorf(
			"chdb: a session is already open on path %q; chDB allows only one data path per process, cannot also open %q",
			activePath, path)
	}

	conn, err := initConnection(path)
	if err != nil {
		if createdTemp != "" {
			_ = os.RemoveAll(createdTemp)
		}
		return nil, err
	}

	if activeRefs == 0 {
		activePath = path
		activeTemp = isTemp
	}
	activeRefs++

	return &Session{connStr: path, path: path, isTemp: isTemp, conn: conn}, nil
}

// Query calls `query_conn` function with the current connection and a default output format of "CSV" if not provided.
func (s *Session) Query(queryStr string, outputFormats ...string) (result chdbpurego.ChdbResult, err error) {
	outputFormat := "CSV" // Default value
	if len(outputFormats) > 0 {
		outputFormat = outputFormats[0]
	}
	return s.conn.Query(queryStr, outputFormat)
}

// QueryStream calls `query_conn` function with the current connection and a default output format of "CSV" if not provided.
// The result is a stream of data that can be read in chunks.
// This is useful for large datasets that cannot be loaded into memory all at once.
func (s *Session) QueryStream(queryStr string, outputFormats ...string) (result chdbpurego.ChdbStreamResult, err error) {
	outputFormat := "CSV" // Default value
	if len(outputFormats) > 0 {
		outputFormat = outputFormats[0]
	}
	return s.conn.QueryStreaming(queryStr, outputFormat)
}

// Close closes this session's native connection. When it is the last open
// session on a registry-owned temporary directory, that directory is removed.
// Close is idempotent.
func (s *Session) Close() {
	if s == nil {
		return
	}
	sessMu.Lock()
	defer sessMu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.conn != nil {
		s.conn.Close()
	}
	s.release(false)
}

// Cleanup closes this session and removes its data directory, regardless of
// whether it is temporary. It is destructive and intended for teardown; do not
// call it while other sessions on the same path are still in use. Cleanup is
// idempotent.
func (s *Session) Cleanup() {
	if s == nil {
		return
	}
	sessMu.Lock()
	defer sessMu.Unlock()
	pathToRemove := s.path
	if !s.closed {
		s.closed = true
		if s.conn != nil {
			s.conn.Close()
		}
		s.release(true)
	}
	if pathToRemove != "" {
		_ = os.RemoveAll(pathToRemove)
	}
}

// release decrements the active refcount and, when it reaches zero, clears the
// registry and (unless forced cleanup already handles removal) deletes a
// registry-owned temp directory. Callers must hold sessMu.
func (s *Session) release(forced bool) {
	if activeRefs > 0 {
		activeRefs--
	}
	if activeRefs == 0 {
		if !forced && activeTemp && activePath != "" {
			_ = os.RemoveAll(activePath)
		}
		activePath = ""
		activeTemp = false
	}
}

// Path returns the path of the session.
func (s *Session) Path() string {
	return s.path
}

// ConnStr returns the current connection string used for the underlying connection
func (s *Session) ConnStr() string {
	return s.connStr
}

// IsTemp returns whether the session is temporary.
func (s *Session) IsTemp() bool {
	return s.isTemp
}
