package chdbdriver

import (
	"database/sql/driver"
	"fmt"
	"io"
	"time"
	"unsafe"

	"reflect"

	"github.com/chdb-io/chdb-go/chdbstable"
	"github.com/parquet-go/parquet-go"
)

func bytesToString(data []byte) string {
	return *(*string)(unsafe.Pointer(&data))
}

func getStringFromBytes(v parquet.Value) string {
	return bytesToString(v.ByteArray())
}

type parquetRows struct {
	localResult   *chdbstable.LocalResult     // result from clickhouse
	reader        *parquet.GenericReader[any] // parquet reader
	curRecord     parquet.Row                 // TODO: delete this?
	buffer        []parquet.Row               // record buffer
	bufferSize    int                         // amount of records to preload into buffer
	bufferIndex   int64                       // index in the current buffer
	curRow        int64                       // row counter
	needNewBuffer bool
}

func (r *parquetRows) Columns() (out []string) {
	sch := r.reader.Schema()
	for _, f := range sch.Fields() {
		out = append(out, f.Name())
	}

	return
}

func (r *parquetRows) Close() error {
	if r.curRecord != nil {
		r.curRecord = nil
	}
	// ignore reader close
	_ = r.reader.Close()
	r.reader = nil
	r.localResult = nil
	r.buffer = nil
	return nil
}

func (r *parquetRows) readNextChunk() error {
	r.buffer = make([]parquet.Row, r.bufferSize)
	readAmount, err := r.reader.ReadRows(r.buffer)
	if err == io.EOF && readAmount == 0 {
		return err // no records read, should exit the loop
	}
	if err == io.EOF && readAmount > 0 {
		return nil //here we are at EOF, but since we read at least 1 record, we should consume it
	}
	if readAmount == 0 {
		return io.EOF //same thing
	}
	if readAmount < r.bufferSize {
		r.buffer = r.buffer[:readAmount] //eliminate empty items so the loop will exit before
	}
	r.bufferIndex = 0
	r.needNewBuffer = false
	return nil
}

func (r *parquetRows) Next(dest []driver.Value) error {
	if r.curRow == 0 && r.localResult.RowsRead() == 0 {
		return io.EOF //here we can simply return early since we don't need to issue a read to the file
	}
	if r.needNewBuffer {
		err := r.readNextChunk()
		if err != nil {
			return err
		}

	}
	r.curRecord = r.buffer[r.bufferIndex]
	if r.curRecord == nil || len(r.curRecord) == 0 {
		return fmt.Errorf("empty row")
	}
	var scanError error
	r.curRecord.Range(func(columnIndex int, columnValues []parquet.Value) bool {
		if len(columnValues) != 1 {
			return false
		}
		curVal := columnValues[0]
		if curVal.IsNull() {
			dest[columnIndex] = nil
			return true
		}
		switch r.ColumnTypeDatabaseTypeName(columnIndex) {
		case "STRING":
			dest[columnIndex] = getStringFromBytes(curVal)
		case "INT8", "INT(8,true)":
			dest[columnIndex] = int8(curVal.Int32()) //check if this is correct
		case "INT16", "INT(16,true)":
			dest[columnIndex] = int16(curVal.Int32())
		case "INT64", "INT(64,true)":
			dest[columnIndex] = curVal.Int64()
		case "INT(64,false)":
			dest[columnIndex] = curVal.Uint64()
		case "INT(32,false)":
			dest[columnIndex] = curVal.Uint32()
		case "INT(8,false)":
			dest[columnIndex] = uint8(curVal.Uint32()) //check if this is correct
		case "INT(16,false)":
			dest[columnIndex] = uint16(curVal.Uint32())
		case "INT32", "INT(32,true)":
			dest[columnIndex] = curVal.Int32()
		case "FLOAT32":
			dest[columnIndex] = curVal.Float()
		case "DOUBLE":
			dest[columnIndex] = curVal.Double()
		case "BOOLEAN":
			dest[columnIndex] = curVal.Boolean()
		case "BYTE_ARRAY", "FIXED_LEN_BYTE_ARRAY":
			dest[columnIndex] = curVal.ByteArray()
		case "TIMESTAMP(isAdjustedToUTC=true,unit=MILLIS)", "TIME(isAdjustedToUTC=true,unit=MILLIS)":
			dest[columnIndex] = time.UnixMilli(curVal.Int64()).UTC()
		case "TIMESTAMP(isAdjustedToUTC=true,unit=MICROS)", "TIME(isAdjustedToUTC=true,unit=MICROS)":
			dest[columnIndex] = time.UnixMicro(curVal.Int64()).UTC()
		case "TIMESTAMP(isAdjustedToUTC=true,unit=NANOS)", "TIME(isAdjustedToUTC=true,unit=NANOS)":
			dest[columnIndex] = time.Unix(0, curVal.Int64()).UTC()
		case "TIMESTAMP(isAdjustedToUTC=false,unit=MILLIS)", "TIME(isAdjustedToUTC=false,unit=MILLIS)":
			dest[columnIndex] = time.UnixMilli(curVal.Int64())
		case "TIMESTAMP(isAdjustedToUTC=false,unit=MICROS)", "TIME(isAdjustedToUTC=false,unit=MICROS)":
			dest[columnIndex] = time.UnixMicro(curVal.Int64())
		case "TIMESTAMP(isAdjustedToUTC=false,unit=NANOS)", "TIME(isAdjustedToUTC=false,unit=NANOS)":
			dest[columnIndex] = time.Unix(0, curVal.Int64())
		default:
			scanError = fmt.Errorf("could not cast to type: %s", r.ColumnTypeDatabaseTypeName(columnIndex))
			return false

		}
		return true
	})
	if scanError != nil {
		return scanError
	}
	r.curRow++
	r.bufferIndex++
	r.needNewBuffer = r.bufferIndex == int64(len(r.buffer)) // if we achieved the buffer size, we need a new one
	return nil
}

func (r *parquetRows) ColumnTypeDatabaseTypeName(index int) string {
	return r.reader.Schema().Fields()[index].Type().String()
}

func (r *parquetRows) ColumnTypeNullable(index int) (nullable, ok bool) {
	return r.reader.Schema().Fields()[index].Optional(), true
}

func (r *parquetRows) ColumnTypePrecisionScale(index int) (precision, scale int64, ok bool) {
	return 0, 0, false
}

func (r *parquetRows) ColumnTypeScanType(index int) reflect.Type {
	switch r.reader.Schema().Fields()[index].Type().Kind() {
	case parquet.Boolean:
		return reflect.TypeOf(false)
	case parquet.Int32:
		return reflect.TypeOf(int32(0))
	case parquet.Int64:
		return reflect.TypeOf(int64(0))
	case parquet.Float:
		return reflect.TypeOf(float32(0))
	case parquet.Double:
		return reflect.TypeOf(float64(0))
	case parquet.ByteArray, parquet.FixedLenByteArray:
		return reflect.TypeOf("")
	}
	return nil
}
