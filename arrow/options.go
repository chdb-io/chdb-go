//go:build cgo && (darwin || linux)

package chdbarrow

import "github.com/chdb-io/chdb-go/v2/internal/nativearrow"

// OutputOptions controls the type mapping of direct Arrow results. Zero value
// is the core default: unsupported types error, LowCardinality materializes
// to its base type, and ClickHouse String becomes Arrow UTF-8. Opting into a
// fallback is explicit; it never changes the query to Arrow IPC.
type OutputOptions struct {
	UnsupportedAsBinary        bool
	LowCardinalityAsDictionary bool
	StringAsBinary             bool
}

func (o OutputOptions) native() *nativearrow.OutputOptions {
	out := &nativearrow.OutputOptions{StringAsString: 1}
	if o.UnsupportedAsBinary {
		out.UnsupportedAsBinary = 1
	}
	if o.LowCardinalityAsDictionary {
		out.LowCardinalityAsDictionary = 1
	}
	if o.StringAsBinary {
		out.StringAsString = 0
	}
	return out
}
