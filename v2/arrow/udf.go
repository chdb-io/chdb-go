//go:build cgo && (darwin || linux)

package chdbarrow

/*
#include <stdint.h>
#include <stdlib.h>

// Mirrors chdb_scalar_function_options in chdb.h; function pointers are kept
// as void * of the same size.
typedef struct chdb_go_scalar_function_options {
	size_t struct_size;
	const char * const * argument_types;
	size_t argument_count;
	const char * return_type;
	uint32_t flags;
	void * callback;
	void * user_data;
	void * destroy;
} chdb_go_scalar_function_options;

enum {
	CHDB_GO_SCALAR_FUNCTION_VOLATILE = 1u << 0,
	CHDB_GO_SCALAR_FUNCTION_SPECIAL_NULL_HANDLING = 1u << 1,
	CHDB_GO_SCALAR_FUNCTION_BINARY_STRINGS = 1u << 2,
};

int chdbGoScalarFunctionCall(void *, void *, void *, void *, void *, char *, size_t);
void chdbGoScalarFunctionDestroy(void *);
typedef int (*chdb_go_scalar_function_call)(void *, void *, void *, void *, void *, char *, size_t);
typedef void (*chdb_go_scalar_function_destroy)(void *);
*/
import "C"

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"runtime/cgo"
	"sync/atomic"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/cdata"
	"github.com/chdb-io/chdb-go/v2/chdb"
	"github.com/chdb-io/chdb-go/v2/internal/nativearrow"
)

// ErrScalarUDFUnavailable reports a libchdb without the scalar function ABI
// (chdb_create_scalar_function). Everything else in this module keeps working.
var ErrScalarUDFUnavailable = errors.New("chdbarrow: loaded libchdb cannot register scalar functions")

// ScalarFuncConfig describes a scalar function to chDB.
type ScalarFuncConfig struct {
	// InputTypes are the ClickHouse types of the arguments, e.g. "Int64" or
	// "Array(String)". An argument of another type is accepted when it converts
	// without loss, e.g. an Int32 for an Int64, and arrives converted.
	InputTypes []string
	// ResultType is the ClickHouse type of the result, e.g. "String" or
	// "Nullable(Float64)". Whatever the executor returns is converted to it;
	// a value that does not fit is an error.
	ResultType string
	// Volatile marks a function that can return different results for the same
	// arguments, like rand(). Otherwise chDB may evaluate a call with constant
	// arguments once for all rows.
	Volatile bool
	// SpecialNullHandling passes NULL arguments to the executor, as nil or as
	// Arrow nulls, and keeps ResultType as is. By default a row with a NULL
	// argument yields NULL without reaching the executor, and the result
	// becomes Nullable when an argument can be NULL.
	SpecialNullHandling bool
	// BinaryStrings passes String arguments as []byte and Arrow binary instead
	// of string and Arrow UTF-8, for values that are not valid UTF-8.
	BinaryStrings bool
}

type (
	// RowExecutorFn computes the result of one row from its argument values.
	// The values slice is reused for the next row; copy what you keep.
	RowExecutorFn func(values []driver.Value) (any, error)
	// BatchExecutorFn computes the results of a whole batch: one array with a
	// value per row of args, whose columns are the arguments. args is released
	// after the call; retain what you keep.
	BatchExecutorFn func(args arrow.RecordBatch) (arrow.Array, error)
)

// ScalarFuncExecutor holds the functions that compute a scalar function. Its
// first non-nil member is used.
type ScalarFuncExecutor struct {
	// RowExecutor is called once per row, with Go values: nil for NULL, bool,
	// int8 ... uint64, float32, float64, string, []byte, time.Time (Date,
	// DateTime and DateTime64), string for Decimal, UUID, Enum and 128/256-bit
	// integers, []any for Array, map[string]any for Tuple and map[any]any for
	// Map. It returns nil for NULL or one of bool, an integer, a float, string,
	// []byte or time.Time; chDB converts that to ResultType, parsing a string
	// where needed, so a Decimal or a UUID result can be returned as a string.
	RowExecutor RowExecutorFn
	// BatchExecutor is called once per batch of rows with the arguments as
	// Arrow arrays. It may return an array of any Arrow type that converts to
	// ResultType.
	BatchExecutor BatchExecutorFn
}

// ScalarFunc is a scalar user-defined function. chDB calls its executor from
// several threads at once, so the executor must be safe for concurrent use.
// It runs inside a query and must not use chDB itself.
type ScalarFunc interface {
	Config() ScalarFuncConfig
	Executor() ScalarFuncExecutor
}

// liveScalarFunctions counts the registrations chDB has not destroyed yet.
var liveScalarFunctions atomic.Int64

// scalarFunction is what a registration's cgo handle refers to.
type scalarFunction struct {
	row   RowExecutorFn
	batch BatchExecutorFn
}

// RegisterScalarUDF registers f under name, callable from SQL in every chDB
// session of this process until DropScalarUDF, including sessions opened
// later. The name must not be taken by a built-in function or another
// user-defined function.
func RegisterScalarUDF(sess *chdb.Session, name string, f ScalarFunc) error {
	if name == "" {
		return errors.New("chdbarrow: empty scalar function name")
	}
	if f == nil {
		return errors.New("chdbarrow: nil scalar function")
	}
	config, executor := f.Config(), f.Executor()
	if executor.RowExecutor == nil && executor.BatchExecutor == nil {
		return fmt.Errorf("chdbarrow: scalar function %q has no executor", name)
	}
	if config.ResultType == "" {
		return fmt.Errorf("chdbarrow: scalar function %q has no result type", name)
	}
	return sess.WithNativeArrow(func(conn nativearrow.Connection) error {
		if c := conn.Capabilities(); !c.ScalarFunctions {
			return fmt.Errorf("%w; loaded %q", ErrScalarUDFUnavailable, c.EngineVersion)
		}
		return registerScalarFunction(conn, name, config, &scalarFunction{
			row:   executor.RowExecutor,
			batch: executor.BatchExecutor,
		})
	})
}

func registerScalarFunction(conn nativearrow.Connection, name string, config ScalarFuncConfig, fn *scalarFunction) error {
	options := (*C.chdb_go_scalar_function_options)(C.calloc(1, C.sizeof_chdb_go_scalar_function_options))
	if options == nil {
		return errors.New("chdbarrow: allocate scalar function options")
	}
	defer C.free(unsafe.Pointer(options))

	cStrings := make([]*C.char, 0, len(config.InputTypes)+1)
	defer func() {
		for _, s := range cStrings {
			C.free(unsafe.Pointer(s))
		}
	}()
	if n := len(config.InputTypes); n > 0 {
		types := (**C.char)(C.calloc(C.size_t(n), C.size_t(unsafe.Sizeof((*C.char)(nil)))))
		if types == nil {
			return errors.New("chdbarrow: allocate scalar function argument types")
		}
		defer C.free(unsafe.Pointer(types))
		view := unsafe.Slice(types, n)
		for i, t := range config.InputTypes {
			view[i] = C.CString(t)
			cStrings = append(cStrings, view[i])
		}
		options.argument_types = types
		options.argument_count = C.size_t(n)
	}
	options.return_type = C.CString(config.ResultType)
	cStrings = append(cStrings, options.return_type)

	var flags C.uint32_t
	if config.Volatile {
		flags |= C.CHDB_GO_SCALAR_FUNCTION_VOLATILE
	}
	if config.SpecialNullHandling {
		flags |= C.CHDB_GO_SCALAR_FUNCTION_SPECIAL_NULL_HANDLING
	}
	if config.BinaryStrings {
		flags |= C.CHDB_GO_SCALAR_FUNCTION_BINARY_STRINGS
	}
	options.flags = flags
	options.struct_size = C.sizeof_chdb_go_scalar_function_options
	options.callback = unsafe.Pointer(C.chdb_go_scalar_function_call(C.chdbGoScalarFunctionCall))
	options.destroy = unsafe.Pointer(C.chdb_go_scalar_function_destroy(C.chdbGoScalarFunctionDestroy))

	// The handle travels in C memory: chDB keeps user_data after this call,
	// and a handle is an integer the Go runtime must never see as a pointer.
	userData := C.malloc(C.size_t(unsafe.Sizeof(C.uintptr_t(0))))
	if userData == nil {
		return errors.New("chdbarrow: allocate scalar function state")
	}
	handle := cgo.NewHandle(fn)
	*(*C.uintptr_t)(userData) = C.uintptr_t(handle)
	options.user_data = userData

	// Counted first: the function can be dropped, and destroyed, as soon as it is registered.
	liveScalarFunctions.Add(1)
	if err := conn.CreateScalarFunction(name, unsafe.Pointer(options)); err != nil {
		// A failed registration leaves user_data with the caller.
		liveScalarFunctions.Add(-1)
		handle.Delete()
		C.free(userData)
		return err
	}
	return nil
}

// DropScalarUDF unregisters a function registered with RegisterScalarUDF. A
// query already using it finishes with it.
func DropScalarUDF(sess *chdb.Session, name string) error {
	return sess.WithNativeArrow(func(conn nativearrow.Connection) error {
		if c := conn.Capabilities(); !c.ScalarFunctions {
			return fmt.Errorf("%w; loaded %q", ErrScalarUDFUnavailable, c.EngineVersion)
		}
		return conn.DropScalarFunction(name)
	})
}

//export chdbGoScalarFunctionCall
func chdbGoScalarFunctionCall(userData, argumentSchema, arguments, resultSchema, result unsafe.Pointer, errBuf *C.char, errSize C.size_t) (code C.int) {
	// A panic must not unwind into chDB's C++ frames.
	defer func() {
		if r := recover(); r != nil {
			code = writeCallbackError(errBuf, errSize, fmt.Errorf("panic: %v", r))
		}
	}()
	fn := cgo.Handle(*(*C.uintptr_t)(userData)).Value().(*scalarFunction)
	out, err := fn.call((*cdata.CArrowSchema)(argumentSchema), (*cdata.CArrowArray)(arguments))
	if err != nil {
		return writeCallbackError(errBuf, errSize, err)
	}
	// The export holds its own reference until chDB releases the result.
	defer out.Release()
	cdata.ExportArrowArray(out, (*cdata.CArrowArray)(result), (*cdata.CArrowSchema)(resultSchema))
	return 0
}

//export chdbGoScalarFunctionDestroy
func chdbGoScalarFunctionDestroy(userData unsafe.Pointer) {
	cgo.Handle(*(*C.uintptr_t)(userData)).Delete()
	C.free(userData)
	liveScalarFunctions.Add(-1)
}

// call computes one batch. The result lives in C memory, because chDB keeps
// it after the callback returns.
func (f *scalarFunction) call(schema *cdata.CArrowSchema, arguments *cdata.CArrowArray) (arrow.Array, error) {
	// On failure the arguments stay with chDB, which releases them.
	args, err := cdata.ImportCRecordBatch(arguments, schema)
	if err != nil {
		return nil, fmt.Errorf("import arguments: %w", err)
	}
	defer args.Release()

	if f.batch == nil {
		return f.rows(args)
	}
	out, err := f.batch(args)
	if err != nil {
		if out != nil {
			out.Release()
		}
		return nil, err
	}
	if out == nil {
		return nil, errors.New("BatchExecutor returned a nil array")
	}
	defer out.Release()
	if int64(out.Len()) != args.NumRows() {
		return nil, fmt.Errorf("BatchExecutor returned %d values for %d rows", out.Len(), args.NumRows())
	}
	return array.Concatenate([]arrow.Array{out}, cAllocator{})
}

// rows computes one batch with the RowExecutor.
func (f *scalarFunction) rows(args arrow.RecordBatch) (arrow.Array, error) {
	readers := make([]valueReader, args.NumCols())
	for i, column := range args.Columns() {
		reader, err := newValueReader(column, args.Schema().Field(i))
		if err != nil {
			return nil, fmt.Errorf("argument %d: %w", i+1, err)
		}
		readers[i] = reader
	}
	rows := int(args.NumRows())
	values := make([]driver.Value, len(readers))
	results := make([]any, rows)
	for row := 0; row < rows; row++ {
		for i, read := range readers {
			values[i] = read(row)
		}
		result, err := f.row(values)
		if err != nil {
			return nil, err
		}
		results[row] = result
	}
	return buildResult(results, cAllocator{})
}

// writeCallbackError copies err into chDB's error buffer, truncated to fit.
func writeCallbackError(buf *C.char, size C.size_t, err error) C.int {
	if buf != nil && size > 0 {
		out := unsafe.Slice((*byte)(unsafe.Pointer(buf)), int(size))
		n := copy(out[:len(out)-1], err.Error())
		out[n] = 0
	}
	return 1
}
