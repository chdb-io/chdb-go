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
//
// activePath holds the resolved physical identity of the live engine (an
// absolute directory, or ":memory:") so that two Sessions naming the same data
// path with different connection-string spellings ("db", "file:db",
// "file:db?param=v") are correctly recognized as the same path.
var (
	sessMu     sync.Mutex
	activePath string // resolved identity of the live engine; "" when none is open
	activeRefs int    // number of open Sessions on activePath
	activeTemp bool   // whether activePath is a temp dir owned by the registry
)

type Session struct {
	// mu guards conn and closed so that Query/QueryStream cannot run against a
	// native connection that Close/Cleanup is freeing. Query takes a read lock
	// (concurrent queries are allowed); Close/Cleanup take the write lock, which
	// waits for in-flight queries to finish before the native connection is
	// destroyed.
	mu      sync.RWMutex
	conn    chdbpurego.ChdbConn
	connStr string // full connection string handed to the native layer (params preserved)
	path    string // resolved on-disk data directory ("" for an in-memory session)
	isTemp  bool
	closed  bool
}

// resolveTarget canonicalizes a requested connection string the same way the
// native chdb-purego layer (NewConnectionFromConnString) does, so the registry
// identity key always matches the directory libchdb actually opens. It strips a
// leading "file:" scheme (and the "file:///" triple slash), drops any "?params"
// query string, and makes a filesystem path absolute. It returns:
//
//   - key: the identity used to decide whether two Sessions share a data path
//     (an absolute directory, or ":memory:"). Drive-letter and other absolute
//     paths are normalized by filepath.Abs, which is OS-aware (so Windows
//     "C:\\data" resolves correctly).
//   - dir: the on-disk directory to remove on Cleanup ("" for in-memory).
func resolveTarget(p string) (key, dir string) {
	s := p
	if strings.HasPrefix(s, "file:") {
		s = s[len("file:"):]
		// "file:///abs/path" -> "/abs/path" (keep a single leading slash).
		if strings.HasPrefix(s, "///") {
			s = s[2:]
		}
	}
	if i := strings.IndexByte(s, '?'); i != -1 {
		s = s[:i]
	}
	if s == "" || s == ":memory:" {
		return ":memory:", ""
	}
	if abs, err := filepath.Abs(s); err == nil {
		s = abs
	}
	return s, s
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
// process). The same physical path written different ways ("db", "file:db",
// "file:db?param=v") is recognized as the same path and is allowed.
func NewSession(paths ...string) (*Session, error) {
	requested := ""
	if len(paths) > 0 {
		requested = paths[0]
	}

	sessMu.Lock()
	defer sessMu.Unlock()

	var (
		connStr     string
		key         string
		dir         string
		isTemp      bool
		createdTemp string
	)

	switch {
	case requested == "" && activeRefs > 0:
		// Reuse the already-open data path so the new session attaches to the
		// same engine instead of trying to open a second path.
		connStr = activePath
		key = activePath
		if activePath != ":memory:" {
			dir = activePath
		}
		isTemp = activeTemp
	case requested == "":
		// Nothing open and no path requested: create a temp directory.
		tempDir, err := os.MkdirTemp("", "chdb_")
		if err != nil {
			return nil, err
		}
		connStr, key, dir = tempDir, tempDir, tempDir
		isTemp = true
		createdTemp = tempDir
	default:
		// Preserve the original DSN (including any params) for the native
		// connect, but key/identify the session by its resolved physical path.
		connStr = requested
		key, dir = resolveTarget(requested)
	}

	if activeRefs > 0 && key != activePath {
		if createdTemp != "" {
			_ = os.RemoveAll(createdTemp)
		}
		return nil, fmt.Errorf(
			"chdb: a session is already open on path %q; chDB allows only one data path per process, cannot also open %q",
			activePath, key)
	}

	conn, err := initConnection(connStr)
	if err != nil {
		if createdTemp != "" {
			_ = os.RemoveAll(createdTemp)
		}
		return nil, err
	}

	if activeRefs == 0 {
		activePath = key
		activeTemp = isTemp
	}
	activeRefs++

	return &Session{connStr: connStr, path: dir, isTemp: isTemp, conn: conn}, nil
}

// Query calls `query_conn` function with the current connection and a default output format of "CSV" if not provided.
func (s *Session) Query(queryStr string, outputFormats ...string) (result chdbpurego.ChdbResult, err error) {
	outputFormat := "CSV" // Default value
	if len(outputFormats) > 0 {
		outputFormat = outputFormats[0]
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.conn == nil {
		return nil, fmt.Errorf("chdb: query on a closed session")
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.conn == nil {
		return nil, fmt.Errorf("chdb: query on a closed session")
	}
	return s.conn.QueryStreaming(queryStr, outputFormat)
}

// Close closes this session's native connection. When it is the last open
// session on a registry-owned temporary directory, that directory is removed.
//
// Close is idempotent and safe to call concurrently with Query: it waits for
// any in-flight query on this session to finish before freeing the native
// connection.
func (s *Session) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
	s.mu.Unlock()

	sessMu.Lock()
	s.release(false)
	sessMu.Unlock()
}

// Cleanup closes this session and, when it is the last session on the data
// path, removes the data directory regardless of whether it is temporary. It is
// destructive and intended for teardown, but it will NOT delete a directory
// that sibling sessions on the same path are still using. Cleanup is
// idempotent and, like Close, waits for any in-flight query to finish.
func (s *Session) Cleanup() {
	if s == nil {
		return
	}
	s.mu.Lock()
	wasOpen := !s.closed
	if wasOpen {
		s.closed = true
		if s.conn != nil {
			s.conn.Close()
			s.conn = nil
		}
	}
	dir := s.path
	s.mu.Unlock()

	sessMu.Lock()
	var last bool
	if wasOpen {
		// forced: suppress release()'s own temp removal; we remove dir below
		// (Cleanup removes non-temp directories too).
		last = s.release(true)
	} else {
		// This session was already released by a prior Close; only remove the
		// directory if the path is now idle (no sibling sessions remain).
		last = activeRefs == 0
	}
	sessMu.Unlock()

	if last && dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// release decrements the active refcount and reports whether this was the last
// session on the path. When the count reaches zero the registry is cleared and,
// unless the caller forces its own cleanup, a registry-owned temp directory is
// removed. Callers must hold sessMu.
func (s *Session) release(forced bool) (last bool) {
	if activeRefs > 0 {
		activeRefs--
	}
	if activeRefs == 0 {
		if !forced && activeTemp && activePath != "" {
			_ = os.RemoveAll(activePath)
		}
		activePath = ""
		activeTemp = false
		return true
	}
	return false
}

// Path returns the resolved on-disk data directory of the session ("" for an
// in-memory session).
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

// ActiveSessionRefs returns the number of currently-open sessions sharing the
// process-wide data path (0 when no session is open). It is primarily useful
// for diagnostics and tests that assert sessions and their native connections
// are released correctly.
func ActiveSessionRefs() int {
	sessMu.Lock()
	defer sessMu.Unlock()
	return activeRefs
}
