//go:build cgo && (darwin || linux)

package chdbarrow

import (
	"context"
	"errors"
	"testing"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/chdb-io/chdb-go/v2/internal/nativearrow"
)

type scanTestStream struct {
	closed []bool
}

func (*scanTestStream) Fetch(unsafe.Pointer) error { panic("unexpected native fetch") }
func (s *scanTestStream) Close(cancel bool)        { s.closed = append(s.closed, cancel) }

func scanTestBatch(t *testing.T) arrow.RecordBatch {
	t.Helper()
	schema := arrow.NewSchema([]arrow.Field{{Name: "id", Type: arrow.PrimitiveTypes.Int64}}, nil)
	b := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer b.Release()
	b.Field(0).(*array.Int64Builder).Append(42)
	return b.NewRecordBatch()
}

func TestScanBatchesCompletesAndClosesWithoutCancel(t *testing.T) {
	stream := &scanTestStream{}
	batch := scanTestBatch(t)
	batch.Retain() // keep one reference for this test after scanBatches releases its own
	defer batch.Release()
	reads, calls := 0, 0
	fetch := func(nativearrow.Stream, *arrow.Schema) (*arrow.Schema, arrow.RecordBatch, bool, error) {
		reads++
		if reads == 1 {
			return batch.Schema(), batch, false, nil
		}
		return batch.Schema(), nil, true, nil
	}
	if err := scanBatches(context.Background(), stream, fetch, func(got arrow.RecordBatch) error {
		calls++
		if got.NumRows() != 1 || got.Column(0).(*array.Int64).Value(0) != 42 {
			t.Fatalf("unexpected batch: %v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || reads != 2 || len(stream.closed) != 1 || stream.closed[0] {
		t.Fatalf("calls=%d reads=%d close=%v", calls, reads, stream.closed)
	}
}

func TestScanBatchesStopsOnConsumerError(t *testing.T) {
	stream := &scanTestStream{}
	batch := scanTestBatch(t)
	want := errors.New("consumer failed")
	reads := 0
	err := scanBatches(context.Background(), stream, func(nativearrow.Stream, *arrow.Schema) (*arrow.Schema, arrow.RecordBatch, bool, error) {
		reads++
		return batch.Schema(), batch, false, nil
	}, func(arrow.RecordBatch) error { return want })
	if !errors.Is(err, want) || reads != 1 || len(stream.closed) != 1 || !stream.closed[0] {
		t.Fatalf("err=%v reads=%d close=%v", err, reads, stream.closed)
	}
}

func TestScanBatchesChecksCancellationBetweenBatches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &scanTestStream{}
	batch := scanTestBatch(t)
	reads := 0
	err := scanBatches(ctx, stream, func(nativearrow.Stream, *arrow.Schema) (*arrow.Schema, arrow.RecordBatch, bool, error) {
		reads++
		return batch.Schema(), batch, false, nil
	}, func(arrow.RecordBatch) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) || reads != 1 || len(stream.closed) != 1 || !stream.closed[0] {
		t.Fatalf("err=%v reads=%d close=%v", err, reads, stream.closed)
	}
}

func TestScanBatchesCancelsOnFetchError(t *testing.T) {
	stream := &scanTestStream{}
	want := errors.New("fetch failed")
	err := scanBatches(context.Background(), stream, func(nativearrow.Stream, *arrow.Schema) (*arrow.Schema, arrow.RecordBatch, bool, error) {
		return nil, nil, false, want
	}, func(arrow.RecordBatch) error { t.Fatal("unexpected batch"); return nil })
	if !errors.Is(err, want) || len(stream.closed) != 1 || !stream.closed[0] {
		t.Fatalf("err=%v close=%v", err, stream.closed)
	}
}

func TestScanBatchesDoesNotDeliverAfterCancellationDuringFetch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &scanTestStream{}
	batch := scanTestBatch(t)
	err := scanBatches(ctx, stream, func(nativearrow.Stream, *arrow.Schema) (*arrow.Schema, arrow.RecordBatch, bool, error) {
		cancel()
		return batch.Schema(), batch, false, nil
	}, func(arrow.RecordBatch) error { t.Fatal("delivered after cancellation"); return nil })
	if !errors.Is(err, context.Canceled) || len(stream.closed) != 1 || !stream.closed[0] {
		t.Fatalf("err=%v close=%v", err, stream.closed)
	}
}

func TestScanBatchesRejectsNilStream(t *testing.T) {
	err := scanBatches(context.Background(), nil, nil, nil)
	if err == nil {
		t.Fatal("nil native stream was accepted")
	}
}
