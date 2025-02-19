package chdbpurego

import (
	"unsafe"
)

func ptrToGoString(ptr *byte) string {
	if ptr == nil {
		return ""
	}

	var length int
	for {
		if *(*byte)(unsafe.Pointer(uintptr(unsafe.Pointer(ptr)) + uintptr(length))) == 0 {
			break
		}
		length++
	}

	return string(unsafe.Slice(ptr, length))
}
