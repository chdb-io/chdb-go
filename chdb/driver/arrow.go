package chdbdriver

import (
	"database/sql/driver"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/apache/arrow/go/v15/arrow"
	"github.com/apache/arrow/go/v15/arrow/array"
	"github.com/apache/arrow/go/v15/arrow/decimal128"
	"github.com/apache/arrow/go/v15/arrow/decimal256"
	"github.com/apache/arrow/go/v15/arrow/ipc"
	"github.com/chdb-io/chdb-go/chdbstable"
)

type arrowRows struct {
	localResult *chdbstable.LocalResult
	reader      *ipc.FileReader
	curRecord   arrow.Record
	curRow      int64
	fd          *os.File
}

func (r *arrowRows) Columns() (out []string) {
	sch := r.reader.Schema()
	for i := 0; i < sch.NumFields(); i++ {
		out = append(out, sch.Field(i).Name)
	}
	return
}

func (r *arrowRows) Close() error {
	if r.curRecord != nil {
		r.curRecord = nil
	}
	// ignore reader close
	_ = r.reader.Close()
	r.reader = nil
	r.localResult = nil
	if r.fd != nil {
		_ = r.fd.Close()
		_ = os.Remove(r.fd.Name())
	}
	return nil
}

func (r *arrowRows) Next(dest []driver.Value) error {
	if r.curRecord != nil && r.curRow == r.curRecord.NumRows() {
		r.curRecord = nil
	}
	for r.curRecord == nil {
		record, err := r.reader.Read()
		if err != nil {
			return err
		}
		if record.NumRows() == 0 {
			continue
		}
		r.curRecord = record
		r.curRow = 0
	}

	for i, col := range r.curRecord.Columns() {
		if col.IsNull(int(r.curRow)) {
			dest[i] = nil
			continue
		}
		switch col := col.(type) {
		case *array.Boolean:
			dest[i] = col.Value(int(r.curRow))
		case *array.Int8:
			dest[i] = col.Value(int(r.curRow))
		case *array.Uint8:
			dest[i] = col.Value(int(r.curRow))
		case *array.Int16:
			dest[i] = col.Value(int(r.curRow))
		case *array.Uint16:
			dest[i] = col.Value(int(r.curRow))
		case *array.Int32:
			dest[i] = col.Value(int(r.curRow))
		case *array.Uint32:
			dest[i] = col.Value(int(r.curRow))
		case *array.Int64:
			dest[i] = col.Value(int(r.curRow))
		case *array.Uint64:
			dest[i] = col.Value(int(r.curRow))
		case *array.Float32:
			dest[i] = col.Value(int(r.curRow))
		case *array.Float64:
			dest[i] = col.Value(int(r.curRow))
		case *array.String:
			dest[i] = col.Value(int(r.curRow))
		case *array.LargeString:
			dest[i] = col.Value(int(r.curRow))
		case *array.Binary:
			dest[i] = col.Value(int(r.curRow))
		case *array.LargeBinary:
			dest[i] = col.Value(int(r.curRow))
		case *array.Date32:
			dest[i] = col.Value(int(r.curRow)).ToTime()
		case *array.Date64:
			dest[i] = col.Value(int(r.curRow)).ToTime()
		case *array.Time32:
			dest[i] = col.Value(int(r.curRow)).ToTime(col.DataType().(*arrow.Time32Type).Unit)
		case *array.Time64:
			dest[i] = col.Value(int(r.curRow)).ToTime(col.DataType().(*arrow.Time64Type).Unit)
		case *array.Timestamp:
			dest[i] = col.Value(int(r.curRow)).ToTime(col.DataType().(*arrow.TimestampType).Unit)
		case *array.Decimal128:
			dest[i] = col.Value(int(r.curRow))
		case *array.Decimal256:
			dest[i] = col.Value(int(r.curRow))
		default:
			return fmt.Errorf(
				"not yet implemented populating from columns of type " + col.DataType().String(),
			)
		}
	}

	r.curRow++
	return nil
}

func (r *arrowRows) ColumnTypeDatabaseTypeName(index int) string {
	return r.reader.Schema().Field(index).Type.String()
}

func (r *arrowRows) ColumnTypeNullable(index int) (nullable, ok bool) {
	return r.reader.Schema().Field(index).Nullable, true
}

func (r *arrowRows) ColumnTypePrecisionScale(index int) (precision, scale int64, ok bool) {
	typ := r.reader.Schema().Field(index).Type
	switch dt := typ.(type) {
	case *arrow.Decimal128Type:
		return int64(dt.Precision), int64(dt.Scale), true
	case *arrow.Decimal256Type:
		return int64(dt.Precision), int64(dt.Scale), true
	}
	return 0, 0, false
}

func (r *arrowRows) ColumnTypeScanType(index int) reflect.Type {
	switch r.reader.Schema().Field(index).Type.ID() {
	case arrow.BOOL:
		return reflect.TypeOf(false)
	case arrow.INT8:
		return reflect.TypeOf(int8(0))
	case arrow.UINT8:
		return reflect.TypeOf(uint8(0))
	case arrow.INT16:
		return reflect.TypeOf(int16(0))
	case arrow.UINT16:
		return reflect.TypeOf(uint16(0))
	case arrow.INT32:
		return reflect.TypeOf(int32(0))
	case arrow.UINT32:
		return reflect.TypeOf(uint32(0))
	case arrow.INT64:
		return reflect.TypeOf(int64(0))
	case arrow.UINT64:
		return reflect.TypeOf(uint64(0))
	case arrow.FLOAT32:
		return reflect.TypeOf(float32(0))
	case arrow.FLOAT64:
		return reflect.TypeOf(float64(0))
	case arrow.DECIMAL128:
		return reflect.TypeOf(decimal128.Num{})
	case arrow.DECIMAL256:
		return reflect.TypeOf(decimal256.Num{})
	case arrow.BINARY:
		return reflect.TypeOf([]byte{})
	case arrow.STRING:
		return reflect.TypeOf(string(""))
	case arrow.TIME32, arrow.TIME64, arrow.DATE32, arrow.DATE64, arrow.TIMESTAMP:
		return reflect.TypeOf(time.Time{})
	}
	return nil
}
