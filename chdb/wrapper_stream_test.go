package chdb

import (
	"runtime"
	"testing"
	"time"
)

// A streaming result is fetched chunk by chunk through the connection that
// started the query, so the session QueryStream opens has to stay open until the
// caller is done reading. Closing it right away — what QueryStream used to do —
// left the stream reading through a freed connection: chdb-core v26.5 failed the
// first fetch with "Unexpected null connection" and v26.7 segfaulted.
func TestQueryStreamOutlivesTheSessionItOpened(t *testing.T) {
	const wantRows = 200000
	before := ActiveSessionRefs()

	stream, err := QueryStream("SELECT number FROM system.numbers LIMIT 200000", "CSV")
	if err != nil {
		t.Fatalf("QueryStream() error: %v", err)
	}
	// Checked before the first fetch: reading a stream whose session is already
	// gone is what crashes, so fail here instead of in the engine.
	if refs := ActiveSessionRefs(); refs != before+1 {
		t.Fatalf("open sessions after QueryStream = %d, want %d (the stream must keep its session open)", refs, before+1)
	}

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
		if chunks > 10000 {
			// The engine keeps answering with empty chunks past end of data, so a
			// stream that never reports the end would loop here forever.
			t.Fatal("stream never reported end of data")
		}
	}
	if rows != wantRows {
		t.Errorf("streamed %d rows, want %d", rows, wantRows)
	}
	if chunks < 2 {
		t.Errorf("streamed in %d chunks, want the result spread over several fetches", chunks)
	}
	if err := stream.Error(); err != nil {
		t.Errorf("stream error after draining: %v", err)
	}

	// Draining the stream is enough to release the session, so a caller that
	// reads to the end and never calls Free does not leak the connection.
	if refs := ActiveSessionRefs(); refs != before {
		t.Errorf("open sessions after draining = %d, want %d", refs, before)
	}
	// Reading past the end stays at the end instead of fetching through the
	// connection that has just been closed.
	if chunk := stream.GetNext(); chunk != nil {
		t.Error("GetNext() past end of data returned a chunk, want nil")
	}
	// Free is still safe (and idempotent) after the stream released itself.
	stream.Free()
	stream.Free()
}

// A caller that stops reading early releases the session with Free/Cancel.
func TestQueryStreamFreeReleasesTheSession(t *testing.T) {
	before := ActiveSessionRefs()

	stream, err := QueryStream("SELECT number FROM system.numbers LIMIT 200000", "CSV")
	if err != nil {
		t.Fatalf("QueryStream() error: %v", err)
	}
	if refs := ActiveSessionRefs(); refs != before+1 {
		t.Fatalf("open sessions after QueryStream = %d, want %d (the stream must keep its session open)", refs, before+1)
	}
	if chunk := stream.GetNext(); chunk == nil {
		stream.Free()
		t.Fatal("GetNext() returned no chunk")
	}

	stream.Free()
	if refs := ActiveSessionRefs(); refs != before {
		t.Errorf("open sessions after Free = %d, want %d", refs, before)
	}
	if chunk := stream.GetNext(); chunk != nil {
		t.Error("GetNext() after Free returned a chunk, want nil")
	}
}

// An abandoned stream is collected: its session holds a native connection and
// pins the process-wide data path, so it must not depend on the caller.
func TestQueryStreamAbandonedStreamIsCollected(t *testing.T) {
	before := ActiveSessionRefs()

	func() {
		stream, err := QueryStream("SELECT number FROM system.numbers LIMIT 200000", "CSV")
		if err != nil {
			t.Fatalf("QueryStream() error: %v", err)
		}
		if refs := ActiveSessionRefs(); refs != before+1 {
			stream.Free()
			t.Fatalf("open sessions after QueryStream = %d, want %d (the stream must keep its session open)", refs, before+1)
		}
		if chunk := stream.GetNext(); chunk == nil {
			stream.Free()
			t.Fatal("GetNext() returned no chunk")
		}
	}()

	deadline := time.Now().Add(30 * time.Second)
	for {
		runtime.GC()
		if ActiveSessionRefs() == before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("open sessions after dropping the stream = %d, want %d", ActiveSessionRefs(), before)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
