package chdbpurego

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

type result struct {
	chdb_result *chdb_result
}

func newChdbResult(cRes *chdb_result) ChdbResult {
	res := &result{
		chdb_result: cRes,
	}
	// runtime.SetFinalizer(res, res.Free)
	return res

}

// Buf implements ChdbResult.
func (c *result) Buf() []byte {
	if c.chdb_result != nil {
		buf := chdbResultBuffer(c.chdb_result)
		if buf != nil {
			// Assuming we have a way to get the length of the buffer
			// Thlis is a placeholder; replace with actual length retrieva logic
			length := c.Len() // Replace with actual length
			return unsafe.Slice(buf, length)
		}

	}
	return nil
}

// BytesRead implements ChdbResult.
func (c *result) BytesRead() uint64 {
	if c.chdb_result != nil {
		return chdbResultBytesRead(c.chdb_result)
	}
	return 0
}

// Elapsed implements ChdbResult.
func (c *result) Elapsed() float64 {
	if c.chdb_result != nil {
		return chdbResultElapsed(c.chdb_result)
	}
	return 0
}

// Error implements ChdbResult.
func (c *result) Error() error {
	if c.chdb_result != nil {
		if s := chdbResultError(c.chdb_result); s != "" {
			return errors.New(s)
		}
	}
	return nil
}

// Free implements ChdbResult.
func (c *result) Free() {
	if c.chdb_result != nil {
		chdbDestroyQueryResult(c.chdb_result)
		c.chdb_result = nil
	}

}

// Len implements ChdbResult.
func (c *result) Len() int {
	if c.chdb_result != nil {
		return int(chdbResultLen(c.chdb_result))
	}
	return 0
}

// RowsRead implements ChdbResult.
func (c *result) RowsRead() uint64 {
	if c.chdb_result != nil {
		return chdbResultRowsRead(c.chdb_result)
	}
	return 0
}

// String implements ChdbResult.
func (c *result) String() string {
	ret := c.Buf()
	if ret == nil {
		return ""
	}
	return string(ret)
}

type connection struct {
	conn *chdb_connection
}

func newChdbConn(conn *chdb_connection) ChdbConn {
	c := &connection{
		conn: conn,
	}
	// runtime.SetFinalizer(c, c.Close)
	return c
}

// Close implements ChdbConn.
//
// Close is idempotent: it nils the underlying handle after freeing it so a
// second Close (or a Close racing a Query that checks for a nil handle) does
// not double-free the native connection.
func (c *connection) Close() {
	if c.conn != nil {
		chdbCloseConn(c.conn)
		c.conn = nil
	}
}

// Query implements ChdbConn.
func (c *connection) Query(queryStr string, formatStr string) (result ChdbResult, err error) {
	if c.conn == nil {
		return nil, fmt.Errorf("invalid connection")
	}

	res := chdbQuery(c.conn.internal_data, queryStr, formatStr)
	if res == nil {
		// According to the C ABI of chDB v1.2.0, the C function query_stable_v2
		// returns nil if the query returns no data. This is not an error. We
		// will change this behavior in the future.
		return newChdbResult(res), nil
	}
	errMsg := chdbResultError(res)
	if errMsg != "" {
		return nil, errors.New(errMsg)
	}

	return newChdbResult(res), nil
}

// QueryStreaming implements ChdbConn.
func (c *connection) QueryStreaming(queryStr string, formatStr string) (result ChdbStreamResult, err error) {

	if c.conn == nil {
		return nil, fmt.Errorf("invalid connection")
	}

	res := chdbStreamQuery(c.conn.internal_data, queryStr, formatStr)
	if res == nil {
		// According to the C ABI of chDB v1.2.0, the C function query_stable_v2
		// returns nil if the query returns no data. This is not an error. We
		// will change this behavior in the future.
		return newStreamingResult(c.conn, res), nil
	}
	if s := chdbResultError(res); s != "" {
		return nil, errors.New(s)
	}

	return newStreamingResult(c.conn, res), nil
}

func (c *connection) Ready() bool {
	if c.conn != nil {
		return true
	}
	return false
}

// NewConnection is the low level function to create a new connection to the chdb server.
// using NewConnectionFromConnString is recommended.
//
// Deprecated: Use NewConnectionFromConnString instead. This function will be removed in a future version.
//
// Session will keep the state of query.
// If path is None, it will create a temporary directory and use it as the database path
// and the temporary directory will be removed when the session is closed.
// You can also pass in a path to create a database at that path where will keep your data.
// This is a thin wrapper around the connect_chdb C API.
// the argc and argv should be like:
//   - argc = 1, argv = []string{"--path=/tmp/chdb"}
//   - argc = 2, argv = []string{"--path=/tmp/chdb", "--readonly=1"}
//
// Important:
//   - chDB supports only one data path per process. Multiple connections to the
//     same path can be open at once and execute queries concurrently; connecting
//     to a different path while connections are still open returns an error.
//   - You need to ensure that the path exists before creating a new connection. Or you can use NewConnectionFromConnString.
func NewConnection(argc int, argv []string) (ChdbConn, error) {
	if err := ensureLoaded(); err != nil {
		return nil, err
	}

	var new_argv []string
	if (argc > 0 && argv[0] != "clickhouse") || argc == 0 {
		new_argv = make([]string, argc+1)
		new_argv[0] = "clickhouse"
		copy(new_argv[1:], argv)
	} else {
		new_argv = argv
	}

	// Remove ":memory:" if it is the only argument
	if len(new_argv) == 2 && (new_argv[1] == ":memory:" || new_argv[1] == "file::memory:") {
		new_argv = new_argv[:1]
	}

	// Convert string slice to C-style char pointers in one step
	c_argv := make([]*byte, len(new_argv))
	for i, str := range new_argv {
		// Convert string to []byte and append null terminator
		bytes := append([]byte(str), 0)
		// Use &bytes[0] to get pointer to first byte
		c_argv[i] = &bytes[0]
	}

	// debug print new_argv
	// for _, arg := range new_argv {
	// 	fmt.Println("arg: ", arg)
	// }

	var conn *chdb_connection
	var err error
	func() {
		// chdb_connect — even after chdb_set_signal_handlers_enabled(0) —
		// still resets the kernel sigaction table for SIGSEGV / SIGABRT /
		// SIGBUS / SIGILL / SIGFPE to SIG_DFL on the first call (verified
		// empirically; see issue #30 for context). guardSignalHandlers
		// snapshots Go's handlers now and the deferred closure restores
		// them on return — even if chdb_connect panics (a C++ exception
		// propagated through purego), the LIFO defer order runs
		// recover() first to set err and then the restore, leaving the
		// process with Go's stack-growth / panic-recovery handlers
		// intact.
		//
		// On old libchdb builds that don't export the signal-handler
		// control API, guardSignalHandlers returns a no-op closure and
		// the call site behaves exactly as it did before the issue #30
		// fix landed.
		defer guardSignalHandlers()()

		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("C++ exception: %v", r)
			}
		}()
		conn = chdbConnect(len(new_argv), c_argv)
	}()

	if err != nil {
		return nil, err
	}

	if conn == nil {
		return nil, fmt.Errorf("could not create a chdb connection")
	}
	return newChdbConn(conn), nil
}

// NewConnectionFromConnString creates a new connection to the chdb server using a connection string.
// You can use a connection string to pass in the path and other parameters.
// Examples:
//   - ":memory:" (for in-memory database)
//   - "test.db" (for relative path)
//   - "file:test.db" (same as above)
//   - "/path/to/test.db" (for absolute path)
//   - "file:/path/to/test.db" (same as above)
//   - "file:test.db?param1=value1&param2=value2" (for relative path with query params)
//   - "file::memory:?verbose&log-level=test" (for in-memory database with query params)
//   - "///path/to/test.db?param1=value1&param2=value2" (for absolute path)
//
// Connection string args handling:
//
//	Connection string can contain query params like "file:test.db?param1=value1&param2=value2"
//	"param1=value1" will be passed to ClickHouse engine as start up args.
//
//	For more details, see `clickhouse local --help --verbose`
//	Some special args handling:
//	- "mode=ro" would be "--readonly=1" for clickhouse (read-only mode)
//
// Important:
//   - chDB supports only one data path per process. Multiple connections to the
//     same path can be open at once and execute queries concurrently; connecting
//     to a different path while connections are still open returns an error.
func NewConnectionFromConnString(conn_string string) (ChdbConn, error) {
	if conn_string == "" || conn_string == ":memory:" {
		return NewConnection(0, []string{})
	}

	// Handle file: prefix
	workingStr := conn_string
	if strings.HasPrefix(workingStr, "file:") {
		workingStr = workingStr[5:]
		// Handle triple slash for absolute paths
		if strings.HasPrefix(workingStr, "///") {
			workingStr = workingStr[2:] // Remove two slashes, keep one
		}
	}

	// Split path and parameters
	var path string
	var params []string
	if queryPos := strings.Index(workingStr, "?"); queryPos != -1 {
		path = workingStr[:queryPos]
		paramStr := workingStr[queryPos+1:]

		// Parse parameters
		for _, param := range strings.Split(paramStr, "&") {
			if param == "" {
				continue
			}
			if eqPos := strings.Index(param, "="); eqPos != -1 {
				key := param[:eqPos]
				value := param[eqPos+1:]
				if key == "mode" && value == "ro" {
					params = append(params, "--readonly=1")
				} else if key == "udf_path" && value != "" {
					params = append(params, "--")
					params = append(params, "--user_scripts_path="+value)
					params = append(params, "--user_defined_executable_functions_config="+value+"/*.xml")
				} else {
					params = append(params, "--"+key+"="+value)
				}
			} else {
				params = append(params, "--"+param)
			}
		}
	} else {
		path = workingStr
	}

	// Convert relative paths to absolute if needed
	if path != "" && !strings.HasPrefix(path, "/") && path != ":memory:" {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve path: %s", path)
		}
		path = absPath
	}

	// Check if path exists and handle directory creation/permissions
	if path != "" && path != ":memory:" {
		// Check if path exists
		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			// Create directory if it doesn't exist
			if err := os.MkdirAll(path, 0755); err != nil {
				return nil, fmt.Errorf("failed to create directory: %s", path)
			}
		} else if err != nil {
			return nil, fmt.Errorf("failed to check directory: %s", path)
		}

		// Check write permissions if not in readonly mode
		isReadOnly := false
		for _, param := range params {
			if param == "--readonly=1" {
				isReadOnly = true
				break
			}
		}

		if !isReadOnly {
			// Check write permissions by attempting to create a file
			if err := unix.Access(path, unix.W_OK); err != nil {
				return nil, fmt.Errorf("no write permission for directory: %s", path)
			}
		}
	}

	// Build arguments array
	argv := make([]string, 0, len(params)+2)
	argv = append(argv, "clickhouse")
	if path != "" && path != ":memory:" {
		argv = append(argv, "--path="+path)
	}
	argv = append(argv, params...)

	return NewConnection(len(argv), argv)
}
