package chdbpurego

import "unsafe"

// old local result struct. for reference:
// https://github.com/chdb-io/chdb/blob/main/programs/local/chdb.h#L29
type local_result struct {
	buf        *byte
	len        uintptr
	_vec       unsafe.Pointer
	elapsed    float64
	rows_read  uint64
	bytes_read uint64
}

// new local result struct. for reference: https://github.com/chdb-io/chdb/blob/main/programs/local/chdb.h#L40
type local_result_v2 struct {
	buf           *byte
	len           uintptr
	_vec          unsafe.Pointer
	elapsed       float64
	rows_read     uint64
	bytes_read    uint64
	error_message *byte
}

// clickhouse background server connection.for reference: https://github.com/chdb-io/chdb/blob/main/programs/local/chdb.h#L82
type chdb_conn struct {
	server    unsafe.Pointer
	connected bool
	queue     unsafe.Pointer
}

type ChdbResult interface {
	Buf() []byte
	String() string
	Len() int
	Elapsed() float64
	RowsRead() uint64
	BytesRead() uint64
	Error() error
	Free() error
}

type ChdbConn interface {
	Query(queryStr string, formatStr string) (result ChdbResult, err error)
	Ready() bool
	Close()
}
