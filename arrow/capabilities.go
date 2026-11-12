//go:build cgo && (darwin || linux)

package chdbarrow

import (
	"github.com/chdb-io/chdb-go/v2/chdb"
	"github.com/chdb-io/chdb-go/v2/internal/nativearrow"
)

// Capabilities reports which native Arrow functions the selected libchdb
// exports. It does not certify that an old release implements the current
// ownership contract; behavior and supported types still need integration
// tests against that exact engine artifact.
type Capabilities struct {
	EngineVersion      string
	ContractCompatible bool
	Input              bool
	TableOutput        bool
	ReaderOutput       bool
}

func GetCapabilities(sess *chdb.Session) (Capabilities, error) {
	var out Capabilities
	err := sess.WithNativeArrow(func(conn nativearrow.Connection) error {
		c := conn.Capabilities()
		out = Capabilities{
			EngineVersion:      c.EngineVersion,
			ContractCompatible: compatibleVersion(c.EngineVersion),
			Input:              c.Input,
			TableOutput:        c.TableOutput,
			ReaderOutput:       c.ReaderOutput,
		}
		return nil
	})
	return out, err
}
