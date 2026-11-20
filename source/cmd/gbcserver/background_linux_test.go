//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWaitForControlStateStopsWhenChildExitsBeforeReady(t *testing.T) {
	childDone := make(chan struct{})
	close(childDone)
	err := waitForControlState(t.TempDir(), time.Second, childDone)
	if err == nil || !strings.Contains(err.Error(), "listener 就绪前退出") {
		t.Fatalf("expected an early child exit error, got %v", err)
	}
}

func TestWaitForControlStateAcceptsPublishedReadyState(t *testing.T) {
	dataDir := t.TempDir()
	childDone := make(chan struct{})
	state := controlState{Network: "unix", Address: filepath.Join(dataDir, "control.sock"), Token: "test-token"}
	go func() {
		time.Sleep(25 * time.Millisecond)
		content, _ := json.Marshal(state)
		_ = os.WriteFile(filepath.Join(dataDir, controlFileName), content, 0o600)
	}()
	if err := waitForControlState(dataDir, time.Second, childDone); err != nil {
		t.Fatalf("ready control state was not accepted: %v", err)
	}
}

func TestCleanupFailedBackgroundStartRemovesOnlyOwnedPID(t *testing.T) {
	dataDir := t.TempDir()
	controlPath := filepath.Join(dataDir, controlFileName)
	pidPath := filepath.Join(dataDir, "gbcserver.pid")
	if err := os.WriteFile(controlPath, []byte(`{"address":"/tmp/control.sock","token":"token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidPath, []byte("1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleanupFailedBackgroundStart(dataDir, 1234)
	if _, err := os.Stat(controlPath); !os.IsNotExist(err) {
		t.Fatalf("control state remained after failed startup cleanup: %v", err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("owned PID file remained after failed startup cleanup: %v", err)
	}

	if err := os.WriteFile(pidPath, []byte("5678\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanupFailedBackgroundStart(dataDir, 1234)
	if _, err := os.Stat(pidPath); err != nil {
		t.Fatalf("PID file for another process was removed: %v", err)
	}
}
