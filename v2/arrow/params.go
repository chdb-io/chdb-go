//go:build cgo && (darwin || linux)

package chdbarrow

import (
	"errors"
	"fmt"

	"github.com/chdb-io/chdb-go/v2/internal/nativearrow"
)

// Parameter binds a ClickHouse {name:Type} placeholder for one query. Value
// is a textual representation parsed as Type by the engine; it is never
// interpolated into the SQL string. Values can contain arbitrary bytes.
type Parameter struct {
	Name, Value string
}

func nativeParams(params []Parameter) ([]nativearrow.Parameter, error) {
	if len(params) == 0 {
		return nil, nil
	}
	out := make([]nativearrow.Parameter, len(params))
	seen := make(map[string]bool, len(params))
	for i, p := range params {
		if !validParamName(p.Name) || seen[p.Name] {
			return nil, fmt.Errorf("chdbarrow: invalid or duplicate parameter name %q", p.Name)
		}
		seen[p.Name] = true
		out[i] = nativearrow.Parameter{Name: p.Name, Value: p.Value}
	}
	return out, nil
}

func validParamName(name string) bool {
	if name == "" {
		return false
	}
	for i, ch := range name {
		if ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || i > 0 && ch >= '0' && ch <= '9' {
			continue
		}
		return false
	}
	return true
}

func requireParamBinding(c nativearrow.Capabilities, params []nativearrow.Parameter) error {
	if len(params) != 0 && !c.ParamBinding {
		return fmt.Errorf("%w; engine does not export streaming Arrow parameter binding", ErrArrowEngineIncompatible)
	}
	return nil
}

var errNilSQLConn = errors.New("chdbarrow: nil database/sql connection")
