package chdbstable

/*
#cgo LDFLAGS: -L/usr/local/lib -lchdb
#include <stdlib.h> // Include the C standard library for C.free
#include "chdb.h"
*/
import "C"
import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"
)

// ChdbError is returned when the C function returns an error.
type ChdbError struct {
	msg string
}

func (e *ChdbError) Error() string {
	return e.msg
}

// ErrNilResult is returned when the C function returns a nil pointer.
var ErrNilResult = errors.New("chDB C function returned nil pointer")

// LocalResult mirrors the C struct local_result_v2 in Go.
type LocalResult struct {
	cResult *C.struct_local_result_v2
}

type ChdbConn struct {
	conn *C.struct_chdb_conn
}

// newLocalResult creates a new LocalResult and sets a finalizer to free C memory.
func newLocalResult(cResult *C.struct_local_result_v2) *LocalResult {
	result := &LocalResult{cResult: cResult}
	runtime.SetFinalizer(result, freeLocalResult)
	return result
}

// newChdbConn creates a new ChdbConn and sets a finalizer to close the connection (and thus free the memory)
func newChdbConn(conn *C.struct_chdb_conn) *ChdbConn {
	result := &ChdbConn{conn: conn}
	runtime.SetFinalizer(result, closeChdbConn)
	return result
}

func NewConnection(argc int, argv []string) (*ChdbConn, error) {
	cArgv := make([]*C.char, len(argv))
	for i, s := range argv {
		cArgv[i] = C.CString(s)
		defer C.free(unsafe.Pointer(cArgv[i]))
	}
	conn := C.connect_chdb(C.int(argc), &cArgv[0])
	if conn == nil {
		return nil, fmt.Errorf("could not create a chdb connection")
	}
	return newChdbConn(*conn), nil
}

func closeChdbConn(conn *ChdbConn) {
	C.close_conn(&conn.conn)
}

// freeLocalResult is called by the garbage collector.
func freeLocalResult(result *LocalResult) {
	C.free_result_v2(result.cResult)
}

// QueryStable calls the C function query_stable_v2.
func QueryStable(argc int, argv []string) (result *LocalResult, err error) {
	cArgv := make([]*C.char, len(argv))
	for i, s := range argv {
		cArgv[i] = C.CString(s)
		defer C.free(unsafe.Pointer(cArgv[i]))
	}

	cResult := C.query_stable_v2(C.int(argc), &cArgv[0])
	if cResult == nil {
		// According to the C ABI of chDB v1.2.0, the C function query_stable_v2
		// returns nil if the query returns no data. This is not an error. We
		// will change this behavior in the future.
		return newLocalResult(cResult), nil
	}
	if cResult.error_message != nil {
		return nil, &ChdbError{msg: C.GoString(cResult.error_message)}
	}
	return newLocalResult(cResult), nil
}

// QueryStable calls the C function query_conn.
func (c *ChdbConn) QueryConn(queryStr string, formatStr string) (result *LocalResult, err error) {

	query := C.CString(queryStr)
	format := C.CString(formatStr)
	// free the strings in the C heap
	defer C.free(unsafe.Pointer(query))
	defer C.free(unsafe.Pointer(format))

	cResult := C.query_conn(c.conn, query, format)
	if cResult == nil {
		// According to the C ABI of chDB v1.2.0, the C function query_stable_v2
		// returns nil if the query returns no data. This is not an error. We
		// will change this behavior in the future.
		return newLocalResult(cResult), nil
	}
	if cResult.error_message != nil {
		return nil, &ChdbError{msg: C.GoString(cResult.error_message)}
	}
	return newLocalResult(cResult), nil
}

func (c *ChdbConn) Close() {
	C.close_conn(&c.conn)
}

// Accessor methods to access fields of the local_result_v2 struct.
func (r *LocalResult) Buf() []byte {
	if r.cResult == nil {
		return nil
	}
	if r.cResult.buf == nil {
		return nil
	}
	return C.GoBytes(unsafe.Pointer(r.cResult.buf), C.int(r.cResult.len))
}

// Stringer interface for LocalResult
func (r LocalResult) String() string {
	ret := r.Buf()
	if ret == nil {
		return ""
	}
	return string(ret)
}

func (r *LocalResult) Len() int {
	if r.cResult == nil {
		return 0
	}
	return int(r.cResult.len)
}

func (r *LocalResult) Elapsed() float64 {
	if r.cResult == nil {
		return 0
	}
	return float64(r.cResult.elapsed)
}

func (r *LocalResult) RowsRead() uint64 {
	if r.cResult == nil {
		return 0
	}
	return uint64(r.cResult.rows_read)
}

func (r *LocalResult) BytesRead() uint64 {
	if r.cResult == nil {
		return 0
	}
	return uint64(r.cResult.bytes_read)
}
