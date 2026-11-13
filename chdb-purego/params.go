package chdbpurego

import (
	"errors"
	"runtime"

	"github.com/chdb-io/chdb-go/v2/internal/nativearrow"
)

// pinnedParams keeps every Go buffer reachable from the C pointer arrays
// pinned for the synchronous parameter-binding call. The engine copies the
// values during initialization; it never retains these pointers on a stream.
func pinnedParams(params []nativearrow.Parameter, call func(names **byte, nameLens *uint, values **byte, valueLens *uint, count uint) *chdb_result) *chdb_result {
	var pin runtime.Pinner
	defer pin.Unpin()
	names, values := make([]*byte, len(params)), make([]*byte, len(params))
	nameLens, valueLens := make([]uint, len(params)), make([]uint, len(params))
	nameBufs, valueBufs := make([][]byte, len(params)), make([][]byte, len(params))
	for i, param := range params {
		nameBufs[i] = append([]byte(param.Name), 0)
		valueBufs[i] = append([]byte(param.Value), 0)
		nameLens[i], valueLens[i] = uint(len(param.Name)), uint(len(param.Value))
		names[i], values[i] = &nameBufs[i][0], &valueBufs[i][0]
		pin.Pin(names[i])
		pin.Pin(values[i])
	}
	pin.Pin(&names[0])
	pin.Pin(&values[0])
	pin.Pin(&nameLens[0])
	pin.Pin(&valueLens[0])
	result := call(&names[0], &nameLens[0], &values[0], &valueLens[0], uint(len(params)))
	runtime.KeepAlive(nameBufs)
	runtime.KeepAlive(valueBufs)
	return result
}

// QueryWithParams binds {name:Type} placeholders for a single formatted query.
func (c *connection) QueryWithParams(sql, format string, params []nativearrow.Parameter) (ChdbResult, error) {
	if len(params) == 0 {
		return c.Query(sql, format)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.conn == nil || chdbQueryWithParamsN == nil {
		return nil, errors.New("chdb: native query parameter binding unavailable")
	}
	query, fmtBytes := []byte(sql), []byte(format)
	if len(query) == 0 {
		return nil, errors.New("chdb: empty parameterized query")
	}
	fmtBytes = append(fmtBytes, 0)
	res := pinnedParams(params, func(names **byte, nameLens *uint, values **byte, valueLens *uint, count uint) *chdb_result {
		return chdbQueryWithParamsN(c.conn.internal_data, &query[0], uint(len(query)), &fmtBytes[0], uint(len(format)), names, nameLens, values, valueLens, count)
	})
	runtime.KeepAlive(query)
	runtime.KeepAlive(fmtBytes)
	if res == nil {
		return nil, errors.New("chdb: parameterized query returned no result")
	}
	if msg := chdbResultError(res); msg != "" {
		chdbDestroyQueryResult(res)
		return nil, errors.New(msg)
	}
	return newChdbResult(res), nil
}

// QueryStreamingWithParams initializes a parameterized formatted stream.
func (c *connection) QueryStreamingWithParams(sql, format string, params []nativearrow.Parameter) (ChdbStreamResult, error) {
	if len(params) == 0 {
		return c.QueryStreaming(sql, format)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.conn == nil || chdbStreamQueryWithParamsN == nil {
		return nil, errors.New("chdb: native streaming query parameter binding unavailable")
	}
	query, fmtBytes := []byte(sql), []byte(format)
	if len(query) == 0 {
		return nil, errors.New("chdb: empty parameterized query")
	}
	fmtBytes = append(fmtBytes, 0)
	res := pinnedParams(params, func(names **byte, nameLens *uint, values **byte, valueLens *uint, count uint) *chdb_result {
		return chdbStreamQueryWithParamsN(c.conn.internal_data, &query[0], uint(len(query)), &fmtBytes[0], uint(len(format)), names, nameLens, values, valueLens, count)
	})
	runtime.KeepAlive(query)
	runtime.KeepAlive(fmtBytes)
	if res == nil {
		return nil, errors.New("chdb: parameterized streaming query returned no result")
	}
	if msg := chdbResultError(res); msg != "" {
		chdbDestroyQueryResult(res)
		return nil, errors.New(msg)
	}
	return newStreamingResult(c, res), nil
}
