package chdbdriver

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"

	"github.com/chdb-io/chdb-go/chdb"
	chdbpurego "github.com/chdb-io/chdb-go/chdb-purego"
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

		reader := parquet.NewGenericReader[any](bytes.NewReader(nextRes.Buf()))
		return &parquetStreamingRows{
			stream: result, curChunk: nextRes, reader: reader,
			bufferSize: bufSize, needNewBuffer: true,
			useUnsafeStringReader: useUnsafe,
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
	session     *chdb.Session
}

// Connect returns a connection to a database.
func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	if c.driverType == INVALID {
		return nil, fmt.Errorf("DriverType not supported")
	}
	cc := &conn{
		udfPath: c.udfPath, session: c.session,
		driverType: c.driverType, bufferSize: c.bufferSize,
		useUnsafe: c.useUnsafe, isStreaming: c.isStreaming,
	}
	cc.SetupQueryFun()
	return cc, nil
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
	sessionPath, ok := opts[sessionOptionKey]
	if ok {
		ret.session, err = chdb.NewSession(sessionPath)
		if err != nil {
			return nil, err
		}
	}
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
	if ret.session == nil {

		ret.session, err = chdb.NewSession()
		if err != nil {
			return nil, err
		}
	}
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
	return cc.Connect(context.Background())
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
	return nil
}

func (c *conn) SetupQueryFun() {
	if c.isStreaming {
		c.streamFun = chdb.QueryStream
	} else {
		c.QueryFun = chdb.Query
	}

	if c.session != nil {
		if c.isStreaming {
			c.streamFun = c.session.QueryStream
		} else {
			c.QueryFun = c.session.Query
		}

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

	result, err := c.QueryFun(compiledQuery, c.driverType.String(), c.udfPath)
	if err != nil {
		return nil, err
	}
	return &execResult{
		err:      nil,
		localRes: result,
	}, nil
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
		return c.driverType.PrepareStreamingRows(result, c.bufferSize, c.useUnsafe)
	}
	result, err := c.QueryFun(compiledQuery, c.driverType.GetFormat(), c.udfPath)
	if err != nil {
		return nil, err
	}

	buf := result.Buf()
	if len(buf) == 0 {
		return nil, fmt.Errorf("result is nil")
	}
	return c.driverType.PrepareRows(result, buf, c.bufferSize, c.useUnsafe)

}

func (c *conn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("does not support Transcation")
}

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return nil, fmt.Errorf("does not support prepare statement")
}

// todo: func(c *conn) Prepare(query string)
// todo: func(c *conn) PrepareContext(ctx context.Context, query string)
// todo: prepared statment
