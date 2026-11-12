package chdbpurego

import (
	"reflect"
	"testing"
	"unsafe"
)

func TestArrowStreamClosePolicy(t *testing.T) {
	oldCancel, oldDestroy := chdbStreamCancelQuery, chdbDestroyQueryResult
	defer func() {
		chdbStreamCancelQuery = oldCancel
		chdbDestroyQueryResult = oldDestroy
	}()

	inner := new(byte)
	conn := &connection{conn: &chdb_connection{internal_data: unsafe.Pointer(inner)}}
	result := &chdb_result{}
	var calls []string
	chdbStreamCancelQuery = func(gotConn unsafe.Pointer, gotResult *chdb_result) {
		if gotConn != unsafe.Pointer(inner) || gotResult != result {
			t.Error("cancel received a different connection handle or result")
		}
		calls = append(calls, "cancel")
	}
	chdbDestroyQueryResult = func(gotResult *chdb_result) {
		if gotResult != result {
			t.Error("destroy received a different result")
		}
		calls = append(calls, "destroy")
	}

	active := &arrowStream{conn: conn, result: result}
	active.Close(true)
	active.Close(true) // idempotent
	if want := []string{"cancel", "destroy"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("active stream close calls = %v, want %v", calls, want)
	}

	calls = nil
	finished := &arrowStream{conn: conn, result: result}
	finished.Close(false)
	if want := []string{"destroy"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("finished stream close calls = %v, want %v", calls, want)
	}
}
