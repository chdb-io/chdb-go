//go:build cgo && (darwin || linux)

package chdbarrow

import (
	"context"
	"database/sql"
	"errors"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/chdb-io/chdb-go/v2/chdb"
)

// QueryFromReaderBatchesSQLContext runs the complete Arrow pipeline on one
// database/sql pooled connection. The driver connection is borrowed only
// within Conn.Raw; input, result, and temporary source are all retired before
// it is returned to the pool. Import chdb/driver to register the SQL driver.
// The SQL uses {name:Type} placeholders, not '?' positional interpolation.
func QueryFromReaderBatchesSQLContext(ctx context.Context, conn *sql.Conn, reader array.RecordReader, build func(string) string, params []Parameter, consume func(arrow.RecordBatch) error) error {
	if conn == nil {
		return errNilSQLConn
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return conn.Raw(func(raw any) error {
		adapter, ok := raw.(interface {
			WithArrowSession(func(*chdb.Session) error) error
		})
		if !ok {
			return errors.New("chdbarrow: database/sql connection is not a chdb-go driver connection")
		}
		return adapter.WithArrowSession(func(sess *chdb.Session) error {
			return QueryFromReaderBatchesParamsContext(ctx, sess, reader, build, params, consume)
		})
	})
}
