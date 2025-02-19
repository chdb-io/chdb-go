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
	// Raw bytes result buffer, used for reading the result of clickhouse query
	Buf() []byte
	// String rapresentation of the the buffer
	String() string
	// Lenght in bytes of the buffer
	Len() int
	// Number of seconds elapsed for the query execution
	Elapsed() float64
	// Amount of rows returned by the query
	RowsRead() uint64
	// Amount of bytes returned by the query
	BytesRead() uint64
	// If the query had any error during execution, here you can retrieve the cause.
	Error() error
	// Free the query result and all the allocated memory
	Free()
}

type ChdbConn interface {
	//Query executes the given queryStr in the underlying clickhouse connection, and output the result in the given formatStr
	Query(queryStr string, formatStr string) (result ChdbResult, err error)
	//Ready returns a boolean indicating if the connections is successfully established.
	Ready() bool
	//Close the connection and free the underlying allocated memory
	Close()
}
