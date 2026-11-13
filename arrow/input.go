//go:build cgo && (darwin || linux)

package chdbarrow

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/chdb-io/chdb-go/v2/chdb"
)

// QueryFromReader consumes a standard Arrow Go reader incrementally, once,
// through a temporary SQL source. A batch is copied into C-owned buffers
// only when core asks for it. The caller must not concurrently call Next on
// reader, and the SQL must scan source once; a second scan cannot replay it.
// This synchronous interface does not interrupt a blocked native query.
func QueryFromReader(sess *chdb.Session, reader array.RecordReader, sql func(source string) string, format string) (out []byte, err error) {
	return QueryFromReaderContext(context.Background(), sess, reader, sql, format)
}

// QueryFromReaderContext is QueryFromReader with cancellation checked before
// each input batch is requested. It does not interrupt a blocked native query
// or a producer's in-progress Next from another goroutine.
func QueryFromReaderContext(ctx context.Context, sess *chdb.Session, reader array.RecordReader, sql func(source string) string, format string) (out []byte, err error) {
	if reader == nil || sql == nil {
		return nil, errors.New("chdbarrow: nil reader or query builder")
	}
	source, err := RegisterReaderContext(ctx, sess, reader)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	return source.QueryContext(ctx, sql, format)
}

// copyReader owns a reference to the caller's reader and makes each batch
// safe for the long-lived Arrow C Stream before exporting it to core. Its
// Next callback is serialized by the C Stream consumer (one-shot contract).
type copyReader struct {
	refs    atomic.Int64
	ctx     context.Context
	src     array.RecordReader
	current arrow.RecordBatch
	err     error
}

func newCopyReader(ctx context.Context, src array.RecordReader) *copyReader {
	src.Retain()
	r := &copyReader{ctx: ctx, src: src}
	r.refs.Store(1)
	return r
}

func (r *copyReader) Retain() { r.refs.Add(1) }
func (r *copyReader) Release() {
	if r.refs.Add(-1) == 0 {
		if r.current != nil {
			r.current.Release()
			r.current = nil
		}
		r.src.Release()
	}
}
func (r *copyReader) Schema() *arrow.Schema          { return r.src.Schema() }
func (r *copyReader) RecordBatch() arrow.RecordBatch { return r.current }
func (r *copyReader) Record() arrow.Record           { return r.current }
func (r *copyReader) Err() error                     { return r.err }
func (r *copyReader) Next() bool {
	if r.current != nil {
		r.current.Release()
		r.current = nil
	}
	if err := r.ctx.Err(); err != nil {
		r.err = err
		return false
	}
	if !r.src.Next() {
		r.err = r.src.Err()
		return false
	}
	if err := r.ctx.Err(); err != nil {
		r.err = err
		return false
	}
	r.current, r.err = copyRecord(r.src.RecordBatch())
	return r.err == nil
}

func copyRecord(batch arrow.RecordBatch) (arrow.RecordBatch, error) {
	if batch == nil {
		return nil, errors.New("chdbarrow: reader produced nil batch")
	}
	cols := make([]arrow.Array, int(batch.NumCols()))
	defer func() {
		for _, col := range cols {
			if col != nil {
				col.Release()
			}
		}
	}()
	for i := range cols {
		var err error
		cols[i], err = array.Concatenate([]arrow.Array{batch.Column(i)}, cAllocator{})
		if err != nil {
			return nil, fmt.Errorf("chdbarrow: copy column %d: %w", i, err)
		}
	}
	return array.NewRecordBatch(batch.Schema(), cols, batch.NumRows()), nil
}
