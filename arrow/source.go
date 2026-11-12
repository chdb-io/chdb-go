//go:build cgo && (darwin || linux)

package chdbarrow

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/cdata"
	"github.com/chdb-io/chdb-go/v2/chdb"
	"github.com/chdb-io/chdb-go/v2/internal/nativearrow"
)

var (
	ErrSourceClosed   = errors.New("chdbarrow: source is closed")
	ErrSourceConsumed = errors.New("chdbarrow: Arrow reader can only be scanned once")
)

// Source is a one-shot, in-memory SQL source registered on one Session. Query
// is the only supported way to scan it: the generated name is process-wide in
// core, but this module can only track queries made through this Source. Close
// waits for such a query, unregisters, then releases the borrowed C Stream.
// Session.Close also closes any live Sources. Callers must not use an expression
// captured from Query's callback in another connection or after Query returns.
type Source struct {
	mu       sync.Mutex
	lease    *nativearrow.Lease
	name     string
	stream   unsafe.Pointer
	view     *cdata.CArrowArrayStream
	copying  *copyReader
	used     bool
	closed   bool
	closeErr error
}

// RegisterBatch accepts a standard Arrow Go batch and returns a closable
// source. The caller retains ownership of batch.
func RegisterBatch(sess *chdb.Session, batch arrow.RecordBatch) (*Source, error) {
	if batch == nil {
		return nil, errors.New("chdbarrow: nil batch")
	}
	reader, err := array.NewRecordReader(batch.Schema(), []arrow.RecordBatch{batch})
	if err != nil {
		return nil, err
	}
	defer reader.Release()
	return RegisterReader(sess, reader)
}

// RegisterReader borrows a standard Arrow Go reader for one incremental scan.
// Each requested batch is copied into C-owned memory before core can retain
// it. The source retains the reader until Close; callers must not call Next on
// that reader concurrently. Supported input types are primitive booleans,
// integers, floats, Date32, timestamp, Decimal128, binary and UTF-8, including nullable
// columns.
func RegisterReader(sess *chdb.Session, reader array.RecordReader) (*Source, error) {
	return RegisterReaderContext(context.Background(), sess, reader)
}

// RegisterReaderContext checks cancellation before each producer Next. A
// blocked user-defined Next is not forcibly interrupted; its implementation
// may itself observe ctx to provide a stronger cancellation guarantee.
func RegisterReaderContext(ctx context.Context, sess *chdb.Session, reader array.RecordReader) (*Source, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if reader == nil {
		return nil, errors.New("chdbarrow: nil reader")
	}
	if err := supported(reader.Schema()); err != nil {
		return nil, err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	s := &Source{name: "chdb_go_" + hex.EncodeToString(token[:])}
	s.copying = newCopyReader(ctx, reader)
	s.stream = C.calloc(1, C.size_t(unsafe.Sizeof(cdata.CArrowArrayStream{})))
	if s.stream == nil {
		s.copying.Release()
		return nil, errors.New("chdbarrow: allocate Arrow C Stream")
	}
	s.view = (*cdata.CArrowArrayStream)(s.stream)
	cdata.ExportRecordReader(s.copying, s.view)
	s.copying.Release() // exported stream holds the last reference

	s.mu.Lock() // a concurrent Session.Close cannot observe partial setup
	lease, err := sess.AcquireNativeArrow(func() { _ = s.Close() })
	if err != nil {
		s.releaseStream()
		s.mu.Unlock()
		return nil, err
	}
	s.lease = lease
	if err := requireArrowEngine(lease.Conn.Capabilities(), true, false, false); err != nil {
		s.releaseStream()
		lease.Release()
		s.lease = nil
		s.mu.Unlock()
		return nil, err
	}
	if err := lease.Conn.RegisterStream(s.name, s.stream); err != nil {
		s.releaseStream() // failed registration never transfers ownership
		lease.Release()
		s.lease = nil
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()
	return s, nil
}

// Query executes exactly one synchronous SQL statement that scans this source
// at most once. build receives an arrowstream('...') expression valid only for
// this call. This method does not support an asynchronous/lazy result; use
// QueryReader separately for queries without a Source dependency.
func (s *Source) Query(build func(source string) string, format string) ([]byte, error) {
	if format == "" {
		format = "TSV"
	}
	var out []byte
	err := s.run(build, func(conn nativearrow.Connection, query string) error {
		var callErr error
		out, callErr = conn.QueryBytes(query, format)
		return callErr
	})
	return out, err
}

// QueryTable scans this one-shot source and returns a typed Arrow table. The
// caller must Release the table. It follows the same tracked, synchronous
// lifetime as Query, so Close cannot unregister during the SQL scan.
func (s *Source) QueryTable(build func(source string) string) (arrow.Table, error) {
	var table arrow.Table
	err := s.run(build, func(conn nativearrow.Connection, query string) error {
		var callErr error
		table, callErr = queryTable(conn, query, nil)
		return callErr
	})
	if err != nil && table != nil {
		table.Release()
		table = nil
	}
	return table, err
}

// QueryTableWithOptions is QueryTable with explicit output type mapping.
func (s *Source) QueryTableWithOptions(build func(string) string, options OutputOptions) (arrow.Table, error) {
	var table arrow.Table
	err := s.run(build, func(conn nativearrow.Connection, query string) error {
		var callErr error
		table, callErr = queryTable(conn, query, options.native())
		return callErr
	})
	if err != nil && table != nil {
		table.Release()
		table = nil
	}
	return table, err
}

func (s *Source) run(build func(string) string, execute func(nativearrow.Connection, string) error) error {
	if s == nil || build == nil {
		return errors.New("chdbarrow: nil source or query builder")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSourceClosed
	}
	if s.used {
		return ErrSourceConsumed
	}
	s.used = true
	query := build("arrowstream('" + s.name + "')")
	if query == "" {
		return errors.New("chdbarrow: empty query")
	}
	err := execute(s.lease.Conn, query)
	if inputErr := s.copying.Err(); inputErr != nil {
		err = errors.Join(err, inputErr)
	}
	return err
}

// Close is idempotent. If core refuses to unregister, it returns the error
// and intentionally retains the C Stream: leaking a still-borrowed pointer is
// safer than releasing memory that another native scan might still access.
func (s *Source) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	if s.lease != nil {
		s.closeErr = s.lease.Conn.UnregisterStream(s.name)
		if s.closeErr == nil {
			s.releaseStream()
		}
		s.lease.Release()
		s.lease = nil
	}
	return s.closeErr
}

func (s *Source) releaseStream() {
	if s.stream != nil {
		cdata.ReleaseCArrowArrayStream(s.view)
		C.free(s.stream)
		s.stream = nil
		s.view = nil
	}
}
