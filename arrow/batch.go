//go:build cgo && (darwin || linux)

// Package chdbarrow is an opt-in Arrow Go integration for chDB. This first
// slice supports one-shot batch/reader input, materialized typed output, and
// incrementally fetched typed results. It does not promise zero-copy
// conversion within the engine. The main chdb-go module remains cgo-free.
package chdbarrow

/*
#include <stdlib.h>
#include <string.h>
static void *chdb_arrow_alloc(size_t n) {
	void *p = NULL;
	if (posix_memalign(&p, 64, n) != 0) return NULL;
	memset(p, 0, n);
	return p;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/chdb-io/chdb-go/v2/chdb"
)

// QueryBatch scans batch once on sess, returning a copy of the formatted SQL
// result. sql receives the temporary source expression, such as
// arrowstream('chdb_go_...'), and must return one SQL query that scans it at
// most once. The source name is process-global in core: do not reuse it from
// another connection or after QueryBatch returns. Closing sess waits for this
// entire call, including native unregister and stream release.
//
// This first slice accepts nullable primitive and binary/string columns. It copies their
// buffers into C-owned memory before exporting the Arrow C Stream, so native
// callbacks never retain Go-heap data. It does not support cancellation or
// incremental readers yet; those are separate A2/A4 milestones.
func QueryBatch(sess *chdb.Session, batch arrow.RecordBatch, sql func(source string) string, format string) (out []byte, err error) {
	if batch == nil || sql == nil {
		return nil, errors.New("chdbarrow: nil batch or query builder")
	}
	if err := supported(batch.Schema()); err != nil {
		return nil, err
	}
	reader, err := array.NewRecordReader(batch.Schema(), []arrow.RecordBatch{batch})
	if err != nil {
		return nil, err
	}
	defer reader.Release()
	return QueryFromReader(sess, reader, sql, format)
}

func supported(schema *arrow.Schema) error {
	if schema == nil || len(schema.Fields()) == 0 {
		return errors.New("chdbarrow: batch must have a nonempty schema")
	}
	for _, f := range schema.Fields() {
		switch f.Type.ID() {
		case arrow.BOOL,
			arrow.INT8, arrow.INT16, arrow.INT32, arrow.INT64,
			arrow.UINT8, arrow.UINT16, arrow.UINT32, arrow.UINT64,
			arrow.FLOAT32, arrow.FLOAT64,
			arrow.DATE32, arrow.TIMESTAMP, arrow.DECIMAL128,
			arrow.STRING, arrow.BINARY:
		default:
			return fmt.Errorf("chdbarrow: unsupported input type %s for %q", f.Type, f.Name)
		}
	}
	return nil
}

type cAllocator struct{}

func (cAllocator) Allocate(n int) []byte {
	if n == 0 {
		return nil
	}
	p := C.chdb_arrow_alloc(C.size_t(n))
	if p == nil {
		panic("chdbarrow: native allocation failed")
	}
	return unsafe.Slice((*byte)(p), n)
}

func (a cAllocator) Reallocate(n int, b []byte) []byte {
	if n == 0 {
		a.Free(b)
		return nil
	}
	out := a.Allocate(n)
	copy(out, b)
	a.Free(b)
	return out
}

func (cAllocator) Free(b []byte) {
	if len(b) != 0 {
		C.free(unsafe.Pointer(&b[0]))
	}
}
