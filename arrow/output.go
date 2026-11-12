//go:build cgo && (darwin || linux)

package chdbarrow

/*
#include <stdint.h>
#include <stdlib.h>

struct ArrowSchema {
	const char *format, *name, *metadata;
	int64_t flags, n_children;
	struct ArrowSchema **children, *dictionary;
	void (*release)(struct ArrowSchema *);
	void *private_data;
};
struct ArrowArray {
	int64_t length, null_count, offset, n_buffers, n_children;
	const void **buffers;
	struct ArrowArray **children, *dictionary;
	void (*release)(struct ArrowArray *);
	void *private_data;
};
struct ArrowArrayStream {
	int (*get_schema)(struct ArrowArrayStream *, struct ArrowSchema *);
	int (*get_next)(struct ArrowArrayStream *, struct ArrowArray *);
	const char *(*get_last_error)(struct ArrowArrayStream *);
	void (*release)(struct ArrowArrayStream *);
	void *private_data;
};
static int chdb_get_schema(struct ArrowArrayStream *s, struct ArrowSchema *out) {
	return s->get_schema(s, out);
}
static int chdb_get_next(struct ArrowArrayStream *s, struct ArrowArray *out) {
	return s->get_next(s, out);
}
static const char *chdb_last_error(struct ArrowArrayStream *s) {
	return s->get_last_error ? s->get_last_error(s) : NULL;
}
static int chdb_stream_valid(struct ArrowArrayStream *s) {
	return s->release && s->get_schema && s->get_next;
}
static void chdb_release_stream(struct ArrowArrayStream *s) {
	if (s->release) s->release(s);
}
static void chdb_release_schema(struct ArrowSchema *s) {
	if (s->release) s->release(s);
}
static void chdb_release_array(struct ArrowArray *a) {
	if (a->release) a->release(a);
}
static int chdb_array_valid(struct ArrowArray *a) { return a->release != NULL; }
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/cdata"
	"github.com/chdb-io/chdb-go/v2/chdb"
	"github.com/chdb-io/chdb-go/v2/internal/nativearrow"
)

// QueryTable materializes a typed Arrow table directly from the native C Data
// result, without Arrow IPC. The caller owns and must Release the returned
// table. Native stream, schema, and result handles are deterministically
// released before this call returns, including on errors. This is not a
// streaming query; use QueryReader for incrementally fetched results.
func QueryTable(sess *chdb.Session, sql string) (table arrow.Table, err error) {
	return queryTableOnSession(sess, sql, nil)
}

// QueryTableWithOptions materializes typed Arrow results with explicit core
// type mapping. The caller owns the returned Table and must Release it.
func QueryTableWithOptions(sess *chdb.Session, sql string, options OutputOptions) (arrow.Table, error) {
	return queryTableOnSession(sess, sql, options.native())
}

func queryTableOnSession(sess *chdb.Session, sql string, options *nativearrow.OutputOptions) (table arrow.Table, err error) {
	err = sess.WithNativeArrow(func(conn nativearrow.Connection) error {
		table, err = queryTable(conn, sql, options)
		return err
	})
	return table, err
}

func queryTable(conn nativearrow.Connection, sql string, options *nativearrow.OutputOptions) (arrow.Table, error) {
	if err := requireArrowEngine(conn.Capabilities(), false, true, false); err != nil {
		return nil, err
	}
	stream := (*C.struct_ArrowArrayStream)(C.calloc(1, C.size_t(C.sizeof_struct_ArrowArrayStream)))
	if stream == nil {
		return nil, errors.New("chdbarrow: allocate output stream")
	}
	defer C.free(unsafe.Pointer(stream))
	defer C.chdb_release_stream(stream)
	if err := conn.QueryArrow(sql, unsafe.Pointer(stream), options); err != nil {
		return nil, err
	}
	if C.chdb_stream_valid(stream) == 0 {
		return nil, errors.New("chdbarrow: engine returned an invalid Arrow stream")
	}
	schemaPtr := (*C.struct_ArrowSchema)(C.calloc(1, C.size_t(C.sizeof_struct_ArrowSchema)))
	if schemaPtr == nil {
		return nil, errors.New("chdbarrow: allocate Arrow schema")
	}
	defer C.free(unsafe.Pointer(schemaPtr))
	defer C.chdb_release_schema(schemaPtr)
	if C.chdb_get_schema(stream, schemaPtr) != 0 {
		return nil, streamError(stream, "get_schema")
	}
	schema, err := cdata.ImportCArrowSchema((*cdata.CArrowSchema)(unsafe.Pointer(schemaPtr)))
	if err != nil {
		return nil, fmt.Errorf("chdbarrow: import schema: %w", err)
	}
	var batches []arrow.RecordBatch
	defer func() {
		for _, batch := range batches {
			batch.Release()
		}
	}()
	for {
		arr := (*C.struct_ArrowArray)(C.calloc(1, C.size_t(C.sizeof_struct_ArrowArray)))
		if arr == nil {
			return nil, errors.New("chdbarrow: allocate Arrow array")
		}
		rc := C.chdb_get_next(stream, arr)
		if rc != 0 {
			C.chdb_release_array(arr)
			C.free(unsafe.Pointer(arr))
			return nil, streamError(stream, "get_next")
		}
		if C.chdb_array_valid(arr) == 0 {
			C.free(unsafe.Pointer(arr))
			break
		}
		batch, importErr := cdata.ImportCRecordBatchWithSchema(
			(*cdata.CArrowArray)(unsafe.Pointer(arr)), schema)
		// On success Arrow Go moves ownership out of arr; on failure
		// release the still-owned C array explicitly.
		C.chdb_release_array(arr)
		C.free(unsafe.Pointer(arr))
		if importErr != nil {
			return nil, fmt.Errorf("chdbarrow: import batch: %w", importErr)
		}
		batches = append(batches, batch)
	}
	return array.NewTableFromRecords(schema, batches), nil
}

func streamError(s *C.struct_ArrowArrayStream, call string) error {
	if msg := C.chdb_last_error(s); msg != nil {
		return fmt.Errorf("chdbarrow: Arrow %s: %s", call, C.GoString(msg))
	}
	return fmt.Errorf("chdbarrow: Arrow %s failed", call)
}

// fetchOneBatch turns one native streaming fetch into a retained Arrow Go
// record. The per-fetch C stream is released before return, while the moved
// ArrowArray owns the batch's buffers. EOF still returns the schema so a
// zero-row result has a typed shape.
func fetchOneBatch(stream nativearrow.Stream, previous *arrow.Schema) (schema *arrow.Schema, batch arrow.RecordBatch, eof bool, err error) {
	out := (*C.struct_ArrowArrayStream)(C.calloc(1, C.size_t(C.sizeof_struct_ArrowArrayStream)))
	if out == nil {
		return nil, nil, false, errors.New("chdbarrow: allocate batch stream")
	}
	defer C.free(unsafe.Pointer(out))
	defer C.chdb_release_stream(out)
	if err := stream.Fetch(unsafe.Pointer(out)); err != nil {
		return nil, nil, false, err
	}
	if C.chdb_stream_valid(out) == 0 {
		return nil, nil, false, errors.New("chdbarrow: invalid fetched Arrow stream")
	}
	schemaPtr := (*C.struct_ArrowSchema)(C.calloc(1, C.size_t(C.sizeof_struct_ArrowSchema)))
	if schemaPtr == nil {
		return nil, nil, false, errors.New("chdbarrow: allocate batch schema")
	}
	defer C.free(unsafe.Pointer(schemaPtr))
	defer C.chdb_release_schema(schemaPtr)
	if C.chdb_get_schema(out, schemaPtr) != 0 {
		return nil, nil, false, streamError(out, "get_schema")
	}
	schema, err = cdata.ImportCArrowSchema((*cdata.CArrowSchema)(unsafe.Pointer(schemaPtr)))
	if err != nil {
		return nil, nil, false, fmt.Errorf("chdbarrow: import batch schema: %w", err)
	}
	if previous != nil && !previous.Equal(schema) {
		return nil, nil, false, errors.New("chdbarrow: Arrow schema changed between batches")
	}
	arr := (*C.struct_ArrowArray)(C.calloc(1, C.size_t(C.sizeof_struct_ArrowArray)))
	if arr == nil {
		return nil, nil, false, errors.New("chdbarrow: allocate batch array")
	}
	defer C.free(unsafe.Pointer(arr))
	defer C.chdb_release_array(arr)
	if C.chdb_get_next(out, arr) != 0 {
		return nil, nil, false, streamError(out, "get_next")
	}
	if C.chdb_array_valid(arr) == 0 {
		return schema, nil, true, nil
	}
	batch, err = cdata.ImportCRecordBatchWithSchema((*cdata.CArrowArray)(unsafe.Pointer(arr)), schema)
	if err != nil {
		return nil, nil, false, fmt.Errorf("chdbarrow: import batch: %w", err)
	}
	return schema, batch, false, nil
}
