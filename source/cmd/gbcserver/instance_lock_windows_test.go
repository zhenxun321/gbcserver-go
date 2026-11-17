//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestWindowsInstanceLockExcludesAndReleases(t *testing.T) {
	name := fmt.Sprintf(`Global\GBCServer.Test.%d.%d`, os.Getpid(), time.Now().UnixNano())
	first, err := acquireWindowsInstanceLock(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireWindowsInstanceLock(name); !errors.Is(err, errInstanceAlreadyRunning) {
		first.Release()
		t.Fatalf("同一命名事件应拒绝第二个实例: %v", err)
	}
	first.Release()
	third, err := acquireWindowsInstanceLock(name)
	if err != nil {
		t.Fatalf("关闭事件后应允许重新启动: %v", err)
	}
	third.Release()
}
