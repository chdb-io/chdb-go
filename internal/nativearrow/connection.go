// Package nativearrow is the narrow bridge between the optional Arrow module
// and a chdb Session. It is internal so callers cannot obtain a raw native
// connection or keep one after the scoped callback returns.
package nativearrow

import (
	"sync"
	"unsafe"
)

// Connection is valid only during Session.WithNativeArrow's callback or while
// a Session-issued Lease remains active. The caller owns any input stream and
// must unregister it before releasing its memory.
type Connection interface {
	Capabilities() Capabilities
	RegisterStream(name string, stream unsafe.Pointer) error
	UnregisterStream(name string) error
	QueryBytes(sql, format string) ([]byte, error)
	QueryArrow(sql string, outStream unsafe.Pointer, options *OutputOptions) error
	QueryArrowStream(sql string, options *OutputOptions) (Stream, error)
	QueryArrowStreamParams(sql string, options *OutputOptions, params []Parameter) (Stream, error)
}

// Parameter is a per-call ClickHouse {name:Type} binding. Value is parsed by
// the engine according to the placeholder's type, not interpolated into SQL.
type Parameter struct {
	Name, Value string
}

// Capabilities reports exactly the Arrow symbols available in the loaded
// library. Presence does not by itself certify a particular core behavior.
type Capabilities struct {
	EngineVersion string
	Input         bool
	TableOutput   bool
	ReaderOutput  bool
	ParamBinding  bool
}

// OutputOptions matches the three int fields of chdb_arrow_options in chdb.h.
// A nil pointer asks core for its default type contract.
type OutputOptions struct {
	UnsupportedAsBinary        int32
	LowCardinalityAsDictionary int32
	StringAsString             int32
}

// Stream owns a native streaming query result. Fetch fills one caller-owned C
// Stream; its release callback must be invoked independently. Close waits for
// Fetch, then optionally cancels the still-active query before destroying its
// handle. Callers must pass false at EOF to avoid cancelling retired state.
type Stream interface {
	Fetch(outStream unsafe.Pointer) error
	Close(cancel bool)
}

// Lease pins a Session connection until its owner retires its native handles.
type Lease struct {
	Conn    Connection
	once    sync.Once
	release func()
}

func NewLease(conn Connection, release func()) *Lease {
	return &Lease{Conn: conn, release: release}
}

func (l *Lease) Release() {
	if l != nil {
		l.once.Do(l.release)
	}
}
