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

// clickhouse streaming result struct. for reference: https://github.com/chdb-io/chdb/blob/main/programs/local/chdb.h#L65
type chdb_streaming_result struct {
	internal_data unsafe.Pointer
}

// clickhouse background server connection.for reference: https://github.com/chdb-io/chdb/blob/main/programs/local/chdb.h#L82
type chdb_conn struct {
	server    unsafe.Pointer
	connected bool
	queue     unsafe.Pointer
}

type ChdbResult interface {
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

type ChdbStreamResult interface {
	// GetNext returns the next chunk of data from the stream.
	// The chunk is a ChdbResult object that can be used to read the data.
	// If there are no more chunks, it returns nil.
	GetNext() ChdbResult
	// Error returns the error message if there was an error during the streaming process.
	Error() error
	// Cancel cancels the streaming process and frees the underlying memory.
	Cancel()
	// Free frees the underlying memory and closes the stream.
	Free()
}

type ChdbConn interface {
	//Query executes the given queryStr in the underlying clickhouse connection, and output the result in the given formatStr
	Query(queryStr string, formatStr string) (result ChdbResult, err error)
	// QueryStreaming executes the given queryStr in the underlying clickhouse connection, and output the result in the given formatStr
	// The result is a stream of data that can be read in chunks.
	// This is useful for large datasets that cannot be loaded into memory all at once.
	QueryStreaming(queryStr string, formatStr string) (result ChdbStreamResult, err error)
	//Ready returns a boolean indicating if the connections is successfully established.
	Ready() bool
	//Close the connection and free the underlying allocated memory
	Close()
}
