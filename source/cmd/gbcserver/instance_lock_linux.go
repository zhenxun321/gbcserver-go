//go:build linux

package main

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

const instanceLockEnvironment = "GBCSERVER_INSTANCE_LOCK_FD"

type linuxInstanceLock struct {
	file *os.File
}

func acquireInstanceLock() (instanceLock, error) {
	if inherited := os.Getenv(instanceLockEnvironment); inherited != "" {
		fd, err := strconv.Atoi(inherited)
		if err != nil || fd < 3 {
			return nil, fmt.Errorf("继承的单实例锁文件描述符无效")
		}
		file := os.NewFile(uintptr(fd), "gbcserver-instance-lock")
		if file == nil {
			return nil, fmt.Errorf("继承的单实例锁文件不可用")
		}
		_ = os.Unsetenv(instanceLockEnvironment)
		return &linuxInstanceLock{file: file}, nil
	}

	return acquireLinuxInstanceLockAt("/tmp/gbcserver.instance.lock")
}

func acquireLinuxInstanceLockAt(path string) (instanceLock, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o666)
	if err != nil {
		return nil, fmt.Errorf("打开系统单实例锁失败: %w", err)
	}
	if err := syscall.Fchmod(fd, 0o666); err != nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("设置系统单实例锁权限失败: %w", err)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = syscall.Close(fd)
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, errInstanceAlreadyRunning
		}
		return nil, fmt.Errorf("加锁系统单实例文件失败: %w", err)
	}
	return &linuxInstanceLock{file: os.NewFile(uintptr(fd), "gbcserver-instance-lock")}, nil
}

func (lock *linuxInstanceLock) Release() {
	if lock != nil && lock.file != nil {
		_ = syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
		_ = lock.file.Close()
		lock.file = nil
	}
}

func (lock *linuxInstanceLock) BackgroundFile() *os.File {
	if lock == nil {
		return nil
	}
	return lock.file
}
