//go:build cgo && (darwin || linux)

package chdbarrow

import (
	"context"
	"sync"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/chdb-io/chdb-go/v2/chdb"
	"github.com/chdb-io/chdb-go/v2/internal/nativearrow"
)

// ResultReader returns typed Arrow batches incrementally. The first fetch is
// performed by QueryReader to establish Schema even for an empty result. The
// current batch is borrowed until the next Next or Close; call Retain on the
// batch to keep it longer. Reader Retain/Release follow the Arrow Go interface.
// Close is an explicit force-close (also used by Session.Close), even when
// the reader has additional references; the underlying connection cannot
// outlive its Session.
//
// Cancellation is cooperative at Next boundaries: Close waits for an active
// native fetch, then cancels and destroys the handle. It does not promise to
// interrupt a blocked fetch from a second goroutine, since concurrent calls
// on one native connection are outside the current core contract.
type ResultReader struct {
	mu      sync.Mutex
	refs    int
	ctx     context.Context
	lease   *nativearrow.Lease
	stream  nativearrow.Stream
	schema  *arrow.Schema
	pending arrow.RecordBatch
	current arrow.RecordBatch
	err     error
	eof     bool
	closed  bool
}

var _ array.RecordReader = (*ResultReader)(nil)

// QueryReader begins a streaming typed query on sess. It holds a Session
// resource lease until EOF or Close. It does not use Arrow IPC.
func QueryReader(ctx context.Context, sess *chdb.Session, sql string) (*ResultReader, error) {
	return queryReaderWithOptionsParams(ctx, sess, sql, nil, nil)
}

// QueryReaderParams streams a typed result with server-side named bindings.
func QueryReaderParams(ctx context.Context, sess *chdb.Session, sql string, params []Parameter) (*ResultReader, error) {
	return queryReaderWithOptionsParams(ctx, sess, sql, nil, params)
}

// QueryReaderWithOptions streams typed batches using explicit core type
// mapping. It retains the same cooperative cancellation semantics as
// QueryReader.
func QueryReaderWithOptions(ctx context.Context, sess *chdb.Session, sql string, options OutputOptions) (*ResultReader, error) {
	return queryReaderWithOptionsParams(ctx, sess, sql, options.native(), nil)
}

func queryReaderWithOptionsParams(ctx context.Context, sess *chdb.Session, sql string, options *nativearrow.OutputOptions, params []Parameter) (*ResultReader, error) {
	bindings, err := nativeParams(params)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r := &ResultReader{ctx: ctx, refs: 1}
	r.mu.Lock() // Session.Close cannot see a half-initialized reader
	lease, err := sess.AcquireNativeArrow(func() { _ = r.Close() })
	if err != nil {
		r.mu.Unlock()
		return nil, err
	}
	r.lease = lease
	if err = requireArrowEngine(lease.Conn.Capabilities(), false, false, true); err != nil {
		r.err = err
		r.closeLocked()
		r.mu.Unlock()
		return nil, err
	}
	if err = requireParamBinding(lease.Conn.Capabilities(), bindings); err != nil {
		r.err = err
		r.closeLocked()
		r.mu.Unlock()
		return nil, err
	}
	r.stream, err = lease.Conn.QueryArrowStreamParams(sql, options, bindings)
	if err == nil {
		r.schema, r.pending, r.eof, err = fetchOneBatch(r.stream, nil)
	}
	if err != nil {
		r.err = err
		r.closeLocked()
		r.mu.Unlock()
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		r.err = err
		r.closeLocked()
		r.mu.Unlock()
		return nil, err
	}
	if r.eof {
		r.closeLocked()
	}
	r.mu.Unlock()
	return r, nil
}

func (r *ResultReader) Schema() *arrow.Schema {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.schema
}

func (r *ResultReader) Next() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.eof {
		return false
	}
	if r.current != nil {
		r.current.Release()
		r.current = nil
	}
	if err := r.ctx.Err(); err != nil {
		r.err = err
		r.closeLocked()
		return false
	}
	if r.pending != nil {
		r.current = r.pending
		r.pending = nil
		return true
	}
	var batch arrow.RecordBatch
	var err error
	r.schema, batch, r.eof, err = fetchOneBatch(r.stream, r.schema)
	if err != nil {
		r.err = err
		r.closeLocked()
		return false
	}
	if err := r.ctx.Err(); err != nil {
		if batch != nil {
			batch.Release()
		}
		r.err = err
		r.closeLocked()
		return false
	}
	if r.eof {
		r.closeLocked()
		return false
	}
	r.current = batch
	return true
}

func (r *ResultReader) RecordBatch() arrow.RecordBatch {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current
}

// Record is the deprecated Arrow Go alias of RecordBatch.
func (r *ResultReader) Record() arrow.Record { return r.RecordBatch() }

func (r *ResultReader) Retain() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.refs++
	}
}

func (r *ResultReader) Release() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.refs--
	if r.refs == 0 {
		r.closeLocked()
	}
}

func (r *ResultReader) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *ResultReader) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeLocked()
	return nil
}

// closeLocked retires the handle before releasing the Session lease. A query
// already at EOF must not be cancelled: older cores could walk retired state.
func (r *ResultReader) closeLocked() {
	if r.closed {
		return
	}
	r.closed = true
	if r.pending != nil {
		r.pending.Release()
		r.pending = nil
	}
	if r.current != nil {
		r.current.Release()
		r.current = nil
	}
	if r.stream != nil {
		r.stream.Close(!r.eof)
		r.stream = nil
	}
	if r.lease != nil {
		r.lease.Release()
		r.lease = nil
	}
}
