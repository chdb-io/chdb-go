//go:build chdb_static && chdb_static_manual && cgo && linux && (amd64 || arm64)

package chdbpurego

// Only the explicit manual-archive mode asks the linker to find libchdb.a.
// Module consumers get relocatable .syso inputs from the opt-in module instead.
// Do not put these flags in CGO_LDFLAGS: it is repeated for every cgo package.
// Keep system libraries after the archive for GNU ld's --as-needed policy.

/*
#cgo LDFLAGS: -Wl,--whole-archive -l:libchdb.a -Wl,--no-whole-archive -lc -lm -lrt -lpthread -ldl
*/
import "C"
