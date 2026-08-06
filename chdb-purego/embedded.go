package chdbpurego

import (
	"fmt"
	"io"
	"sync"
)

// EmbeddedEngine describes a libchdb payload compiled into the calling binary.
//
// A per-platform module supplies one of these through RegisterEmbeddedEngine so
// that `go get` is all a user needs: no system-wide install, no separate
// download step, and the engine version is pinned by the build rather than by
// whatever happens to be present on the machine.
type EmbeddedEngine struct {
	// Version is the chdb-core release the payload was built from, used in
	// diagnostics so a bug report says which engine is running.
	Version string

	// FileName is the name the library must be written under. The published
	// macOS archive calls its Mach-O library libchdb.so, so this is not
	// derivable from the platform.
	FileName string

	// Digest is the lowercase hex SHA-256 of the extracted library. It names
	// the cache directory, which is what makes concurrent extraction safe:
	// every process computes the same destination, so whichever one publishes
	// it first has produced exactly what the others would have.
	Digest string

	// Size is the extracted size in bytes. Only used to make "not enough
	// space" errors actionable.
	Size int64

	// Open returns the extracted library bytes. Decompression lives in the
	// platform module so that this package needs no compression dependency
	// and the payload format can change without touching the loader.
	Open func() (io.ReadCloser, error)
}

var (
	embeddedMu     sync.Mutex
	embeddedEngine *EmbeddedEngine
)

// RegisterEmbeddedEngine records the engine payload built into this binary. It
// is called from a platform module's init and panics if called twice, which can
// only happen if two platform modules were linked in at once — a build
// configuration that would otherwise silently pick one of them.
func RegisterEmbeddedEngine(e EmbeddedEngine) {
	embeddedMu.Lock()
	defer embeddedMu.Unlock()
	if embeddedEngine != nil {
		panic(fmt.Sprintf("chdb: two embedded engines linked in (%s and %s)", embeddedEngine.Version, e.Version))
	}
	if e.Digest == "" || e.FileName == "" || e.Open == nil {
		panic("chdb: incomplete embedded engine registration")
	}
	embeddedEngine = &e
}

func registeredEngine() *EmbeddedEngine {
	embeddedMu.Lock()
	defer embeddedMu.Unlock()
	return embeddedEngine
}

// EmbeddedEngineVersion returns the chdb-core release whose engine is compiled
// into this binary, or the empty string for a build that resolves the library
// from the system.
func EmbeddedEngineVersion() string {
	if e := registeredEngine(); e != nil {
		return e.Version
	}
	return ""
}
