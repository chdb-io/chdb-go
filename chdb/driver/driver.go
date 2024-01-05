package chdbdriver

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/apache/arrow/go/v14/arrow"
	"github.com/apache/arrow/go/v14/arrow/array"
	"github.com/apache/arrow/go/v14/arrow/decimal128"
	"github.com/apache/arrow/go/v14/arrow/decimal256"
	"github.com/chdb-io/chdb-go/chdb"
	"github.com/chdb-io/chdb-go/chdbstable"

	"github.com/apache/arrow/go/v14/arrow/ipc"
)

const sessionOptionKey = "session"
const udfPathOptionKey = "udfPath"

func init() {
	sql.Register("chdb", Driver{})
}

type queryHandle func(string, ...string) *chdbstable.LocalResult

type connector struct {
	udfPath string
	session *chdb.Session
}

// Connect returns a connection to a database.
func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	cc := &conn{udfPath: c.udfPath, session: c.session}
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
	udfPath  string
	session  *chdb.Session
	QueryFun queryHandle
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

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	result := c.QueryFun(query, "Arrow", c.udfPath)
	buf := result.Buf()
	if buf == nil {
		return nil, fmt.Errorf("result is nil")
	}
	reader, err := ipc.NewFileReader(bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	return &rows{localResult: result, reader: reader}, nil
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

type rows struct {
	localResult *chdbstable.LocalResult
	reader      *ipc.FileReader
	curRecord   arrow.Record
	curRow      int64
}

func (r *rows) Columns() (out []string) {
	sch := r.reader.Schema()
	for i := 0; i < sch.NumFields(); i++ {
		out = append(out, sch.Field(i).Name)
	}
	return
}

func (r *rows) Close() error {
	if r.curRecord != nil {
		r.curRecord = nil
	}
	// ignore reader close
	_ = r.reader.Close()
	r.reader = nil
	r.localResult = nil
	return nil
}

func (r *rows) Next(dest []driver.Value) error {
	if r.curRecord != nil && r.curRow == r.curRecord.NumRows() {
		r.curRecord = nil
	}
	for r.curRecord == nil {
		record, err := r.reader.Read()
		if err != nil {
			return err
		}
		if record.NumRows() == 0 {
			continue
		}
		r.curRecord = record
		r.curRow = 0
	}

	for i, col := range r.curRecord.Columns() {
		if col.IsNull(int(r.curRow)) {
			dest[i] = nil
			continue
		}
		switch col := col.(type) {
		case *array.Boolean:
			dest[i] = col.Value(int(r.curRow))
		case *array.Int8:
			dest[i] = col.Value(int(r.curRow))
		case *array.Uint8:
			dest[i] = col.Value(int(r.curRow))
		case *array.Int16:
			dest[i] = col.Value(int(r.curRow))
		case *array.Uint16:
			dest[i] = col.Value(int(r.curRow))
		case *array.Int32:
			dest[i] = col.Value(int(r.curRow))
		case *array.Uint32:
			dest[i] = col.Value(int(r.curRow))
		case *array.Int64:
			dest[i] = col.Value(int(r.curRow))
		case *array.Uint64:
			dest[i] = col.Value(int(r.curRow))
		case *array.Float32:
			dest[i] = col.Value(int(r.curRow))
		case *array.Float64:
			dest[i] = col.Value(int(r.curRow))
		case *array.String:
			dest[i] = col.Value(int(r.curRow))
		case *array.LargeString:
			dest[i] = col.Value(int(r.curRow))
		case *array.Binary:
			dest[i] = col.Value(int(r.curRow))
		case *array.LargeBinary:
			dest[i] = col.Value(int(r.curRow))
		case *array.Date32:
			dest[i] = col.Value(int(r.curRow)).ToTime()
		case *array.Date64:
			dest[i] = col.Value(int(r.curRow)).ToTime()
		case *array.Time32:
			dest[i] = col.Value(int(r.curRow)).ToTime(col.DataType().(*arrow.Time32Type).Unit)
		case *array.Time64:
			dest[i] = col.Value(int(r.curRow)).ToTime(col.DataType().(*arrow.Time64Type).Unit)
		case *array.Timestamp:
			dest[i] = col.Value(int(r.curRow)).ToTime(col.DataType().(*arrow.TimestampType).Unit)
		case *array.Decimal128:
			dest[i] = col.Value(int(r.curRow))
		case *array.Decimal256:
			dest[i] = col.Value(int(r.curRow))
		default:
			return fmt.Errorf(
				"not yet implemented populating from columns of type " + col.DataType().String(),
			)
		}
	}

	r.curRow++
	return nil
}

func (r *rows) ColumnTypeDatabaseTypeName(index int) string {
	return r.reader.Schema().Field(index).Type.String()
}

func (r *rows) ColumnTypeNullable(index int) (nullable, ok bool) {
	return r.reader.Schema().Field(index).Nullable, true
}

func (r *rows) ColumnTypePrecisionScale(index int) (precision, scale int64, ok bool) {
	typ := r.reader.Schema().Field(index).Type
	switch dt := typ.(type) {
	case *arrow.Decimal128Type:
		return int64(dt.Precision), int64(dt.Scale), true
	case *arrow.Decimal256Type:
		return int64(dt.Precision), int64(dt.Scale), true
	}
	return 0, 0, false
}

func (r *rows) ColumnTypeScanType(index int) reflect.Type {
	switch r.reader.Schema().Field(index).Type.ID() {
	case arrow.BOOL:
		return reflect.TypeOf(false)
	case arrow.INT8:
		return reflect.TypeOf(int8(0))
	case arrow.UINT8:
		return reflect.TypeOf(uint8(0))
	case arrow.INT16:
		return reflect.TypeOf(int16(0))
	case arrow.UINT16:
		return reflect.TypeOf(uint16(0))
	case arrow.INT32:
		return reflect.TypeOf(int32(0))
	case arrow.UINT32:
		return reflect.TypeOf(uint32(0))
	case arrow.INT64:
		return reflect.TypeOf(int64(0))
	case arrow.UINT64:
		return reflect.TypeOf(uint64(0))
	case arrow.FLOAT32:
		return reflect.TypeOf(float32(0))
	case arrow.FLOAT64:
		return reflect.TypeOf(float64(0))
	case arrow.DECIMAL128:
		return reflect.TypeOf(decimal128.Num{})
	case arrow.DECIMAL256:
		return reflect.TypeOf(decimal256.Num{})
	case arrow.BINARY:
		return reflect.TypeOf([]byte{})
	case arrow.STRING:
		return reflect.TypeOf(string(""))
	case arrow.TIME32, arrow.TIME64, arrow.DATE32, arrow.DATE64, arrow.TIMESTAMP:
		return reflect.TypeOf(time.Time{})
	}
	return nil
}
