package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"gbcserver/source/internal/store"
)

func reserveTCPPorts(t *testing.T, count int) []int {
	t.Helper()
	listeners := make([]net.Listener, 0, count)
	ports := make([]int, 0, count)
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	for range count {
		listener, err := net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
		ports = append(ports, listener.Addr().(*net.TCPAddr).Port)
	}
	return ports
}

func prepareManagedTestData(t *testing.T, httpPort, whitelistPort, couponPort int) string {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "gbcserver")
	if _, _, err := bootstrapDeployment(dataDir, ""); err != nil {
		t.Fatal(err)
	}
	db, err := openNormalizedDB(filepath.Join(dataDir, "gbcserver.db"))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"httpPort":        strconv.Itoa(httpPort),
		"whitelistListen": "127.0.0.1:" + strconv.Itoa(whitelistPort),
		"couponListen":    "127.0.0.1:" + strconv.Itoa(couponPort),
	} {
		if err := (store.ConfigStore{DB: db}).Set(context.Background(), key, value); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dataDir
}

func TestManagedStartPublishesControlAfterListenersAreReady(t *testing.T) {
	ports := reserveTCPPorts(t, 3)
	dataDir := prepareManagedTestData(t, ports[0], ports[1], ports[2])
	result := make(chan int, 1)
	go func() { result <- runManagedStart(dataDir, "", false) }()

	controlPath := filepath.Join(dataDir, controlFileName)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(controlPath); err == nil {
			break
		}
		select {
		case code := <-result:
			t.Fatalf("managed service exited before publishing its control state: %d", code)
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	data, err := os.ReadFile(controlPath)
	if err != nil {
		t.Fatalf("listeners became ready without a control state: %v", err)
	}
	var state controlState
	if err := json.Unmarshal(data, &state); err != nil || state.Address == "" || state.Token == "" {
		t.Fatalf("published control state is invalid: state=%+v err=%v", state, err)
	}
	if err := sendControlCommand(dataDir, "stop"); err != nil {
		t.Fatalf("immediate stop after readiness failed: %v", err)
	}
	select {
	case code := <-result:
		if code != 0 {
			t.Fatalf("managed service stop returned %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("managed service did not stop after receiving control request")
	}
	if _, err := os.Stat(controlPath); !os.IsNotExist(err) {
		t.Fatalf("control state remained after shutdown: %v", err)
	}
}

func TestManagedStartDoesNotPublishControlWhenWebListenerFails(t *testing.T) {
	occupied, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	pluginPorts := reserveTCPPorts(t, 2)
	dataDir := prepareManagedTestData(t, port, pluginPorts[0], pluginPorts[1])
	code := runManagedStart(dataDir, "", false)
	if code == 0 {
		t.Fatal("managed start should fail when the configured Web port is occupied")
	}
	if _, err := os.Stat(filepath.Join(dataDir, controlFileName)); !os.IsNotExist(err) {
		t.Fatalf("failed startup published a control state: %v", err)
	}
}
