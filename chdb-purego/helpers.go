package chdbpurego

import (
	"runtime"
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

func stringToPtr(s string, pinner *runtime.Pinner) *byte {
	// Pinne for convert string to bytes
	// maybe there is simpler solution but it was late when I write this code.
	data := make([]byte, len(s)+1)
	copy(data, s)
	data[len(s)] = 0 // Null-terminator

	ptr := &data[0]
	pinner.Pin(ptr)

	return (*byte)(unsafe.Pointer(ptr))
}

func strToBytePtr(s string) *byte {
	b := append([]byte(s), 0) // Convert to []byte and append null terminator
	return &b[0]              // Get pointer to first byte
}
