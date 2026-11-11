package chdbdriver

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/chdb-io/chdb-go/v2/chdb"
	chdbpurego "github.com/chdb-io/chdb-go/v2/chdb-purego"
	"github.com/huandu/go-sqlbuilder"
	"github.com/parquet-go/parquet-go"
)

type DriverType int

const (
	ARROW DriverType = iota
	PARQUET
	PARQUET_STREAMING
	INVALID
)

const (
	sessionOptionKey         = "session"
	udfPathOptionKey         = "udfPath"
	driverTypeKey            = "driverType"
	useUnsafeStringReaderKey = "useUnsafeStringReader"
	driverBufferSizeKey      = "bufferSize"
	defaultBufferSize        = 512
)

func (d DriverType) String() string {
	switch d {
	case ARROW:
		return "Arrow"
	case PARQUET:
		return "Parquet"
	case INVALID:
		return "Invalid"
	}
	return ""
}

func (d DriverType) PrepareRows(result chdbpurego.ChdbResult, buf []byte, bufSize int, useUnsafe bool) (driver.Rows, error) {
	switch d {
	case PARQUET:
		reader := parquet.NewGenericReader[any](bytes.NewReader(buf))
		return &parquetRows{
			localResult: result, reader: reader,
			bufferSize: bufSize, needNewBuffer: true,
			useUnsafeStringReader: useUnsafe,
			schemaFields:          reader.Schema().Fields(),
		}, nil

	}
	return nil, fmt.Errorf("unsupported driver type")
}

func (d DriverType) PrepareStreamingRows(result chdbpurego.ChdbStreamResult, bufSize int, useUnsafe bool) (driver.Rows, error) {
	switch d {
	case PARQUET_STREAMING:
		nextRes := result.GetNext()
		if nextRes == nil {
			return nil, fmt.Errorf("result is nil")
		}
		// A failed query arrives as a first chunk carrying the error and no
		// bytes; parquet.NewGenericReader panics on an empty buffer.
		if err := nextRes.Error(); err != nil {
			return nil, err
		}
		if nextRes.Len() == 0 {
			return nil, fmt.Errorf("result is nil")
		}

		reader := parquet.NewGenericReader[any](bytes.NewReader(nextRes.Buf()))
		return &parquetStreamingRows{
			stream: result, curChunk: nextRes, reader: reader,
			bufferSize: bufSize, needNewBuffer: true,
			useUnsafeStringReader: useUnsafe,
			schemaFields:          reader.Schema().Fields(),
		}, nil

	}
	return nil, fmt.Errorf("unsupported driver type")
}

func (d DriverType) SupportStreaming() bool {
	switch d {
	case PARQUET_STREAMING:
		return true
	}
	return false
}

func (d DriverType) GetFormat() string {
	switch d {
	case PARQUET:
		return "Parquet"
	case PARQUET_STREAMING:
		return "Parquet"
	}
	return ""

}

func parseDriverType(s string) DriverType {
	switch strings.ToUpper(s) {
	// case "ARROW":
	// 	return ARROW
	case "PARQUET":
		return PARQUET
	case "PARQUET_STREAMING":
		return PARQUET_STREAMING
	}
	return INVALID
}

func init() {
	sql.Register("chdb", Driver{})
}

// Row is the result of calling [DB.QueryRow] to select a single row.
type singleRow struct {
	// One of these two will be non-nil:
	err  error // deferred error for easy chaining
	rows driver.Rows
}

// Scan copies the columns from the matched row into the values
// pointed at by dest. See the documentation on [Rows.Scan] for details.
// If more than one row matches the query,
// Scan uses the first row and discards the rest. If no row matches
// the query, Scan returns [ErrNoRows].
func (r *singleRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	vals := make([]driver.Value, 0)
	for _, v := range dest {
		vals = append(vals, v)
	}
	err := r.rows.Next(vals)
	if err != nil {
		return err
	}
	// Make sure the query can be processed to completion with no errors.
	return r.rows.Close()
}

// Err provides a way for wrapping packages to check for
// query errors without calling [Row.Scan].
// Err returns the error, if any, that was encountered while running the query.
// If this error is not nil, this error will also be returned from [Row.Scan].
func (r *singleRow) Err() error {
	return r.err
}

type execResult struct {
	localRes chdbpurego.ChdbResult
	err      error
}

func (e *execResult) LastInsertId() (int64, error) {
	if e.err != nil {
		return 0, e.err
	}
	return -1, fmt.Errorf("does not support LastInsertId")

}
func (e *execResult) RowsAffected() (int64, error) {
	if e.err != nil {
		return 0, e.err
	}
	// chdb return the number of rows inserted/updated/deleted trough rows_read
	return int64(e.localRes.RowsRead()), nil
}

type queryHandle func(string, ...string) (chdbpurego.ChdbResult, error)

type queryStream func(string, ...string) (chdbpurego.ChdbStreamResult, error)

type connector struct {
	udfPath     string
	driverType  DriverType
	bufferSize  int
	isStreaming bool
	useUnsafe   bool
	connStr     string
	keeper      *chdb.Session
}

// Connect returns a connection to a database.
func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	if c.driverType == INVALID {
		return nil, fmt.Errorf("DriverType not supported")
	}
	// Each database/sql connection gets its own native chDB connection to the
	// shared data path, so a pool of connections (MaxOpenConns > 1) yields real
	// parallel query execution instead of serializing on a single connection.
	session, err := chdb.NewSession(c.connStr)
	if err != nil {
		return nil, err
	}
	cc := &conn{
		udfPath: c.udfPath, session: session,
		driverType: c.driverType, bufferSize: c.bufferSize,
		useUnsafe: c.useUnsafe, isStreaming: c.isStreaming,
	}
	cc.SetupQueryFun()
	return cc, nil
}

// Close releases the connector's keeper session. database/sql calls this from
// DB.Close() because the connector implements io.Closer. Dropping the keeper
// reference lets a registry-owned temp directory be removed once all pooled
// connections are closed as well.
func (c *connector) Close() error {
	if c.keeper != nil {
		c.keeper.Close()
		c.keeper = nil
	}
	return nil
}

// Driver returns the underying Driver of the connector,
// compatibility with the Driver method on sql.DB
func (c *connector) Driver() driver.Driver { return Driver{} }

func parseConnectStr(str string) (ret map[string]string, err error) {
	ret = make(map[string]string)
	if len(str) == 0 {
		return
	}
	for _, kv := range strings.Split(str, ";") {
		parsed := strings.SplitN(kv, "=", 2)
		if len(parsed) != 2 {
			return nil, fmt.Errorf("invalid format for connection string, str: %s", kv)
		}

		ret[strings.TrimSpace(parsed[0])] = strings.TrimSpace(parsed[1])
	}

	return
}
func NewConnect(opts map[string]string) (ret *connector, err error) {
	ret = &connector{}
	driverType, ok := opts[driverTypeKey]
	if ok {
		ret.driverType = parseDriverType(driverType)
	} else {
		ret.driverType = PARQUET //default to parquet
	}
	bufferSize, ok := opts[driverBufferSizeKey]
	if ok {
		sz, err := strconv.Atoi(bufferSize)
		if err != nil {
			ret.bufferSize = defaultBufferSize
		} else {
			ret.bufferSize = sz
		}
	} else {
		ret.bufferSize = defaultBufferSize
	}
	useUnsafe, ok := opts[useUnsafeStringReaderKey]
	if ok {
		if strings.ToLower(useUnsafe) == "true" {
			ret.useUnsafe = true
		}
	}

	udfPath, ok := opts[udfPathOptionKey]
	if ok {
		ret.udfPath = udfPath
	}

	// Open a "keeper" session that pins the data path (and any temp directory)
	// for the lifetime of this connector. Each pooled connection then opens its
	// own session on the same path; the keeper guarantees the engine and temp
	// dir survive pool churn (when the live connection count briefly hits zero).
	sessionPath := opts[sessionOptionKey] // "" when not provided
	ret.keeper, err = chdb.NewSession(sessionPath)
	if err != nil {
		return nil, err
	}
	ret.connStr = ret.keeper.ConnStr()

	ret.isStreaming = ret.driverType.SupportStreaming()
	return
}

type Driver struct{}

// Open returns a new connection to the database.
func (d Driver) Open(name string) (driver.Conn, error) {
	cc, err := d.OpenConnector(name)
	if err != nil {
		return nil, err
	}
	c, err := cc.Connect(context.Background())
	if err != nil {
		// Connect failed; release the keeper session NewConnect just opened so
		// it does not leak (database/sql would normally own and close cc).
		if closer, ok := cc.(*connector); ok {
			_ = closer.Close()
		}
		return nil, err
	}
	// On the sql.Open path, database/sql keeps the connector and calls
	// connector.Close() on db.Close(). A direct Driver.Open caller discards the
	// connector, so tie the keeper session's lifetime to the returned
	// connection: closing the conn also releases the keeper.
	if cn, ok := c.(*conn); ok {
		if cnr, ok := cc.(*connector); ok {
			cn.connector = cnr
		}
	}
	return c, nil
}

// OpenConnector expects the same format as driver.Open
func (d Driver) OpenConnector(name string) (driver.Connector, error) {
	opts, err := parseConnectStr(name)
	if err != nil {
		return nil, err
	}
	return NewConnect(opts)
}

type conn struct {
	udfPath     string
	driverType  DriverType
	bufferSize  int
	useUnsafe   bool
	isStreaming bool
	session     *chdb.Session
	// connector is set only on the legacy Driver.Open path (not the sql.Open
	// path, where database/sql owns and closes the connector). When set, Close
	// also releases the connector's keeper session.
	connector *connector
	tx        *tx

	QueryFun  queryHandle
	streamFun queryStream
}

func prepareValues(values []driver.Value) []driver.NamedValue {
	namedValues := make([]driver.NamedValue, len(values))
	for i, value := range values {
		namedValues[i] = driver.NamedValue{
			// nb: Name field is optional
			Ordinal: i,
			Value:   value,
		}
	}
	return namedValues
}

func (c *conn) Close() error {
	if c.tx != nil {
		c.tx.end()
	}
	if c.session != nil {
		c.session.Close()
		c.session = nil
	}
	// Only set on the Driver.Open path; releases the keeper that pins the data
	// path/temp dir for the connector.
	if c.connector != nil {
		_ = c.connector.Close()
		c.connector = nil
	}
	return nil
}

// SetupQueryFun wires both entry points, streaming or not. Exec never streams —
// it throws the result away — so it goes through QueryFun even on a streaming
// connection, where QueryFun used to be left nil and turned every db.Exec into a
// nil-func call.
func (c *conn) SetupQueryFun() {
	c.QueryFun = chdb.Query
	c.streamFun = chdb.QueryStream

	if c.session != nil {
		c.QueryFun = c.session.Query
		c.streamFun = c.session.QueryStream
	}
}

func (c *conn) Query(query string, values []driver.Value) (driver.Rows, error) {
	return c.QueryContext(context.Background(), query, prepareValues(values))
}

func (c *conn) QueryRow(query string, values []driver.Value) *singleRow {
	return c.QueryRowContext(context.Background(), query, values)
}

func (c *conn) Exec(query string, values []driver.Value) (sql.Result, error) {
	return c.ExecContext(context.Background(), query, prepareValues(values))
}

func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	compiledQuery, err := c.compileArguments(query, args)
	if err != nil {
		return nil, err
	}

	result, err := c.QueryFun(compiledQuery, c.driverType.GetFormat(), c.udfPath)
	if err != nil {
		return nil, err
	}
	res := &execResult{
		err:      nil,
		localRes: result,
	}
	runtime.SetFinalizer(res, func(r *execResult) {
		if r.localRes != nil {
			r.localRes.Free()
		}
	})
	return res, nil
}

func (c *conn) QueryRowContext(ctx context.Context, query string, values []driver.Value) *singleRow {

	v, err := c.QueryContext(ctx, query, prepareValues(values))
	if err != nil {
		return &singleRow{
			err:  err,
			rows: nil,
		}
	}
	return &singleRow{
		rows: v,
	}
}

// CheckNamedValue lets unsigned integers through untouched. Arguments are
// interpolated into the query text, which formats uint64 exactly, but
// database/sql's default converter refuses a uint64 with the high bit set,
// and UInt64 columns hold values up to math.MaxUint64. Every other type keeps
// the default conversion.
func (c *conn) CheckNamedValue(nv *driver.NamedValue) error {
	switch nv.Value.(type) {
	case uint, uint64:
		return nil
	}
	return driver.ErrSkip
}

func (c *conn) compileArguments(query string, args []driver.NamedValue) (string, error) {
	var compiledQuery string
	if len(args) > 0 {
		compiledArgs := make([]interface{}, len(args))
		for idx := range args {
			compiledArgs[idx] = args[idx].Value
		}
		compiled, err := sqlbuilder.ClickHouse.Interpolate(query, compiledArgs)
		if err != nil {
			return "", err
		}
		compiledQuery = compiled
	} else {
		compiledQuery = query
	}
	return compiledQuery, nil
}

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	compiledQuery, err := c.compileArguments(query, args)
	if err != nil {
		return nil, err
	}
	if c.isStreaming {
		result, err := c.streamFun(compiledQuery, c.driverType.GetFormat(), c.udfPath)
		if err != nil {
			return nil, err
		}
		rows, err := c.driverType.PrepareStreamingRows(result, c.bufferSize, c.useUnsafe)
		if err != nil {
			// There is no Rows to close the stream from, and the stream holds the
			// connection it was started on open — a sessionless query keeps a whole
			// session for it — so release it here instead of at the next GC.
			result.Free()
			return nil, err
		}
		return rows, nil
	}
	result, err := c.QueryFun(compiledQuery, c.driverType.GetFormat(), c.udfPath)
	if err != nil {
		return nil, err
	}

	buf := result.Buf()
	if len(buf) == 0 {
		// Statements with no result set (DDL, SET, INSERT) land here. Nothing will
		// read the result, so free it instead of leaving it to no one.
		result.Free()
		return nil, fmt.Errorf("result is nil")
	}
	rows, err := c.driverType.PrepareRows(result, buf, c.bufferSize, c.useUnsafe)
	if err != nil {
		result.Free()
		return nil, err
	}
	return rows, nil

}

func (c *conn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

// BeginTx starts a batch, not an ACID transaction, with the semantics
// clickhouse-go gives database/sql: an INSERT prepared inside it collects one
// row per Exec, Commit sends each collected batch as a single INSERT, and
// Rollback discards them. Statements executed directly on the transaction run
// immediately and are not undone by Rollback.
func (c *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if opts.ReadOnly || sql.IsolationLevel(opts.Isolation) != sql.LevelDefault {
		return nil, errors.New("chdb: isolation levels and read-only transactions are not supported")
	}
	if c.tx != nil {
		return nil, errors.New("chdb: a transaction is already open on this connection")
	}
	c.tx = &tx{c: c}
	return c.tx, nil
}

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

// PrepareContext returns a client-side statement: chDB has no server-side
// prepare, so the query is kept and its arguments are interpolated on every
// execution, exactly as db.Exec and db.Query do. Inside a transaction, an
// INSERT ... VALUES (or a bare INSERT INTO t) becomes a batch instead.
func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if c.tx != nil {
		if m := insertBatchQuery.FindStringSubmatch(query); m != nil {
			b := &batchStmt{tx: c.tx, prefix: m[1], row: strings.TrimSpace(m[2])}
			c.tx.batches = append(c.tx.batches, b)
			return b, nil
		}
	}
	return &stmt{c: c, query: query}, nil
}

// insertBatchQuery matches "INSERT INTO [TABLE] t [(cols)] [VALUES row]" and
// captures the part before VALUES and the row template after it. INSERT ...
// SELECT, FORMAT and SETTINGS do not match and run as ordinary statements.
var insertBatchQuery = regexp.MustCompile(`(?is)^\s*(INSERT\s+INTO\s+(?:TABLE\s+)?[^\s(]+(?:\s*\([^()]*\))?)(?:\s+VALUES\s*(.*?))?\s*;?\s*$`)

type tx struct {
	c       *conn
	batches []*batchStmt
}

func (t *tx) end() {
	if t.c != nil && t.c.tx == t {
		t.c.tx = nil
	}
	t.c = nil
}

// Commit sends the batches in the order they were prepared. Each batch is one
// INSERT, so a failure leaves the batches before it inserted.
func (t *tx) Commit() error {
	c, batches := t.c, t.batches
	if c == nil {
		return errors.New("chdb: commit after the connection was closed; batched rows were discarded")
	}
	t.end()
	for _, b := range batches {
		if err := b.flush(c); err != nil {
			return err
		}
	}
	return nil
}

func (t *tx) Rollback() error {
	t.end()
	return nil
}

type batchStmt struct {
	tx     *tx
	prefix string
	row    string // VALUES template; empty means one placeholder per argument
	rows   []string
}

func (b *batchStmt) Close() error  { return nil }
func (b *batchStmt) NumInput() int { return -1 }

func (b *batchStmt) Exec(args []driver.Value) (driver.Result, error) {
	return b.ExecContext(context.Background(), prepareValues(args))
}

func (b *batchStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if b.tx.c == nil {
		return nil, errors.New("chdb: batch statement used after its transaction ended")
	}
	row := b.row
	if row == "" {
		row = "(" + strings.TrimSuffix(strings.Repeat("?, ", len(args)), ", ") + ")"
	}
	compiled, err := b.tx.c.compileArguments(row, args)
	if err != nil {
		return nil, err
	}
	b.rows = append(b.rows, compiled)
	return driver.RowsAffected(0), nil
}

func (b *batchStmt) Query(args []driver.Value) (driver.Rows, error) {
	return nil, errors.New("chdb: a statement prepared for a batch INSERT only supports Exec")
}

func (b *batchStmt) flush(c *conn) error {
	if len(b.rows) == 0 {
		return nil
	}
	_, err := c.ExecContext(context.Background(), b.prefix+" VALUES "+strings.Join(b.rows, ", "), nil)
	return err
}

type stmt struct {
	c     *conn
	query string
}

func (s *stmt) Close() error { return nil }

// NumInput returns -1 because placeholders are counted by the interpolator,
// which also understands quoting.
func (s *stmt) NumInput() int { return -1 }

func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.c.ExecContext(context.Background(), s.query, prepareValues(args))
}

func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.c.QueryContext(context.Background(), s.query, prepareValues(args))
}

func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.c.ExecContext(ctx, s.query, args)
}

func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.c.QueryContext(ctx, s.query, args)
}
