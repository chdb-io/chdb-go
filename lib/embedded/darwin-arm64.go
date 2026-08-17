//go:build darwin && arm64

package embedded

import (
	chdbpurego "github.com/chdb-io/chdb-go/v2/chdb-purego"
	engine "github.com/chdb-io/chdb-go/lib/darwin-arm64"
)

func init() {
	chdbpurego.RegisterEmbeddedEngine(chdbpurego.EmbeddedEngine{
		Version:  engine.Version,
		FileName: engine.FileName,
		Digest:   engine.Digest,
		Size:     engine.Size,
		Open:     engine.Open,
	})
}
