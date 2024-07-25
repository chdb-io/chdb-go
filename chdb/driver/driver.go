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
	"github.com/chdb-io/chdb-go/chdbstable"
	"github.com/huandu/go-sqlbuilder"
	"github.com/parquet-go/parquet-go"

	"github.com/apache/arrow/go/v15/arrow/ipc"
)

type DriverType int

const (
	ARROW DriverType = iota
	PARQUET
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

func (d DriverType) PrepareRows(result *chdbstable.LocalResult, buf []byte, bufSize int, useUnsafe bool) (driver.Rows, error) {
	switch d {
	case ARROW:
		reader, err := ipc.NewFileReader(bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		return &arrowRows{localResult: result, reader: reader}, nil
	case PARQUET:
		reader := parquet.NewGenericReader[any](bytes.NewReader(buf))
		return &parquetRows{
			localResult: result, reader: reader,
			bufferSize: bufSize, needNewBuffer: true,
			useUnsafeStringReader: useUnsafe,
		}, nil
	}
	return nil, fmt.Errorf("Unsupported driver type")
}

func parseDriverType(s string) DriverType {
	switch strings.ToUpper(s) {
	case "ARROW":
		return ARROW
	case "PARQUET":
		return PARQUET
	}
	return INVALID
}

func init() {
	sql.Register("chdb", Driver{})
}

type queryHandle func(string, ...string) (*chdbstable.LocalResult, error)

type connector struct {
	udfPath    string
	driverType DriverType
	bufferSize int
	useUnsafe  bool
	session    *chdb.Session
}

// Connect returns a connection to a database.
func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	if c.driverType == INVALID {
		return nil, fmt.Errorf("DriverType not supported")
	}
	cc := &conn{
		udfPath: c.udfPath, session: c.session,
		driverType: c.driverType, bufferSize: c.bufferSize,
		useUnsafe: c.useUnsafe,
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
		ret.driverType = ARROW //default to arrow
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
	udfPath    string
	driverType DriverType
	bufferSize int
	useUnsafe  bool
	session    *chdb.Session
	QueryFun   queryHandle
}

func (c *conn) Close() error {
	return nil
}

func (c *conn) SetupQueryFun() {
	c.QueryFun = chdb.Query
	if c.session != nil {
		c.QueryFun = c.session.Query
	}
}

func (c *conn) Query(query string, values []driver.Value) (driver.Rows, error) {
	namedValues := make([]driver.NamedValue, len(values))
	for i, value := range values {
		namedValues[i] = driver.NamedValue{
			// nb: Name field is optional
			Ordinal: i,
			Value:   value,
		}
	}
	return c.QueryContext(context.Background(), query, namedValues)
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
	result, err := c.QueryFun(compiledQuery, c.driverType.String(), c.udfPath)
	if err != nil {
		return nil, err
	}

	buf := result.Buf()
	if buf == nil {
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
