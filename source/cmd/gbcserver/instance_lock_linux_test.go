//go:build linux

package main

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLinuxInstanceLockExcludesAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	first, err := acquireLinuxInstanceLockAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLinuxInstanceLockAt(path); !errors.Is(err, errInstanceAlreadyRunning) {
		first.Release()
		t.Fatalf("同一锁文件应拒绝第二个实例: %v", err)
	}
	first.Release()
	third, err := acquireLinuxInstanceLockAt(path)
	if err != nil {
		t.Fatalf("释放锁后应允许重新启动: %v", err)
	}
	third.Release()
}
