package chdbdriver

import (
	"database/sql/driver"
	"io"
	"testing"

	chdbpurego "github.com/chdb-io/chdb-go/chdb-purego"
	"github.com/parquet-go/parquet-go"
)

type fakeResult struct{}

func (fakeResult) Buf() []byte       { return nil }
func (fakeResult) String() string    { return "" }
func (fakeResult) Len() int          { return 0 }
func (fakeResult) Elapsed() float64  { return 0 }
func (fakeResult) RowsRead() uint64  { return 1 }
func (fakeResult) BytesRead() uint64 { return 0 }
func (fakeResult) Error() error      { return nil }
func (fakeResult) Free()             {}

var _ chdbpurego.ChdbResult = (*fakeResult)(nil)

type eofRowGroup struct {
	schema *parquet.Schema
}

func (g *eofRowGroup) NumRows() int64                          { return 4 }
func (g *eofRowGroup) ColumnChunks() []parquet.ColumnChunk     { return nil }
func (g *eofRowGroup) Schema() *parquet.Schema                 { return g.schema }
func (g *eofRowGroup) SortingColumns() []parquet.SortingColumn { return nil }
func (g *eofRowGroup) Rows() parquet.Rows                      { return &eofRows{schema: g.schema} }

type eofRows struct {
	schema *parquet.Schema
	phase  int
	next   int64
}

func (r *eofRows) ReadRows(rows []parquet.Row) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	fill := func(n int, err error) (int, error) {
		if n > len(rows) {
			n = len(rows)
		}
		for i := 0; i < n; i++ {
			rows[i] = parquet.Row{parquet.ValueOf(r.next).Level(0, 0, 0)}
			r.next++
		}
		return n, err
	}

	switch r.phase {
	case 0:
		r.phase = 1
		return fill(2, nil)
	case 1:
		r.phase = 2
		return fill(2, io.EOF)
	default:
		return 0, io.EOF
	}
}

func (r *eofRows) SeekToRow(int64) error { return nil }

func (r *eofRows) Close() error { return nil }

func (r *eofRows) Schema() *parquet.Schema { return r.schema }

func TestParquetNextHandlesEOFWithRemainingRows(t *testing.T) {
	schema := parquet.SchemaOf(struct {
		Number int64 `parquet:"number"`
	}{})

	reader := parquet.NewGenericRowGroupReader[any](&eofRowGroup{schema: schema})
	rows := &parquetRows{
		reader:        reader,
		schemaFields:  schema.Fields(),
		bufferSize:    2,
		needNewBuffer: true,
		localResult:   fakeResult{},
	}

	dest := make([]driver.Value, 1)
	for i := 0; i < 4; i++ {
		if err := rows.Next(dest); err != nil {
			t.Fatalf("rows.Next failed at row %d, err: %v", i, err)
		}
	}

	if err := rows.Next(dest); err != io.EOF {
		t.Fatalf("expected io.EOF after 4 rows, actual: %v", err)
	}
}
