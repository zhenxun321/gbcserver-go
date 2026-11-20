package main

import (
	"syscall"
	"unsafe"
)

var getUserDefaultLocaleName = syscall.NewLazyDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName")

func systemLanguage() string {
	buffer := make([]uint16, 85)
	result, _, _ := getUserDefaultLocaleName.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if result == 0 {
		return ""
	}
	return syscall.UTF16ToString(buffer)
}
