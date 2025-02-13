package chdbpurego

import (
	"errors"
	"fmt"
	"unsafe"
)

type result struct {
	localResv2 *local_result_v2
}

func newChdbResult(cRes *local_result_v2) ChdbResult {
	res := &result{
		localResv2: cRes,
	}
	// runtime.SetFinalizer(res, res.Free)
	return res

}

// Buf implements ChdbResult.
func (c *result) Buf() []byte {
	if c.localResv2 != nil {
		if c.localResv2.buf != nil && c.localResv2.len > 0 {
			return unsafe.Slice(c.localResv2.buf, c.localResv2.len)
		}
	}
	return nil
}

// BytesRead implements ChdbResult.
func (c *result) BytesRead() uint64 {
	if c.localResv2 != nil {
		return c.localResv2.bytes_read
	}
	return 0
}

// Elapsed implements ChdbResult.
func (c *result) Elapsed() float64 {
	if c.localResv2 != nil {
		return c.localResv2.elapsed
	}
	return 0
}

// Error implements ChdbResult.
func (c *result) Error() error {
	if c.localResv2 != nil {
		if c.localResv2.error_message != nil {
			return errors.New(ptrToGoString(c.localResv2.error_message))
		}
	}
	return nil
}

// Free implements ChdbResult.
func (c *result) Free() error {
	if c.localResv2 != nil {
		freeResultV2(c.localResv2)
		c.localResv2 = nil
	}
	return nil
}

// Len implements ChdbResult.
func (c *result) Len() int {
	if c.localResv2 != nil {
		return int(c.localResv2.len)
	}
	return 0
}

// RowsRead implements ChdbResult.
func (c *result) RowsRead() uint64 {
	if c.localResv2 != nil {
		return c.localResv2.rows_read
	}
	return 0
}

// String implements ChdbResult.
func (c *result) String() string {
	ret := c.Buf()
	if ret == nil {
		return ""
	}
	return string(ret)
}

type connection struct {
	conn **chdb_conn
}

func NewChdbConn(conn **chdb_conn) ChdbConn {
	c := &connection{
		conn: conn,
	}
	// runtime.SetFinalizer(c, c.Close)
	return c
}

// Close implements ChdbConn.
func (c *connection) Close() {
	if c.conn != nil {
		closeConn(c.conn)
	}
}

// Query implements ChdbConn.
func (c *connection) Query(queryStr string, formatStr string) (result ChdbResult, err error) {

	if c.conn == nil {
		return nil, fmt.Errorf("invalid connection")
	}

	rawConn := *c.conn

	res := queryConn(rawConn, queryStr, formatStr)
	if res == nil {
		// According to the C ABI of chDB v1.2.0, the C function query_stable_v2
		// returns nil if the query returns no data. This is not an error. We
		// will change this behavior in the future.
		return newChdbResult(res), nil
	}
	if res.error_message != nil {
		return nil, errors.New(ptrToGoString(res.error_message))
	}

	return newChdbResult(res), nil
}

// Ready implements ChdbConn.
func (c *connection) Ready() bool {
	if c.conn != nil {
		deref := *c.conn
		if deref != nil {
			return deref.connected
		}
	}
	return false
}

func RawQuery(argc int, argv []string) (result ChdbResult, err error) {
	res := queryStableV2(argc, argv)
	if res == nil {
		// According to the C ABI of chDB v1.2.0, the C function query_stable_v2
		// returns nil if the query returns no data. This is not an error. We
		// will change this behavior in the future.
		return newChdbResult(res), nil
	}
	if res.error_message != nil {
		return nil, errors.New(ptrToGoString(res.error_message))
	}

	return newChdbResult(res), nil
}

func NewConnection(argc int, argv []string) (ChdbConn, error) {
	conn := connectChdb(argc, argv)
	if conn == nil {
		return nil, fmt.Errorf("could not create a chdb connection")
	}
	return NewChdbConn(conn), nil
}
