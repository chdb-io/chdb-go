//go:build chdb_static && chdb_static_manual && cgo && linux && (amd64 || arm64)

package chdbpurego

// Only the explicit manual-archive mode asks the linker to find libchdb.a.
// Module consumers get relocatable .syso inputs from the opt-in module instead.
// Do not put these flags in CGO_LDFLAGS: it is repeated for every cgo package.

/*
#cgo LDFLAGS: -Wl,--whole-archive -l:libchdb.a -Wl,--no-whole-archive
*/
import "C"
