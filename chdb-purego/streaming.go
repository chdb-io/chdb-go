package chdbpurego

import "errors"

type streamingResult struct {
	// conn is the connection the query was started on, not just its handle:
	// every chunk is fetched through it, so the stream goes through the
	// connection's own guards instead of dereferencing a handle it does not own.
	conn     *connection
	stream   *chdb_result
	curChunk ChdbResult
	// Set once the engine has signalled end of data, so Free() knows there is
	// nothing left to cancel. See the comment there.
	finished bool
}

func newStreamingResult(conn *connection, cRes *chdb_result) ChdbStreamResult {

	// nextChunk := streamingResultNext(conn, cRes)
	// if nextChunk == nil {
	// 	return nil
	// }

	res := &streamingResult{
		conn:   conn,
		stream: cRes,
		// curChunk: newChdbResult(nextChunk),
	}

	// runtime.SetFinalizer(res, res.Free)
	return res

}

// Error implements ChdbStreamResult.
func (c *streamingResult) Error() error {
	if s := c.conn.streamError(c.stream); s != "" {
		return errors.New(s)
	}
	return nil
}

// Free implements ChdbStreamResult.
func (c *streamingResult) Free() {
	if c.stream != nil {
		// Cancel only while the engine still has a stream to stop. Once it has
		// signalled end of data it has retired the query's state, and cancelling a
		// retired stream walks into it: chdb_stream_cancel_query takes the handle as
		// an opaque pointer and casts it without checking, so there is nothing on
		// the engine side to turn that into an error instead of a fault. Against
		// chdb-core v26.7.0 it segfaults on linux/amd64, and survives on arm64 only
		// because the freed memory happens to still read back the way it did.
		//
		// Destroying is still correct and still required — the engine's own note on
		// cancel says the handle is released by chdb_destroy_query_result, not by
		// cancel — so only the cancel is conditional. releaseStream skips both when
		// the connection is already closed, which retires the query anyway.
		c.conn.releaseStream(c.stream, !c.finished)
	}

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
	if c.curChunk != nil {
		// free the current chunk before getting the next one
		c.curChunk.Free()
		c.curChunk = nil
	}
	if c.finished {
		// The engine does not stop at end of data: asked again it keeps handing
		// out empty chunks, so a caller reading until GetNext returns nil would
		// never leave the loop. Report the end once and stay there.
		return nil
	}
	nextChunk := c.conn.fetchStream(c.stream)
	if nextChunk == nil {
		c.finished = true
		return nil
	}
	c.curChunk = newChdbResult(nextChunk)
	// A chunk with no rows is how the engine says the stream is done; callers
	// treat it as EOF and stop reading, so this is the last chunk there will be.
	if c.curChunk.RowsRead() == 0 {
		c.finished = true
	}
	return c.curChunk
}
