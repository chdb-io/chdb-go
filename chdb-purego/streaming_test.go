package chdbpurego

import "testing"

// A stream is a cursor into state its connection owns, so closing the connection
// while a stream is still held has to end the stream rather than let it fetch
// through the freed connection (a segfault against chdb-core v26.7).
func TestStreamingResultAfterConnectionClose(t *testing.T) {
	conn, err := NewConnectionFromConnString(":memory:")
	if err != nil {
		t.Fatalf("NewConnectionFromConnString() error: %v", err)
	}
	defer conn.Close()

	stream, err := conn.QueryStreaming("SELECT number FROM system.numbers LIMIT 200000", "CSV")
	if err != nil {
		t.Fatalf("QueryStreaming() error: %v", err)
	}
	if chunk := stream.GetNext(); chunk == nil {
		t.Fatal("GetNext() returned no chunk")
	}

	conn.Close()

	if chunk := stream.GetNext(); chunk != nil {
		t.Error("GetNext() on a closed connection returned a chunk, want nil")
	}
	if err := stream.Error(); err != nil {
		t.Errorf("Error() on a closed connection = %v, want nil", err)
	}
	// Neither of these may reach the engine: the connection took the query's
	// state with it, so there is nothing left to cancel or destroy.
	stream.Free()
	stream.Cancel()
}

// The engine answers a fetch past end of data with an empty chunk, and keeps
// doing so; the stream reports the end once so that reading until nil ends.
func TestStreamingResultReportsEndOfData(t *testing.T) {
	conn, err := NewConnectionFromConnString(":memory:")
	if err != nil {
		t.Fatalf("NewConnectionFromConnString() error: %v", err)
	}
	defer conn.Close()

	stream, err := conn.QueryStreaming("SELECT number FROM system.numbers LIMIT 10", "CSV")
	if err != nil {
		t.Fatalf("QueryStreaming() error: %v", err)
	}
	defer stream.Free()

	var (
		rows   uint64
		chunks int
	)
	for {
		chunk := stream.GetNext()
		if chunk == nil {
			break
		}
		if err := chunk.Error(); err != nil {
			t.Fatalf("chunk %d: %v", chunks, err)
		}
		rows += chunk.RowsRead()
		chunks++
		if chunks > 100 {
			t.Fatal("stream never reported end of data")
		}
	}
	if rows != 10 {
		t.Errorf("streamed %d rows, want 10", rows)
	}
	if chunk := stream.GetNext(); chunk != nil {
		t.Error("GetNext() past end of data returned a chunk, want nil")
	}
}
