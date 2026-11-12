//go:build cgo && (darwin || linux)

package chdbarrow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/chdb-io/chdb-go/v2/chdb"
)

// InsertBatch inserts a single Arrow batch into an existing table via the
// same short-lived, in-memory source as QueryBatch. The destination may be a
// single identifier or database.table. For this first slice names are limited
// to ASCII letters, digits and underscores, starting with a letter or
// underscore; no SQL fragment can be supplied as an identifier. Schema column
// names are explicitly projected in batch order. The caller manages its batch.
func InsertBatch(sess *chdb.Session, table string, batch arrow.RecordBatch) error {
	if batch == nil {
		return errors.New("chdbarrow: nil batch")
	}
	reader, err := array.NewRecordReader(batch.Schema(), []arrow.RecordBatch{batch})
	if err != nil {
		return err
	}
	defer reader.Release()
	return InsertReader(sess, table, reader)
}

// InsertReader incrementally consumes one Arrow Go reader into an existing
// table. It uses a private, one-shot source and always closes it before return.
// Columns are matched explicitly by name, not by their accidental position in
// the destination. No persistent staging table or Arrow IPC is used.
func InsertReader(sess *chdb.Session, table string, reader array.RecordReader) (err error) {
	return InsertReaderContext(context.Background(), sess, table, reader)
}

// InsertReaderContext checks cancellation at each input-batch boundary. It
// does not claim that an already-running native INSERT can be rolled back or
// interrupted; callers must use their normal database transaction policy.
func InsertReaderContext(ctx context.Context, sess *chdb.Session, table string, reader array.RecordReader) (err error) {
	if reader == nil || reader.Schema() == nil {
		return errors.New("chdbarrow: nil reader or schema")
	}
	destination, err := quotePath(table)
	if err != nil {
		return err
	}
	cols := make([]string, len(reader.Schema().Fields()))
	seen := make(map[string]struct{}, len(cols))
	for i, field := range reader.Schema().Fields() {
		cols[i], err = quotePath(field.Name)
		if err != nil || strings.Contains(field.Name, ".") {
			return fmt.Errorf("chdbarrow: invalid column name %q", field.Name)
		}
		if _, duplicate := seen[field.Name]; duplicate {
			return fmt.Errorf("chdbarrow: duplicate column name %q", field.Name)
		}
		seen[field.Name] = struct{}{}
	}
	projection := strings.Join(cols, ", ")
	source, err := RegisterReaderContext(ctx, sess, reader)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	_, err = source.Query(func(source string) string {
		return "INSERT INTO " + destination + " (" + projection + ") SELECT " + projection + " FROM " + source
	}, "TSV")
	return err
}

func quotePath(path string) (string, error) {
	parts := strings.Split(path, ".")
	if len(parts) > 2 {
		return "", fmt.Errorf("chdbarrow: invalid identifier %q", path)
	}
	for i, part := range parts {
		if part == "" {
			return "", fmt.Errorf("chdbarrow: invalid identifier %q", path)
		}
		for j, ch := range part {
			if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' ||
				(j > 0 && ch >= '0' && ch <= '9') {
				continue
			}
			return "", fmt.Errorf("chdbarrow: invalid identifier %q", path)
		}
		parts[i] = "`" + part + "`"
	}
	return strings.Join(parts, "."), nil
}
