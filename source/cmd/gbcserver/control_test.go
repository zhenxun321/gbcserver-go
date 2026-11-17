package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestControlServerForwardsReloadAndStop(t *testing.T) {
	dataDir := t.TempDir()
	commands := make(chan managedCommand, 1)
	server, err := startControlServer(dataDir, commands)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	for _, expected := range []managedCommand{
		{Action: "reload"},
		{Action: "stop"},
	} {
		if err := sendControlCommand(dataDir, expected.Action); err != nil {
			t.Fatalf("发送 %s 请求失败: %v", expected.Action, err)
		}
		select {
		case actual := <-commands:
			if actual != expected {
				t.Fatalf("管理请求不匹配: got=%+v want=%+v", actual, expected)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("未收到 %s 请求", expected.Action)
		}
	}

	server.Close()
	if _, err := os.Stat(filepath.Join(dataDir, controlFileName)); !os.IsNotExist(err) {
		t.Fatalf("管理通道关闭后应删除状态文件，err=%v", err)
	}
}
