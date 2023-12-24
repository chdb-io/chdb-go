package chdb

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"

	wrapper "github.com/chdb-io/chdb-go/chdb"
	"github.com/chdb-io/chdb-go/chdbstable"

	"github.com/apache/arrow/go/v14/arrow/ipc"
)

func init() {
	sql.Register("chdb", Driver{})
}

type connector struct {
}

// Connect returns a connection to a database.
func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	return &conn{}, nil
}

// Driver returns the underying Driver of the connector,
// compatibility with the Driver method on sql.DB
func (c *connector) Driver() driver.Driver { return Driver{} }

type Driver struct{}

// Open returns a new connection to the database.
func (d Driver) Open(name string) (driver.Conn, error) {
	return &conn{}, nil
}

// OpenConnector expects the same format as driver.Open
func (d Driver) OpenConnector(dataSourceName string) (driver.Connector, error) {
	return &connector{}, nil
}

type conn struct {
}

func (c *conn) Close() error {
	return nil
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
	result := wrapper.Query(query, "Arrow")
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
}

func (r *rows) Columns() (out []string) {
	sch := r.reader.Schema()
	for i := 0; i < sch.NumFields(); i++ {
		out = append(out, sch.Field(i).Name)
	}
	return
}

func (r *rows) Close() error {
	return nil
}

func (r *rows) Next(dest []driver.Value) error {
	return nil
}

func (r *rows) ColumnTypeDatabaseTypeName(index int) string {
	return ""
}

func (r *rows) ColumnTypeNullable(index int) (nullable, ok bool) {
	return
}

func (r *rows) ColumnTypePrecisionScale(index int) (precision, scale int64, ok bool) {
	return
}

func (r *rows) ColumnTypeScanType(index int) reflect.Type {
	return reflect.TypeOf(nil)
}
