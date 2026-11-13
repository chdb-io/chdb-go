//go:build cgo && (darwin || linux)

package chdbarrow

import (
	"context"
	"errors"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/chdb-io/chdb-go/v2/chdb"
	"github.com/chdb-io/chdb-go/v2/internal/nativearrow"
)

// QueryFromReaderBatchesContext scans reader once and delivers the SQL result
// one Arrow batch at a time to consume. Neither input nor output is collected
// in a table or written to an IPC file. The caller owns reader and must keep it
// alive for the duration of this call. consume borrows each batch until it
// returns; Retain/Release it to keep it longer. A zero-row result never calls
// consume. The temporary source is unregistered before this function returns.
// Cancellation is checked at batch boundaries, not during a blocked native
// query, fetch, input Next, or consume call.
func QueryFromReaderBatchesContext(ctx context.Context, sess *chdb.Session, reader array.RecordReader, build func(string) string, consume func(arrow.RecordBatch) error) (err error) {
	if reader == nil || build == nil || consume == nil {
		return errors.New("chdbarrow: nil reader, query builder or batch consumer")
	}
	source, err := RegisterReaderContext(ctx, sess, reader)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	return source.QueryBatchesContext(ctx, build, consume)
}

// QueryBatchesContext executes one synchronous query over this one-shot source
// and delivers typed results a batch at a time. The caller must Close the
// source afterward. The consumer must not close the source or session inside
// the callback; the source holds its query lock until the callback returns.
// An error from consume stops the scan and cancels the native result handle.
// Batches are borrowed until consume returns; Retain/Release to keep one.
func (s *Source) QueryBatchesContext(ctx context.Context, build func(string) string, consume func(arrow.RecordBatch) error) error {
	if consume == nil {
		return errors.New("chdbarrow: nil batch consumer")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return s.run(build, func(conn nativearrow.Connection, query string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := requireArrowEngine(conn.Capabilities(), true, false, true); err != nil {
			return err
		}
		stream, err := conn.QueryArrowStream(query, nil)
		if err != nil {
			return err
		}
		return scanBatches(ctx, stream, fetchOneBatch, consume)
	})
}

type batchFetcher func(nativearrow.Stream, *arrow.Schema) (*arrow.Schema, arrow.RecordBatch, bool, error)

// scanBatches owns the result stream. Keep it inside the Source's synchronous
// query scope: Close must not unregister the input while native Fetch runs.
func scanBatches(ctx context.Context, stream nativearrow.Stream, fetch batchFetcher, consume func(arrow.RecordBatch) error) (err error) {
	if stream == nil {
		return errors.New("chdbarrow: engine returned a nil Arrow result stream")
	}
	eof := false
	defer func() { stream.Close(!eof) }()
	var schema *arrow.Schema
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var batch arrow.RecordBatch
		var done bool
		schema, batch, done, err = fetch(stream, schema)
		if err != nil {
			if batch != nil {
				batch.Release()
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			if batch != nil {
				batch.Release()
			}
			return err
		}
		if done {
			if batch != nil {
				batch.Release()
			}
			eof = true
			return nil
		}
		if batch == nil {
			return errors.New("chdbarrow: fetched a nil Arrow batch before EOF")
		}
		callErr := func() error {
			defer batch.Release()
			return consume(batch)
		}()
		if callErr != nil {
			return callErr
		}
	}
}
