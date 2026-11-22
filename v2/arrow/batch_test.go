//go:build cgo && (darwin || linux)

package chdbarrow

import (
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

func TestQueryBatchRejectsUnsupportedTypeBeforeLoadingEngine(t *testing.T) {
	schema := arrow.NewSchema([]arrow.Field{{Name: "bad", Type: arrow.FixedWidthTypes.Date64}}, nil)
	b := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	b.Field(0).(*array.Date64Builder).Append(arrow.Date64(1))
	rec := b.NewRecordBatch()
	defer rec.Release()
	b.Release()
	_, err := QueryBatch(nil, rec, func(s string) string { return "SELECT * FROM " + s }, "TSV")
	if err == nil || !strings.Contains(err.Error(), "unsupported input type") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCAllocatorCopiesAndReleases(t *testing.T) {
	a := cAllocator{}
	b := a.Allocate(4)
	copy(b, "test")
	b = a.Reallocate(8, b)
	if string(b[:4]) != "test" {
		t.Fatalf("reallocation lost data: %q", b[:4])
	}
	a.Free(b)
}

func TestInsertRejectsSQLFragments(t *testing.T) {
	for _, name := range []string{"", "db..table", "table; DROP TABLE x", "table name", "0table"} {
		if _, err := quotePath(name); err == nil {
			t.Errorf("accepted unsafe identifier %q", name)
		}
	}
	if got, err := quotePath("db.events"); err != nil || got != "`db`.`events`" {
		t.Fatalf("qualified identifier = %q, %v", got, err)
	}
}
