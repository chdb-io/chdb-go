package chdbpurego

import "errors"

type streamingResult struct {
	curConn  *chdb_conn
	stream   *chdb_streaming_result
	curChunk ChdbResult
}

func newStreamingResult(conn *chdb_conn, cRes *chdb_streaming_result) ChdbStreamResult {

	// nextChunk := streamingResultNext(conn, cRes)
	// if nextChunk == nil {
	// 	return nil
	// }

	res := &streamingResult{
		curConn: conn,
		stream:  cRes,
		// curChunk: newChdbResult(nextChunk),
	}

	// runtime.SetFinalizer(res, res.Free)
	return res

}

// Error implements ChdbStreamResult.
func (c *streamingResult) Error() error {
	if s := streamingResultError(c.stream); s != nil {
		return errors.New(*s)
	}
	return nil
}

// Free implements ChdbStreamResult.
func (c *streamingResult) Free() {
	streamingResultCancel(c.curConn, c.stream)
	streamingResultDestroy(c.stream)
	c.stream = nil
	if c.curChunk != nil {
		c.curChunk.Free()
		c.curChunk = nil
	}
}

// Cancel implements ChdbStreamResult.
func (c *streamingResult) Cancel() {
	c.Free()
}

// GetNext implements ChdbStreamResult.
func (c *streamingResult) GetNext() ChdbResult {
	if c.curChunk == nil {
		nextChunk := streamingResultNext(c.curConn, c.stream)
		if nextChunk == nil {
			return nil
		}
		c.curChunk = newChdbResult(nextChunk)
		return c.curChunk
	}
	// free the current chunk before getting the next one
	c.curChunk.Free()
	c.curChunk = nil
	nextChunk := streamingResultNext(c.curConn, c.stream)
	if nextChunk == nil {
		return nil
	}
	c.curChunk = newChdbResult(nextChunk)
	return c.curChunk
}
