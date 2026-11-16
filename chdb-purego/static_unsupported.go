//go:build chdb_static && (!cgo || !(darwin || linux) || !(arm64 || amd64))

package chdbpurego

// An unresolved identifier gives a deliberate build-time error rather than
// silently falling back to runtime loading on an unsupported target.
var _ = chdb_static_requires_cgo_and_linux_or_darwin_on_amd64_or_arm64
