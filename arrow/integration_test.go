//go:build cgo && (darwin || linux)

package chdbarrow

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/chdb-io/chdb-go/v2/chdb"
	_ "github.com/chdb-io/chdb-go/v2/chdb/driver"
	"github.com/chdb-io/chdb-go/v2/internal/nativearrow"
)

// Opt in only with a finished, matching core build that includes the Arrow
// registration ownership fixes. Never run this against an in-progress build.
func TestQueryBatchWithCore(t *testing.T) {
	path := os.Getenv("CHDB_ARROW_TEST_LIB")
	if path == "" {
		t.Skip("set CHDB_ARROW_TEST_LIB to a finished PR #323-compatible libchdb")
	}
	t.Setenv("CHDB_LIB_PATH", path)
	if version, err := chdb.EngineVersion(); err == nil {
		t.Logf("libchdb version: %s", version)
	}
	sess, err := chdb.NewSession(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	capabilities, err := GetCapabilities(sess)
	if err != nil || !capabilities.ContractCompatible || !capabilities.Input || !capabilities.TableOutput || !capabilities.ReaderOutput {
		t.Fatalf("Arrow ABI capabilities = %+v, %v", capabilities, err)
	}
	if !capabilities.ParamBinding {
		t.Fatalf("released engine lacks Arrow parameter ABI: %+v", capabilities)
	}
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "id", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
		{Name: "label", Type: arrow.BinaryTypes.String, Nullable: true},
	}, nil)
	b := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	ids := b.Field(0).(*array.Int64Builder)
	labels := b.Field(1).(*array.StringBuilder)
	ids.Append(7)
	labels.Append("héllo 中文")
	ids.AppendNull()
	labels.AppendNull()
	rec := b.NewRecordBatch()
	b.Release()
	defer rec.Release()
	got, err := QueryBatch(sess, rec, func(source string) string {
		return "SELECT sum(id), count(), countIf(isNull(id)) FROM " + source
	}, "TSV")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "7\t2\t1\n" {
		t.Fatalf("unexpected query output: %q", got)
	}
	streamInput, err := array.NewRecordReader(schema, []arrow.RecordBatch{rec})
	if err != nil {
		t.Fatal(err)
	}
	streamedRows := 0
	err = QueryFromReaderBatchesContext(context.Background(), sess, streamInput,
		func(expr string) string { return "SELECT id FROM " + expr + " WHERE id IS NOT NULL" },
		func(batch arrow.RecordBatch) error {
			ids := batch.Column(0).(*array.Int64)
			for i := 0; i < ids.Len(); i++ {
				if ids.Value(i) != 7 {
					t.Fatalf("unexpected streamed id: %d", ids.Value(i))
				}
				streamedRows++
			}
			return nil
		})
	streamInput.Release()
	if err != nil || streamedRows != 1 {
		t.Fatalf("streamed Arrow query rows=%d err=%v", streamedRows, err)
	}
	bound, err := QueryReaderParams(context.Background(), sess, "SELECT {v:Int64} AS v", []Parameter{{Name: "v", Value: "17"}})
	if err != nil {
		t.Fatal(err)
	}
	if !bound.Next() || bound.RecordBatch().Column(0).(*array.Int64).Value(0) != 17 {
		t.Fatal("server-side Arrow parameter did not bind")
	}
	_ = bound.Close()
	quotedValue := "x' OR 1=1 --"
	quoted, err := QueryReaderParams(context.Background(), sess, "SELECT {v:String} AS v", []Parameter{{Name: "v", Value: quotedValue}})
	if err != nil {
		t.Fatal(err)
	}
	if !quoted.Next() || quoted.RecordBatch().Column(0).(*array.String).Value(0) != quotedValue {
		t.Fatal("parameter value was not preserved as data")
	}
	_ = quoted.Close()
	if leaked, err := QueryReader(context.Background(), sess, "SELECT {v:String}"); err == nil {
		_ = leaked.Close()
		t.Fatal("per-call parameter leaked into a later query")
	}

	boundInput, err := array.NewRecordReader(schema, []arrow.RecordBatch{rec})
	if err != nil {
		t.Fatal(err)
	}
	boundRows := 0
	err = QueryFromReaderBatchesParamsContext(context.Background(), sess, boundInput,
		func(expr string) string { return "SELECT id + {offset:Int64} FROM " + expr + " WHERE id IS NOT NULL" },
		[]Parameter{{Name: "offset", Value: "3"}}, func(batch arrow.RecordBatch) error {
			if batch.Column(0).(*array.Int64).Value(0) != 10 {
				t.Fatal("input scan parameter mismatch")
			}
			boundRows += int(batch.NumRows())
			return nil
		})
	boundInput.Release()
	if err != nil || boundRows != 1 {
		t.Fatalf("bound input scan rows=%d err=%v", boundRows, err)
	}

	db, err := sql.Open("chdb", "session=:memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sqlConn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer sqlConn.Close()
	sqlInput, err := array.NewRecordReader(schema, []arrow.RecordBatch{rec})
	if err != nil {
		t.Fatal(err)
	}
	sqlRows := 0
	err = QueryFromReaderBatchesSQLContext(context.Background(), sqlConn, sqlInput,
		func(expr string) string { return "SELECT id + {offset:Int64} FROM " + expr + " WHERE id IS NOT NULL" },
		[]Parameter{{Name: "offset", Value: "4"}}, func(batch arrow.RecordBatch) error {
			if batch.Column(0).(*array.Int64).Value(0) != 11 {
				t.Fatal("database/sql Arrow result mismatch")
			}
			sqlRows += int(batch.NumRows())
			return nil
		})
	sqlInput.Release()
	if err != nil || sqlRows != 1 {
		t.Fatalf("database/sql Arrow rows=%d err=%v", sqlRows, err)
	}
	primitiveSchema := arrow.NewSchema([]arrow.Field{
		{Name: "i32", Type: arrow.PrimitiveTypes.Int32},
		{Name: "f64", Type: arrow.PrimitiveTypes.Float64},
		{Name: "flag", Type: arrow.FixedWidthTypes.Boolean},
		{Name: "blob", Type: arrow.BinaryTypes.Binary},
	}, nil)
	primitiveBuilder := array.NewRecordBuilder(memory.DefaultAllocator, primitiveSchema)
	primitiveBuilder.Field(0).(*array.Int32Builder).Append(3)
	primitiveBuilder.Field(1).(*array.Float64Builder).Append(2.5)
	primitiveBuilder.Field(2).(*array.BooleanBuilder).Append(true)
	primitiveBuilder.Field(3).(*array.BinaryBuilder).Append([]byte("a"))
	primitive := primitiveBuilder.NewRecordBatch()
	primitiveBuilder.Release()
	defer primitive.Release()
	primitiveResult, err := QueryBatch(sess, primitive, func(expr string) string {
		return "SELECT count(), sum(i32), countIf(flag), max(length(blob)) FROM " + expr
	}, "TSV")
	if err != nil || string(primitiveResult) != "1\t3\t1\t1\n" {
		t.Fatalf("primitive Arrow input = %q, %v", primitiveResult, err)
	}
	timeSchema := arrow.NewSchema([]arrow.Field{
		{Name: "day", Type: arrow.FixedWidthTypes.Date32},
		{Name: "event_time", Type: arrow.FixedWidthTypes.Timestamp_ms},
	}, nil)
	timeBuilder := array.NewRecordBuilder(memory.DefaultAllocator, timeSchema)
	timeBuilder.Field(0).(*array.Date32Builder).Append(arrow.Date32(1))
	timeBuilder.Field(1).(*array.TimestampBuilder).Append(arrow.Timestamp(1000))
	timeBatch := timeBuilder.NewRecordBatch()
	timeBuilder.Release()
	defer timeBatch.Release()
	timeResult, err := QueryBatch(sess, timeBatch, func(expr string) string {
		return "SELECT count(), countIf(isNull(day)), countIf(isNull(event_time)) FROM " + expr
	}, "TSV")
	if err != nil || string(timeResult) != "1\t0\t0\n" {
		t.Fatalf("date/timestamp Arrow input = %q, %v", timeResult, err)
	}
	decimalSchema := arrow.NewSchema([]arrow.Field{
		{Name: "amount", Type: &arrow.Decimal128Type{Precision: 10, Scale: 2}},
	}, nil)
	decimalBuilder := array.NewRecordBuilder(memory.DefaultAllocator, decimalSchema)
	decimalBuilder.Field(0).(*array.Decimal128Builder).Append(decimal128.FromI64(12345))
	decimalBatch := decimalBuilder.NewRecordBatch()
	decimalBuilder.Release()
	defer decimalBatch.Release()
	decimalResult, err := QueryBatch(sess, decimalBatch, func(expr string) string {
		return "SELECT count(), countIf(isNull(amount)) FROM " + expr
	}, "TSV")
	if err != nil || string(decimalResult) != "1\t0\n" {
		t.Fatalf("Decimal128 Arrow input = %q, %v", decimalResult, err)
	}
	registered, err := RegisterBatch(sess, rec)
	if err != nil {
		t.Fatal(err)
	}
	registeredResult, err := registered.Query(func(expr string) string {
		return "SELECT count() FROM " + expr
	}, "TSV")
	if err != nil || string(registeredResult) != "2\n" {
		t.Fatalf("registered batch result = %q, %v", registeredResult, err)
	}
	if _, err := registered.Query(func(expr string) string { return "SELECT * FROM " + expr }, "TSV"); !errors.Is(err, ErrSourceConsumed) {
		t.Fatalf("second scan = %v, want ErrSourceConsumed", err)
	}
	if err := registered.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := registered.Query(func(expr string) string { return "SELECT * FROM " + expr }, "TSV"); !errors.Is(err, ErrSourceClosed) {
		t.Fatalf("closed source query = %v, want ErrSourceClosed", err)
	}
	inputCtx, cancelInput := context.WithCancel(context.Background())
	inputReader, err := array.NewRecordReader(schema, []arrow.RecordBatch{rec})
	if err != nil {
		t.Fatal(err)
	}
	cancellableSource, err := RegisterReaderContext(inputCtx, sess, inputReader)
	inputReader.Release()
	if err != nil {
		t.Fatal(err)
	}
	cancelInput()
	if _, err := cancellableSource.Query(func(expr string) string { return "SELECT count() FROM " + expr }, "TSV"); !errors.Is(err, context.Canceled) {
		_ = cancellableSource.Close()
		t.Fatalf("cancelled input reader = %v, want context.Canceled", err)
	}
	if err := cancellableSource.Close(); err != nil {
		t.Fatal(err)
	}
	typedSource, err := RegisterBatch(sess, rec)
	if err != nil {
		t.Fatal(err)
	}
	typedFromSource, err := typedSource.QueryTable(func(expr string) string {
		return "SELECT id, label FROM " + expr
	})
	if err != nil {
		_ = typedSource.Close()
		t.Fatal(err)
	}
	if typedFromSource.NumRows() != 2 || !typedFromSource.Column(0).Data().Chunk(0).IsNull(1) {
		t.Fatal("typed query over registered source lost rows or NULL")
	}
	if err := typedSource.Close(); err != nil {
		t.Fatal(err)
	}
	if got := typedFromSource.Column(0).Data().Chunk(0).(*array.Int64).Value(0); got != 7 {
		typedFromSource.Release()
		t.Fatalf("typed result changed after source Close: %d", got)
	}
	labelsOut := typedFromSource.Column(1).Data().Chunk(0).(*array.String)
	if labelsOut.Value(0) != "héllo 中文" || !labelsOut.IsNull(1) {
		typedFromSource.Release()
		t.Fatal("typed UTF-8 or null bitmap changed after source Close")
	}
	typedFromSource.Release()
	table, err := QueryTable(sess, "SELECT toInt64(7) AS id")
	if err != nil {
		t.Fatal(err)
	}
	if table.NumRows() != 1 || table.NumCols() != 1 || table.Schema().Field(0).Name != "id" {
		t.Fatalf("unexpected typed table: rows=%d cols=%d schema=%s", table.NumRows(), table.NumCols(), table.Schema())
	}
	if got := table.Column(0).Data().Chunk(0).(*array.Int64).Value(0); got != 7 {
		t.Fatalf("typed Arrow value = %d, want 7", got)
	}
	table.Release()
	complexTypes, err := QueryTable(sess,
		"SELECT toDate('2026-01-01') AS day, toDateTime64('2026-01-01 00:00:01', 3, 'UTC') AS event_time, toDecimal128('123.45', 2) AS amount")
	if err != nil {
		t.Fatal(err)
	}
	if complexTypes.Schema().Field(0).Type.ID() != arrow.DATE32 ||
		complexTypes.Schema().Field(1).Type.ID() != arrow.TIMESTAMP ||
		complexTypes.Schema().Field(2).Type.ID() != arrow.DECIMAL128 {
		complexTypes.Release()
		t.Fatalf("unexpected date/timestamp/decimal result schema: %s", complexTypes.Schema())
	}
	complexTypes.Release()
	empty, err := QueryTable(sess, "SELECT toInt64(7) AS id WHERE 0")
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Release()
	if empty.NumRows() != 0 || empty.NumCols() != 1 || empty.Schema().Field(0).Name != "id" {
		t.Fatalf("zero-row query lost schema: rows=%d cols=%d schema=%s", empty.NumRows(), empty.NumCols(), empty.Schema())
	}
	binary, err := QueryTableWithOptions(sess, "SELECT toString('hello') AS value", OutputOptions{StringAsBinary: true})
	if err != nil {
		t.Fatal(err)
	}
	if binary.Schema().Field(0).Type.ID() != arrow.BINARY {
		binary.Release()
		t.Fatal("explicit StringAsBinary option did not produce Arrow binary")
	}
	binary.Release()
	dictionary, err := QueryTableWithOptions(sess,
		"SELECT toLowCardinality(toString('hello')) AS value",
		OutputOptions{LowCardinalityAsDictionary: true})
	if err != nil {
		t.Fatal(err)
	}
	if dictionary.Schema().Field(0).Type.ID() != arrow.DICTIONARY {
		dictionary.Release()
		t.Fatal("explicit LowCardinalityAsDictionary option did not produce a dictionary")
	}
	dictionary.Release()
	binaryReader, err := QueryReaderWithOptions(context.Background(), sess,
		"SELECT toString('hello') AS value", OutputOptions{StringAsBinary: true})
	if err != nil {
		t.Fatal(err)
	}
	if binaryReader.Schema().Field(0).Type.ID() != arrow.BINARY || !binaryReader.Next() {
		_ = binaryReader.Close()
		t.Fatal("streaming StringAsBinary option was not applied")
	}
	_ = binaryReader.Close()
	created, err := sess.Query("CREATE TEMPORARY TABLE arrow_insert_test (id Nullable(Int64), label Nullable(String))", "TSV")
	if err != nil {
		t.Fatal(err)
	}
	created.Free()
	if err := InsertBatch(sess, "arrow_insert_test", rec); err != nil {
		t.Fatal(err)
	}
	count, err := sess.Query("SELECT count(), sum(id) FROM arrow_insert_test", "TSV")
	if err != nil {
		t.Fatal(err)
	}
	defer count.Free()
	if got := count.String(); got != "2\t7\n" {
		t.Fatalf("unexpected inserted rows: %q", got)
	}
	if _, err := QueryBatch(sess, rec, func(source string) string {
		return "SELECT missing_column FROM " + source
	}, "TSV"); err == nil {
		t.Fatal("invalid query did not report an error")
	}
	if _, err := QueryTable(sess, "SELECT missing_arrow_column"); err == nil {
		t.Fatal("typed query did not report SQL error")
	}
	if _, err := QueryReader(context.Background(), sess, "SELECT missing_arrow_column"); err == nil {
		t.Fatal("streaming typed query did not report SQL error")
	}
	afterError, err := QueryBatch(sess, rec, func(source string) string {
		return "SELECT count() FROM " + source
	}, "TSV")
	if err != nil || string(afterError) != "2\n" {
		t.Fatalf("query after failed registration scope = %q, %v", afterError, err)
	}
	multiBuilder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	multiBuilder.Field(0).(*array.Int64Builder).AppendValues([]int64{7, 0}, []bool{true, false})
	multiBuilder.Field(1).(*array.StringBuilder).AppendValues([]string{"one", ""}, []bool{true, false})
	first := multiBuilder.NewRecordBatch()
	multiBuilder.Field(0).(*array.Int64Builder).Append(5)
	multiBuilder.Field(1).(*array.StringBuilder).Append("two")
	second := multiBuilder.NewRecordBatch()
	multiBuilder.Release()
	// Keep the test's own references: the first reader consumes its held
	// references, but these batches are reused by a second reader below.
	defer first.Release()
	defer second.Release()
	source, err := array.NewRecordReader(schema, []arrow.RecordBatch{first, second})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Release()
	multi, err := QueryFromReader(sess, source, func(expr string) string {
		return "SELECT count(), sum(id), countIf(isNull(id)) FROM " + expr
	}, "TSV")
	if err != nil || string(multi) != "3\t12\t1\n" {
		t.Fatalf("multi-batch Arrow input = %q, %v", multi, err)
	}
	insertSource, err := array.NewRecordReader(schema, []arrow.RecordBatch{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if err := InsertReader(sess, "arrow_insert_test", insertSource); err != nil {
		insertSource.Release()
		t.Fatal(err)
	}
	insertSource.Release()
	inserted, err := sess.Query("SELECT count(), sum(id) FROM arrow_insert_test", "TSV")
	if err != nil {
		t.Fatal(err)
	}
	if got := inserted.String(); got != "5\t19\n" {
		inserted.Free()
		t.Fatalf("multi-batch insert = %q, want 5 rows sum 19", got)
	}
	inserted.Free()
	rdr, err := QueryReader(context.Background(), sess,
		"SELECT number FROM numbers(7) SETTINGS max_block_size=2")
	if err != nil {
		t.Fatal(err)
	}
	if rdr.Schema() == nil || rdr.Schema().NumFields() != 1 {
		t.Fatal("streaming reader lost its schema before Next")
	}
	var rows, batches int64
	for rdr.Next() {
		rows += rdr.RecordBatch().NumRows()
		batches++
	}
	if err := rdr.Err(); err != nil {
		t.Fatal(err)
	}
	if rows != 7 || batches < 2 {
		t.Fatalf("streaming rows=%d batches=%d; want 7 rows in multiple batches", rows, batches)
	}
	_ = rdr.Close()
	dictReader, err := QueryReaderWithOptions(context.Background(), sess,
		"SELECT toLowCardinality(toString(number)) AS value FROM numbers(7) SETTINGS max_block_size=2",
		OutputOptions{LowCardinalityAsDictionary: true})
	if err != nil {
		t.Fatal(err)
	}
	if dictReader.Schema().Field(0).Type.ID() != arrow.DICTIONARY {
		_ = dictReader.Close()
		t.Fatal("streaming dictionary option did not preserve dictionary type")
	}
	var dictRows int64
	for dictReader.Next() {
		dictRows += dictReader.RecordBatch().NumRows()
	}
	if err := dictReader.Err(); err != nil || dictRows != 7 {
		_ = dictReader.Close()
		t.Fatalf("streaming dictionary rows=%d err=%v", dictRows, err)
	}
	_ = dictReader.Close()
	refReader, err := QueryReader(context.Background(), sess,
		"SELECT number FROM numbers(3) SETTINGS max_block_size=1")
	if err != nil {
		t.Fatal(err)
	}
	refReader.Retain()
	refReader.Release()
	if !refReader.Next() {
		t.Fatal("one Release closed a retained Arrow RecordReader")
	}
	refReader.Release()
	if refReader.Next() {
		t.Fatal("last Release did not close Arrow RecordReader")
	}
	emptyReader, err := QueryReader(context.Background(), sess,
		"SELECT toInt64(7) AS id WHERE 0")
	if err != nil {
		t.Fatal(err)
	}
	if emptyReader.Schema() == nil || emptyReader.Schema().Field(0).Name != "id" || emptyReader.Next() {
		t.Fatal("zero-row streaming result lost schema or returned a batch")
	}
	_ = emptyReader.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancelled, err := QueryReader(ctx, sess, "SELECT number FROM numbers(1000) SETTINGS max_block_size=10")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if cancelled.Next() || !errors.Is(cancelled.Err(), context.Canceled) {
		t.Fatalf("cancelled reader: Next=true or Err=%v", cancelled.Err())
	}
	_ = cancelled.Close()
	partial, err := QueryReader(context.Background(), sess,
		"SELECT number FROM numbers(1000) SETTINGS max_block_size=10")
	if err != nil {
		t.Fatal(err)
	}
	if !partial.Next() {
		t.Fatal("expected first batch before early Close")
	}
	_ = partial.Close()
	_ = partial.Close()
	if partial.Next() {
		t.Fatal("closed reader produced a new batch")
	}
	stillUsable, err := sess.Query("SELECT 1", "TSV")
	if err != nil {
		t.Fatal(err)
	}
	stillUsable.Free()
	other, err := chdb.NewSession(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	live, err := QueryReader(context.Background(), other,
		"SELECT number FROM numbers(1000) SETTINGS max_block_size=10")
	if err != nil {
		other.Close()
		t.Fatal(err)
	}
	other.Close() // closes the unconsumed reader before retiring its connection
	if live.Next() {
		t.Fatal("Session.Close left a reader using its retired connection")
	}
	_ = live.Close()
	otherSourceSession, err := chdb.NewSession(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	unconsumed, err := RegisterBatch(otherSourceSession, rec)
	if err != nil {
		otherSourceSession.Close()
		t.Fatal(err)
	}
	otherSourceSession.Close()
	if _, err := unconsumed.Query(func(expr string) string { return "SELECT * FROM " + expr }, "TSV"); !errors.Is(err, ErrSourceClosed) {
		t.Fatalf("session-closed source query = %v, want ErrSourceClosed", err)
	}
	selfClosingSession, err := chdb.NewSession(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	selfClosingSource, err := RegisterBatch(selfClosingSession, rec)
	if err != nil {
		selfClosingSession.Close()
		t.Fatal(err)
	}
	selfCloseDone := make(chan error, 1)
	go func() {
		_, queryErr := selfClosingSource.Query(func(expr string) string {
			selfClosingSession.Close()
			return "SELECT count() FROM " + expr
		}, "TSV")
		selfCloseDone <- queryErr
	}()
	select {
	case queryErr := <-selfCloseDone:
		if !errors.Is(queryErr, ErrSourceClosed) {
			t.Fatalf("query builder closed its Session: %v, want ErrSourceClosed", queryErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("query builder deadlocked while closing its Session")
	}
	overlapped, err := chdb.NewSession(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	busySource, err := RegisterBatch(overlapped, rec)
	if err != nil {
		overlapped.Close()
		t.Fatal(err)
	}
	queryEntered := make(chan struct{})
	queryResume := make(chan struct{})
	queryDone := make(chan error, 1)
	go func() {
		_, queryErr := busySource.Query(func(expr string) string {
			close(queryEntered)
			<-queryResume
			return "SELECT count() FROM " + expr
		}, "TSV")
		queryDone <- queryErr
	}()
	<-queryEntered
	sourceCloseDone := make(chan struct{})
	go func() { overlapped.Close(); close(sourceCloseDone) }()
	select {
	case <-sourceCloseDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Session.Close waited for a SQL builder with no native query in flight")
	}
	close(queryResume)
	if err := <-queryDone; !errors.Is(err, ErrSourceClosed) {
		t.Fatalf("query after close during SQL construction = %v, want ErrSourceClosed", err)
	}
	if _, err := busySource.Query(func(expr string) string { return "SELECT * FROM " + expr }, "TSV"); !errors.Is(err, ErrSourceClosed) {
		t.Fatalf("source after Session.Close = %v, want ErrSourceClosed", err)
	}
	failedSource := &failingReader{RecordReader: source}
	if _, err := QueryFromReader(sess, failedSource, func(expr string) string {
		return "SELECT count() FROM " + expr
	}, "TSV"); err == nil {
		t.Fatal("producer error was lost across the C Stream callback")
	}
	concurrent, err := chdb.NewSession(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	active, err := QueryReader(context.Background(), concurrent,
		"SELECT number FROM numbers(100) SETTINGS max_block_size=10")
	if err != nil {
		concurrent.Close()
		t.Fatal(err)
	}
	if !active.Next() {
		_ = active.Close()
		concurrent.Close()
		t.Fatal("expected initial batch before interleaved Close")
	}
	gate := &gatedNativeFetch{Stream: active.stream, entered: make(chan struct{}), resume: make(chan struct{})}
	active.stream = gate // integration-only instrumentation; no native call changes
	nextDone := make(chan struct{})
	go func() { active.Next(); close(nextDone) }()
	<-gate.entered
	closeDone := make(chan struct{})
	go func() { concurrent.Close(); close(closeDone) }()
	select {
	case <-closeDone:
		t.Fatal("Session.Close retired a connection during a pending Next")
	case <-time.After(20 * time.Millisecond):
	}
	close(gate.resume)
	<-nextDone
	<-closeDone
	if active.Next() {
		t.Fatal("reader produced a batch after Session.Close")
	}
}

type failingReader struct{ array.RecordReader }

func (*failingReader) Next() bool { return false }
func (*failingReader) Err() error { return errors.New("synthetic producer failure") }

type gatedNativeFetch struct {
	nativearrow.Stream
	entered chan struct{}
	resume  chan struct{}
}

func (g *gatedNativeFetch) Fetch(out unsafe.Pointer) error {
	close(g.entered)
	<-g.resume
	return g.Stream.Fetch(out)
}
