//go:build cgo && (darwin || linux)

package chdbarrow

import (
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// valueReader returns the Go value of one row of an Arrow array.
type valueReader func(row int) driver.Value

// newValueReader reads column as the RowExecutor documents. field describes the
// column; its metadata names an Arrow extension type the import did not resolve.
func newValueReader(column arrow.Array, field arrow.Field) (valueReader, error) {
	read, err := newNonNullReader(column, field)
	if err != nil {
		return nil, err
	}
	if column.NullN() == 0 {
		return read, nil
	}
	return func(row int) driver.Value {
		if column.IsNull(row) {
			return nil
		}
		return read(row)
	}, nil
}

func newNonNullReader(column arrow.Array, field arrow.Field) (valueReader, error) {
	if extensionName(field) == "arrow.uuid" {
		if a, ok := column.(*array.FixedSizeBinary); ok {
			return func(row int) driver.Value { return formatUUID(a.Value(row)) }, nil
		}
	}

	switch a := column.(type) {
	case array.ExtensionArray:
		if a.ExtensionType().ExtensionName() == "arrow.uuid" {
			if storage, ok := a.Storage().(*array.FixedSizeBinary); ok {
				return func(row int) driver.Value { return formatUUID(storage.Value(row)) }, nil
			}
		}
		return newValueReader(a.Storage(), arrow.Field{Type: a.Storage().DataType()})
	case *array.Null:
		return func(int) driver.Value { return nil }, nil
	case *array.Boolean:
		return func(row int) driver.Value { return a.Value(row) }, nil
	case *array.Int8:
		return func(row int) driver.Value { return a.Value(row) }, nil
	case *array.Int16:
		return func(row int) driver.Value { return a.Value(row) }, nil
	case *array.Int32:
		return func(row int) driver.Value { return a.Value(row) }, nil
	case *array.Int64:
		return func(row int) driver.Value { return a.Value(row) }, nil
	case *array.Uint8:
		return func(row int) driver.Value { return a.Value(row) }, nil
	case *array.Uint16:
		return func(row int) driver.Value { return a.Value(row) }, nil
	case *array.Uint32:
		return func(row int) driver.Value { return a.Value(row) }, nil
	case *array.Uint64:
		return func(row int) driver.Value { return a.Value(row) }, nil
	case *array.Float32:
		return func(row int) driver.Value { return a.Value(row) }, nil
	case *array.Float64:
		return func(row int) driver.Value { return a.Value(row) }, nil
	// Copies: the arguments are released when the call returns.
	case *array.String:
		return func(row int) driver.Value { return strings.Clone(a.Value(row)) }, nil
	case *array.LargeString:
		return func(row int) driver.Value { return strings.Clone(a.Value(row)) }, nil
	case *array.Binary:
		return func(row int) driver.Value { return append([]byte{}, a.Value(row)...) }, nil
	case *array.LargeBinary:
		return func(row int) driver.Value { return append([]byte{}, a.Value(row)...) }, nil
	case *array.FixedSizeBinary:
		return func(row int) driver.Value { return append([]byte{}, a.Value(row)...) }, nil
	case *array.Date32:
		return func(row int) driver.Value { return a.Value(row).ToTime() }, nil
	case *array.Date64:
		return func(row int) driver.Value { return a.Value(row).ToTime() }, nil
	case *array.Timestamp:
		typ := a.DataType().(*arrow.TimestampType)
		location := time.UTC
		if typ.TimeZone != "" {
			loaded, err := time.LoadLocation(typ.TimeZone)
			if err != nil {
				return nil, fmt.Errorf("time zone %q: %w", typ.TimeZone, err)
			}
			location = loaded
		}
		toTime, err := typ.GetToTimeFunc()
		if err != nil {
			return nil, err
		}
		return func(row int) driver.Value { return toTime(a.Value(row)).In(location) }, nil
	case *array.Decimal128:
		scale := a.DataType().(*arrow.Decimal128Type).Scale
		return func(row int) driver.Value { return a.Value(row).ToString(scale) }, nil
	case *array.Decimal256:
		scale := a.DataType().(*arrow.Decimal256Type).Scale
		return func(row int) driver.Value { return a.Value(row).ToString(scale) }, nil
	case *array.Map:
		mapType := a.DataType().(*arrow.MapType)
		keys, err := newValueReader(a.Keys(), mapType.KeyField())
		if err != nil {
			return nil, fmt.Errorf("map key: %w", err)
		}
		items, err := newValueReader(a.Items(), mapType.ItemField())
		if err != nil {
			return nil, fmt.Errorf("map value: %w", err)
		}
		return func(row int) driver.Value {
			start, end := a.ValueOffsets(row)
			out := make(map[any]any, end-start)
			for i := int(start); i < int(end); i++ {
				key := keys(i)
				// A []byte is not comparable, so it cannot be a Go map key.
				if b, ok := key.([]byte); ok {
					key = string(b)
				}
				out[key] = items(i)
			}
			return out
		}, nil
	case array.ListLike:
		elements, err := newValueReader(a.ListValues(), listElementField(a.DataType()))
		if err != nil {
			return nil, fmt.Errorf("array element: %w", err)
		}
		return func(row int) driver.Value {
			start, end := a.ValueOffsets(row)
			out := make([]any, 0, end-start)
			for i := int(start); i < int(end); i++ {
				out = append(out, elements(i))
			}
			return out
		}, nil
	case *array.Struct:
		structType := a.DataType().(*arrow.StructType)
		fields := make([]valueReader, a.NumField())
		for i := range fields {
			reader, err := newValueReader(a.Field(i), structType.Field(i))
			if err != nil {
				return nil, fmt.Errorf("tuple element %q: %w", structType.Field(i).Name, err)
			}
			fields[i] = reader
		}
		return func(row int) driver.Value {
			out := make(map[string]any, len(fields))
			for i, read := range fields {
				out[structType.Field(i).Name] = read(row)
			}
			return out
		}, nil
	}
	return nil, fmt.Errorf("RowExecutor cannot read Arrow type %s; use BatchExecutor", column.DataType())
}

func listElementField(t arrow.DataType) arrow.Field {
	if list, ok := t.(arrow.ListLikeType); ok {
		return list.ElemField()
	}
	return arrow.Field{}
}

func extensionName(field arrow.Field) string {
	name, _ := field.Metadata.GetValue("ARROW:extension:name")
	return name
}

func formatUUID(b []byte) string {
	if len(b) != 16 {
		return hex.EncodeToString(b)
	}
	s := hex.EncodeToString(b)
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}

// resultKind is the Arrow type a RowExecutor's results are built as.
type resultKind int

const (
	kindNull resultKind = iota
	kindBool
	kindInt
	kindUint
	kindFloat
	kindString
	kindBytes
	kindTime
)

func kindOf(v any) (resultKind, error) {
	switch v.(type) {
	case nil:
		return kindNull, nil
	case bool:
		return kindBool, nil
	case int, int8, int16, int32, int64:
		return kindInt, nil
	case uint, uint8, uint16, uint32, uint64:
		return kindUint, nil
	case float32, float64:
		return kindFloat, nil
	case string:
		return kindString, nil
	case []byte:
		return kindBytes, nil
	case time.Time:
		return kindTime, nil
	}
	return 0, fmt.Errorf("RowExecutor returned %T; return nil, bool, an integer, a float, string, []byte or time.Time, or use BatchExecutor", v)
}

func isNumericKind(k resultKind) bool { return k == kindInt || k == kindUint || k == kindFloat }

// mergeKinds picks one Arrow type for two kinds of results: signed integers
// for signed and unsigned ones, floats for integers and floats, binary for
// strings and byte slices.
func mergeKinds(a, b resultKind) (resultKind, bool) {
	switch {
	case a == b || b == kindNull:
		return a, true
	case a == kindNull:
		return b, true
	case a == kindFloat || b == kindFloat:
		return kindFloat, isNumericKind(a) && isNumericKind(b)
	case isNumericKind(a) && isNumericKind(b):
		return kindInt, true
	case (a == kindString && b == kindBytes) || (a == kindBytes && b == kindString):
		return kindBytes, true
	}
	return 0, false
}

// buildResult turns the results of a batch into one Arrow array allocated by
// mem; chDB converts it to the declared result type.
func buildResult(values []any, mem memory.Allocator) (arrow.Array, error) {
	kind := kindNull
	for row, v := range values {
		k, err := kindOf(v)
		if err != nil {
			return nil, err
		}
		merged, ok := mergeKinds(kind, k)
		if !ok {
			return nil, fmt.Errorf("RowExecutor returned %T for row %d after values of another type", v, row)
		}
		kind = merged
	}

	switch kind {
	case kindNull:
		return array.NewNull(len(values)), nil
	case kindBool:
		b := array.NewBooleanBuilder(mem)
		defer b.Release()
		b.Reserve(len(values))
		for _, v := range values {
			if v == nil {
				b.AppendNull()
			} else {
				b.Append(v.(bool))
			}
		}
		return b.NewArray(), nil
	case kindInt:
		b := array.NewInt64Builder(mem)
		defer b.Release()
		b.Reserve(len(values))
		for _, v := range values {
			if v == nil {
				b.AppendNull()
				continue
			}
			n, err := toInt64(v)
			if err != nil {
				return nil, err
			}
			b.Append(n)
		}
		return b.NewArray(), nil
	case kindUint:
		b := array.NewUint64Builder(mem)
		defer b.Release()
		b.Reserve(len(values))
		for _, v := range values {
			if v == nil {
				b.AppendNull()
			} else {
				b.Append(toUint64(v))
			}
		}
		return b.NewArray(), nil
	case kindFloat:
		b := array.NewFloat64Builder(mem)
		defer b.Release()
		b.Reserve(len(values))
		for _, v := range values {
			if v == nil {
				b.AppendNull()
			} else {
				b.Append(toFloat64(v))
			}
		}
		return b.NewArray(), nil
	case kindString:
		b := array.NewStringBuilder(mem)
		defer b.Release()
		b.Reserve(len(values))
		for _, v := range values {
			if v == nil {
				b.AppendNull()
			} else {
				b.Append(v.(string))
			}
		}
		return b.NewArray(), nil
	case kindBytes:
		b := array.NewBinaryBuilder(mem, arrow.BinaryTypes.Binary)
		defer b.Release()
		b.Reserve(len(values))
		for _, v := range values {
			switch v := v.(type) {
			case nil:
				b.AppendNull()
			case string:
				b.AppendString(v)
			case []byte:
				b.Append(v)
			}
		}
		return b.NewArray(), nil
	case kindTime:
		// Nanoseconds hold 1678 to 2262; microseconds the rest of what chDB can store.
		unit := arrow.Nanosecond
		for _, v := range values {
			if t, ok := v.(time.Time); ok && (t.Year() < 1678 || t.Year() > 2261) {
				unit = arrow.Microsecond
				break
			}
		}
		b := array.NewTimestampBuilder(mem, &arrow.TimestampType{Unit: unit, TimeZone: "UTC"})
		defer b.Release()
		b.Reserve(len(values))
		for _, v := range values {
			if v == nil {
				b.AppendNull()
				continue
			}
			t := v.(time.Time)
			if unit == arrow.Nanosecond {
				b.Append(arrow.Timestamp(t.UnixNano()))
			} else {
				b.Append(arrow.Timestamp(t.UnixMicro()))
			}
		}
		return b.NewArray(), nil
	}
	return nil, errors.New("unreachable result kind")
}

func toInt64(v any) (int64, error) {
	switch v := v.(type) {
	case int:
		return int64(v), nil
	case int8:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	}
	u := toUint64(v)
	if u > math.MaxInt64 {
		return 0, fmt.Errorf("RowExecutor returned %d among signed integers, which Int64 cannot hold", u)
	}
	return int64(u), nil
}

func toUint64(v any) uint64 {
	switch v := v.(type) {
	case uint:
		return uint64(v)
	case uint8:
		return uint64(v)
	case uint16:
		return uint64(v)
	case uint32:
		return uint64(v)
	case uint64:
		return v
	}
	return 0
}

func toFloat64(v any) float64 {
	switch v := v.(type) {
	case float32:
		return float64(v)
	case float64:
		return v
	case int, int8, int16, int32, int64:
		n, _ := toInt64(v)
		return float64(n)
	}
	return float64(toUint64(v))
}
