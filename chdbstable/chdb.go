package chdbstable

/*
#cgo LDFLAGS: -L. -lchdb
#include <stdlib.h> // Include the C standard library for C.free
#include "chdb.h"
*/
import "C"
import (
	"runtime"
	"unsafe"
)

// LocalResult mirrors the C struct local_result in Go.
type LocalResult struct {
	cResult *C.struct_local_result
}

// newLocalResult creates a new LocalResult and sets a finalizer to free C memory.
func newLocalResult(cResult *C.struct_local_result) *LocalResult {
	result := &LocalResult{cResult: cResult}
	runtime.SetFinalizer(result, freeLocalResult)
	return result
}

// freeLocalResult is called by the garbage collector.
func freeLocalResult(result *LocalResult) {
	C.free_result(result.cResult)
}

// QueryStable calls the C function query_stable.
func QueryStable(argc int, argv []string) *LocalResult {
	cArgv := make([]*C.char, len(argv))
	for i, s := range argv {
		cArgv[i] = C.CString(s)
		defer C.free(unsafe.Pointer(cArgv[i]))
	}

	cResult := C.query_stable(C.int(argc), &cArgv[0])
	return newLocalResult(cResult)
}

// Accessor methods to access fields of the local_result struct.
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
