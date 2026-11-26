//go:build cgo && (darwin || linux)

package chdbarrow

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

func readAll(t *testing.T, column arrow.Array, field arrow.Field) []any {
	t.Helper()
	read, err := newValueReader(column, field)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]any, column.Len())
	for i := range out {
		out[i] = read(i)
	}
	return out
}

func TestValueReaderScalars(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	ints := array.NewInt32Builder(mem)
	ints.AppendValues([]int32{1, -2}, nil)
	ints.AppendNull()
	intArray := ints.NewArray()
	ints.Release()
	defer intArray.Release()
	if got := readAll(t, intArray, arrow.Field{Type: intArray.DataType()}); !reflect.DeepEqual(got, []any{int32(1), int32(-2), nil}) {
		t.Fatalf("Int32 values = %#v", got)
	}

	strs := array.NewStringBuilder(mem)
	strs.AppendValues([]string{"héllo", ""}, nil)
	strArray := strs.NewArray()
	strs.Release()
	got := readAll(t, strArray, arrow.Field{Type: strArray.DataType()})
	strArray.Release()
	// The values are copies, so they outlive the array.
	if !reflect.DeepEqual(got, []any{"héllo", ""}) {
		t.Fatalf("String values = %#v", got)
	}

	bins := array.NewBinaryBuilder(mem, arrow.BinaryTypes.Binary)
	bins.Append([]byte{0xff, 0x00})
	bins.Append(nil)
	binArray := bins.NewArray()
	bins.Release()
	defer binArray.Release()
	if got := readAll(t, binArray, arrow.Field{Type: binArray.DataType()}); !reflect.DeepEqual(got, []any{[]byte{0xff, 0x00}, []byte{}}) {
		t.Fatalf("Binary values = %#v", got)
	}

	decType := &arrow.Decimal128Type{Precision: 10, Scale: 2}
	decs := array.NewDecimal128Builder(mem, decType)
	decs.Append(decimal128.FromI64(-1234))
	decArray := decs.NewArray()
	decs.Release()
	defer decArray.Release()
	if got := readAll(t, decArray, arrow.Field{Type: decType}); !reflect.DeepEqual(got, []any{"-12.34"}) {
		t.Fatalf("Decimal values = %#v", got)
	}
}

func TestValueReaderTime(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	tsType := &arrow.TimestampType{Unit: arrow.Millisecond, TimeZone: "Asia/Shanghai"}
	ts := array.NewTimestampBuilder(mem, tsType)
	ts.Append(arrow.Timestamp(1_700_000_000_123))
	tsArray := ts.NewArray()
	ts.Release()
	defer tsArray.Release()
	got := readAll(t, tsArray, arrow.Field{Type: tsType})[0].(time.Time)
	if !got.Equal(time.UnixMilli(1_700_000_000_123)) || got.Location().String() != "Asia/Shanghai" {
		t.Fatalf("Timestamp value = %v (%v)", got, got.Location())
	}

	dates := array.NewDate32Builder(mem)
	dates.Append(arrow.Date32FromTime(time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)))
	dateArray := dates.NewArray()
	dates.Release()
	defer dateArray.Release()
	if got := readAll(t, dateArray, arrow.Field{Type: dateArray.DataType()})[0].(time.Time); !got.Equal(time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("Date32 value = %v", got)
	}
}

func TestValueReaderNested(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	lists := array.NewListBuilder(mem, arrow.PrimitiveTypes.Int64)
	values := lists.ValueBuilder().(*array.Int64Builder)
	lists.Append(true)
	values.AppendValues([]int64{1, 2}, nil)
	lists.Append(true)
	listArray := lists.NewArray()
	lists.Release()
	defer listArray.Release()
	if got := readAll(t, listArray, arrow.Field{Type: listArray.DataType()}); !reflect.DeepEqual(got, []any{[]any{int64(1), int64(2)}, []any{}}) {
		t.Fatalf("List values = %#v", got)
	}

	structType := arrow.StructOf(
		arrow.Field{Name: "id", Type: arrow.PrimitiveTypes.Uint8},
		arrow.Field{Name: "name", Type: arrow.BinaryTypes.String, Nullable: true},
	)
	structs := array.NewStructBuilder(mem, structType)
	structs.Append(true)
	structs.FieldBuilder(0).(*array.Uint8Builder).Append(7)
	structs.FieldBuilder(1).(*array.StringBuilder).AppendNull()
	structArray := structs.NewArray()
	structs.Release()
	defer structArray.Release()
	if got := readAll(t, structArray, arrow.Field{Type: structType}); !reflect.DeepEqual(got, []any{map[string]any{"id": uint8(7), "name": nil}}) {
		t.Fatalf("Struct values = %#v", got)
	}

	maps := array.NewMapBuilder(mem, arrow.BinaryTypes.Binary, arrow.PrimitiveTypes.Float64, false)
	maps.Append(true)
	maps.KeyBuilder().(*array.BinaryBuilder).Append([]byte("k"))
	maps.ItemBuilder().(*array.Float64Builder).Append(1.5)
	mapArray := maps.NewArray()
	maps.Release()
	defer mapArray.Release()
	// A binary key becomes a string, which a Go map can hold.
	if got := readAll(t, mapArray, arrow.Field{Type: mapArray.DataType()}); !reflect.DeepEqual(got, []any{map[any]any{"k": 1.5}}) {
		t.Fatalf("Map values = %#v", got)
	}

	uuids := array.NewFixedSizeBinaryBuilder(mem, &arrow.FixedSizeBinaryType{ByteWidth: 16})
	uuids.Append([]byte{0x12, 0x3e, 0x45, 0x67, 0xe8, 0x9b, 0x12, 0xd3, 0xa4, 0x56, 0x42, 0x66, 0x14, 0x17, 0x40, 0x00})
	uuidArray := uuids.NewArray()
	uuids.Release()
	defer uuidArray.Release()
	field := arrow.Field{Type: uuidArray.DataType(), Metadata: arrow.NewMetadata([]string{"ARROW:extension:name"}, []string{"arrow.uuid"})}
	if got := readAll(t, uuidArray, field); !reflect.DeepEqual(got, []any{"123e4567-e89b-12d3-a456-426614174000"}) {
		t.Fatalf("UUID values = %#v", got)
	}
}

func TestValueReaderUnsupported(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)
	b := array.NewFloat16Builder(mem)
	b.AppendNull()
	a := b.NewArray()
	b.Release()
	defer a.Release()
	if _, err := newValueReader(a, arrow.Field{Type: a.DataType()}); err == nil || !strings.Contains(err.Error(), "use BatchExecutor") {
		t.Fatalf("expected an unsupported type error, got %v", err)
	}
}

func TestBuildResultKinds(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	moment := time.Date(2024, 1, 2, 3, 4, 5, 6, time.UTC)
	far := time.Date(2290, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		values []any
		typ    arrow.DataType
		want   string
	}{
		{"null", []any{nil, nil}, arrow.Null, "[(null) (null)]"},
		{"bool", []any{true, nil, false}, arrow.FixedWidthTypes.Boolean, "[true (null) false]"},
		{"int", []any{1, int8(-2), int64(3), nil}, arrow.PrimitiveTypes.Int64, "[1 -2 3 (null)]"},
		{"int and uint", []any{-1, uint64(2)}, arrow.PrimitiveTypes.Int64, "[-1 2]"},
		{"uint", []any{uint8(1), uint64(math.MaxUint64)}, arrow.PrimitiveTypes.Uint64, "[1 18446744073709551615]"},
		{"float", []any{1, 2.5, float32(0.5)}, arrow.PrimitiveTypes.Float64, "[1 2.5 0.5]"},
		{"string", []any{"a", nil}, arrow.BinaryTypes.String, `["a" (null)]`},
		{"bytes", []any{"a", []byte("b")}, arrow.BinaryTypes.Binary, `["a" "b"]`},
		{"time", []any{moment}, &arrow.TimestampType{Unit: arrow.Nanosecond, TimeZone: "UTC"}, "[1704164645000000006]"},
		{"far time", []any{far}, &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}, "[10098259200000000]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := buildResult(c.values, mem)
			if err != nil {
				t.Fatal(err)
			}
			defer got.Release()
			if !arrow.TypeEqual(got.DataType(), c.typ) || got.Len() != len(c.values) {
				t.Fatalf("type %s len %d, want %s len %d", got.DataType(), got.Len(), c.typ, len(c.values))
			}
			if got.String() != c.want {
				t.Fatalf("values %s, want %s", got.String(), c.want)
			}
		})
	}
}

func TestBuildResultErrors(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	cases := []struct {
		name   string
		values []any
		want   string
	}{
		{"mixed", []any{1, "a"}, "RowExecutor returned string for row 1"},
		{"unsupported", []any{struct{}{}}, "use BatchExecutor"},
		{"uint too large", []any{-1, uint64(math.MaxUint64)}, "Int64 cannot hold"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := buildResult(c.values, mem)
			if err == nil {
				got.Release()
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not contain %q", err, c.want)
			}
		})
	}
}
