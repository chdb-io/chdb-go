//go:build chdb_static && cgo && (darwin || linux) && (arm64 || amd64)

package chdbpurego

// The static adapter targets the complete v26.11.1 C ABI. Unlike the
// dynamic adapter it cannot safely mix optional symbols from another loaded
// engine; an older archive fails at link time instead.

/*
#cgo darwin LDFLAGS: -liconv -framework CoreFoundation -framework Security
#cgo linux LDFLAGS: -lc -lm -lrt -lpthread -ldl
#include <stdlib.h>
#ifndef CHDB_STATIC_HEADER
#include "static_chdb.h"
#else
#include CHDB_STATIC_HEADER
#endif

// chdb_connection is a pointer typedef. The shared Go code holds its
// internal_data (the real chdb_conn), and the close call holds the pointer to
// the handle itself. These casts match the C ABI's opaque handle layout.
static void chdb_static_close(void **conn) { chdb_close_conn((chdb_connection *)conn); }

static chdb_result * chdb_static_params(int kind, void *conn, char *query,
    size_t query_len, char *format, size_t format_len, char **names,
    size_t *name_lens, char **values, size_t *value_lens, size_t count) {
    if (kind == 0) return chdb_query_with_params_n((chdb_connection)conn,
        query, query_len, format, format_len, (const char *const *)names,
        name_lens, (const char *const *)values, value_lens, count);
    if (kind == 1) return chdb_stream_query_with_params_n((chdb_connection)conn,
        query, query_len, format, format_len, (const char *const *)names,
        name_lens, (const char *const *)values, value_lens, count);
    return chdb_stream_query_arrow_with_params_n((chdb_connection)conn,
        query, query_len, (const chdb_arrow_options *)format,
        (const char *const *)names, name_lens,
        (const char *const *)values, value_lens, count);
}
*/
import "C"

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"
)

func init() { staticBindings = bindStatic }

func staticCString(s string) (*C.char, func()) {
	p := C.CString(s)
	return p, func() { C.free(unsafe.Pointer(p)) }
}

func staticBytes(p *byte, n uint) *C.char           { return (*C.char)(unsafe.Pointer(p)) }
func staticConn(p unsafe.Pointer) C.chdb_connection { return C.chdb_connection(p) }
func staticResult(p *C.chdb_result) *chdb_result    { return (*chdb_result)(unsafe.Pointer(p)) }

// staticParams copies pointer arrays into C-owned buffers. The shared purego
// caller's pinned Go arrays cannot be handed to cgo as arrays of Go pointers.
func staticParams(kind C.int, conn unsafe.Pointer, query *byte, queryLen uint,
	format *byte, formatLen uint, names **byte, nameLens *uint, values **byte,
	valueLens *uint, count uint) *chdb_result {
	n := int(count)
	cn, cv := make([]*C.char, n), make([]*C.char, n)
	nl, vl := make([]C.size_t, n), make([]C.size_t, n)
	gn, gv := unsafe.Slice(names, n), unsafe.Slice(values, n)
	gnl, gvl := unsafe.Slice(nameLens, n), unsafe.Slice(valueLens, n)
	for i := range cn {
		nl[i], vl[i] = C.size_t(gnl[i]), C.size_t(gvl[i])
		cn[i] = (*C.char)(C.CBytes(unsafe.Slice(gn[i], int(gnl[i]))))
		cv[i] = (*C.char)(C.CBytes(unsafe.Slice(gv[i], int(gvl[i]))))
		defer C.free(unsafe.Pointer(cn[i]))
		defer C.free(unsafe.Pointer(cv[i]))
	}
	res := C.chdb_static_params(kind, conn, staticBytes(query, queryLen), C.size_t(queryLen),
		staticBytes(format, formatLen), C.size_t(formatLen),
		(**C.char)(unsafe.Pointer(&cn[0])), (*C.size_t)(unsafe.Pointer(&nl[0])),
		(**C.char)(unsafe.Pointer(&cv[0])), (*C.size_t)(unsafe.Pointer(&vl[0])), C.size_t(count))
	runtime.KeepAlive(cn)
	runtime.KeepAlive(cv)
	return staticResult(res)
}

func bindStatic() error {
	if os.Getenv(LibPathEnv) != "" {
		return fmt.Errorf("chdb: %s cannot override a statically linked engine", LibPathEnv)
	}
	if registeredEngine() != nil {
		return fmt.Errorf("chdb: lib/embedded cannot be used with chdb_static")
	}
	if version := C.GoString(C.chdb_version()); version != "26.11.1" {
		return fmt.Errorf("chdb: static adapter requires engine 26.11.1, linked %s", version)
	}
	// Everything below is a real cgo call, not a purego call to a symbol
	// resolved from the executable. The link must include the exact .a file.
	chdbConnect = func(argc int, argv []*byte) *chdb_connection {
		args := make([]*C.char, len(argv))
		for i, arg := range argv {
			args[i] = C.CString(ptrToGoString(arg))
			defer C.free(unsafe.Pointer(args[i]))
		}
		return (*chdb_connection)(unsafe.Pointer(C.chdb_connect(C.int(len(args)), &args[0])))
	}
	chdbCloseConn = func(p *chdb_connection) {
		C.chdb_static_close((*unsafe.Pointer)(unsafe.Pointer(p)))
	}
	chdbQuery = func(conn unsafe.Pointer, query, format string) *chdb_result {
		q, freeQ := staticCString(query)
		defer freeQ()
		f, freeF := staticCString(format)
		defer freeF()
		return staticResult(C.chdb_query(staticConn(conn), q, f))
	}
	chdbStreamQuery = func(conn unsafe.Pointer, query, format string) *chdb_result {
		q, freeQ := staticCString(query)
		defer freeQ()
		f, freeF := staticCString(format)
		defer freeF()
		return staticResult(C.chdb_stream_query(staticConn(conn), q, f))
	}
	chdbStreamFetchResult = func(conn unsafe.Pointer, r *chdb_result) *chdb_result {
		return staticResult(C.chdb_stream_fetch_result(staticConn(conn), (*C.chdb_result)(unsafe.Pointer(r))))
	}
	chdbStreamCancelQuery = func(conn unsafe.Pointer, r *chdb_result) {
		C.chdb_stream_cancel_query(staticConn(conn), (*C.chdb_result)(unsafe.Pointer(r)))
	}
	chdbDestroyQueryResult = func(r *chdb_result) { C.chdb_destroy_query_result((*C.chdb_result)(unsafe.Pointer(r))) }
	chdbResultBuffer = func(r *chdb_result) *byte {
		return (*byte)(unsafe.Pointer(C.chdb_result_buffer((*C.chdb_result)(unsafe.Pointer(r)))))
	}
	chdbResultLen = func(r *chdb_result) uint { return uint(C.chdb_result_length((*C.chdb_result)(unsafe.Pointer(r)))) }
	chdbResultElapsed = func(r *chdb_result) float64 {
		return float64(C.chdb_result_elapsed((*C.chdb_result)(unsafe.Pointer(r))))
	}
	chdbResultRowsRead = func(r *chdb_result) uint64 {
		return uint64(C.chdb_result_rows_read((*C.chdb_result)(unsafe.Pointer(r))))
	}
	chdbResultBytesRead = func(r *chdb_result) uint64 {
		return uint64(C.chdb_result_bytes_read((*C.chdb_result)(unsafe.Pointer(r))))
	}
	chdbResultStorageRowsRead = func(r *chdb_result) uint64 {
		return uint64(C.chdb_result_storage_rows_read((*C.chdb_result)(unsafe.Pointer(r))))
	}
	chdbResultStorageBytesRead = func(r *chdb_result) uint64 {
		return uint64(C.chdb_result_storage_bytes_read((*C.chdb_result)(unsafe.Pointer(r))))
	}
	chdbResultError = func(r *chdb_result) string {
		return C.GoString(C.chdb_result_error((*C.chdb_result)(unsafe.Pointer(r))))
	}
	chdbVersion = func() string { return C.GoString(C.chdb_version()) }
	chdbQueryWithParamsN = func(conn unsafe.Pointer, q *byte, ql uint, f *byte, fl uint,
		n **byte, nl *uint, v **byte, vl *uint, count uint) *chdb_result {
		return staticParams(0, conn, q, ql, f, fl, n, nl, v, vl, count)
	}
	chdbStreamQueryWithParamsN = func(conn unsafe.Pointer, q *byte, ql uint, f *byte, fl uint,
		n **byte, nl *uint, v **byte, vl *uint, count uint) *chdb_result {
		return staticParams(1, conn, q, ql, f, fl, n, nl, v, vl, count)
	}
	chdbStreamQueryArrowWithParamsN = func(conn unsafe.Pointer, q *byte, ql uint, opts unsafe.Pointer,
		n **byte, nl *uint, v **byte, vl *uint, count uint) *chdb_result {
		return staticParams(2, conn, q, ql, (*byte)(opts), 0, n, nl, v, vl, count)
	}
	chdbArrowScan = func(conn unsafe.Pointer, name string, stream unsafe.Pointer) int32 {
		p, free := staticCString(name)
		defer free()
		return int32(C.chdb_arrow_scan(staticConn(conn), p, C.chdb_arrow_stream(stream)))
	}
	chdbArrowUnregisterTable = func(conn unsafe.Pointer, name string) int32 {
		p, free := staticCString(name)
		defer free()
		return int32(C.chdb_arrow_unregister_table(staticConn(conn), p))
	}
	chdbQueryArrowN = func(conn unsafe.Pointer, q *byte, n uint, stream unsafe.Pointer, opts unsafe.Pointer) *chdb_result {
		return staticResult(C.chdb_query_arrow_n(staticConn(conn), staticBytes(q, n), C.size_t(n),
			C.chdb_arrow_stream(stream), (*C.chdb_arrow_options)(opts)))
	}
	chdbStreamQueryArrowN = func(conn unsafe.Pointer, q *byte, n uint, opts unsafe.Pointer) *chdb_result {
		return staticResult(C.chdb_stream_query_arrow_n(staticConn(conn), staticBytes(q, n), C.size_t(n), (*C.chdb_arrow_options)(opts)))
	}
	chdbStreamFetchArrow = func(conn unsafe.Pointer, r *chdb_result, stream unsafe.Pointer) int32 {
		return int32(C.chdb_stream_fetch_arrow(staticConn(conn), (*C.chdb_result)(unsafe.Pointer(r)), C.chdb_arrow_stream(stream)))
	}
	chdbBackupDatabaseN = func(conn unsafe.Pointer, db *byte, dl uint, path *byte, pl uint, base *byte, bl uint) *chdb_result {
		return staticResult(C.chdb_backup_database_n(staticConn(conn), staticBytes(db, dl), C.size_t(dl),
			staticBytes(path, pl), C.size_t(pl), staticBytes(base, bl), C.size_t(bl)))
	}
	chdbRestoreDatabaseN = func(conn unsafe.Pointer, db *byte, dl uint, path *byte, pl uint) *chdb_result {
		return staticResult(C.chdb_restore_database_n(staticConn(conn), staticBytes(db, dl), C.size_t(dl), staticBytes(path, pl), C.size_t(pl)))
	}
	chdbClassifyQueryN = func(conn unsafe.Pointer, sql *byte, sl uint, db *byte, dl uint, out *queryAnalysisV1) int32 {
		return int32(C.chdb_classify_query_n(staticConn(conn), staticBytes(sql, sl), C.size_t(sl),
			staticBytes(db, dl), C.size_t(dl), (*C.chdb_query_analysis_v1)(unsafe.Pointer(out))))
	}
	chdbShutdown = func() int32 { return int32(C.chdb_shutdown()) }
	chdbSetSignalHandlersEnabled = func(enabled int) { C.chdb_set_signal_handlers_enabled(C.int(enabled)) }
	chdbResetSignalHandlers = func() { C.chdb_reset_signal_handlers() }
	loadSigaction()
	func() {
		defer guardSignalHandlers()()
		chdbSetSignalHandlersEnabled(0)
	}()
	return nil
}
