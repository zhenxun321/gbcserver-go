//go:build windows

package main

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsInstanceLock struct {
	handle windows.Handle
}

func acquireInstanceLock() (instanceLock, error) {
	return acquireWindowsInstanceLock(`Global\GBCServer.Instance`)
}

func acquireWindowsInstanceLock(instanceName string) (instanceLock, error) {
	name, err := windows.UTF16PtrFromString(instanceName)
	if err != nil {
		return nil, fmt.Errorf("构造系统单实例锁名称失败: %w", err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:(A;;GA;;;WD)")
	if err != nil {
		return nil, fmt.Errorf("设置系统单实例锁权限失败: %w", err)
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	handle, err := windows.CreateEvent(attributes, 1, 0, name)
	if err != nil {
		if err == windows.ERROR_ALREADY_EXISTS {
			_ = windows.CloseHandle(handle)
			return nil, errInstanceAlreadyRunning
		}
		return nil, fmt.Errorf("创建系统单实例事件失败: %w", err)
	}
	return &windowsInstanceLock{handle: handle}, nil
}

func (lock *windowsInstanceLock) Release() {
	if lock != nil && lock.handle != 0 {
		_ = windows.CloseHandle(lock.handle)
		lock.handle = 0
	}
}

func (lock *windowsInstanceLock) BackgroundFile() *os.File { return nil }
