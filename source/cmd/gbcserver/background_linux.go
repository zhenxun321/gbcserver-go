//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// launchBackground 在 Linux 上创建脱离当前终端的单一 start 进程。
// 子进程仍然同时运行 Web、白名单和兑换 listener；日志和 PID 放在统一数据目录。
func launchBackground(args []string, dataDir string, lockFile *os.File) (bool, error) {
	if os.Getenv("GBCSERVER_DAEMON") == "1" {
		return false, nil
	}
	if lockFile == nil {
		return false, errors.New("后台启动缺少单实例锁")
	}
	absDataDir, err := filepath.Abs(strings.TrimSpace(dataDir))
	if err != nil {
		return false, fmt.Errorf("解析后台数据目录失败: %w", err)
	}
	if err := os.MkdirAll(absDataDir, 0o700); err != nil {
		return false, fmt.Errorf("创建后台日志目录失败: %w", err)
	}
	logPath := filepath.Join(absDataDir, "gbcserver.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return false, fmt.Errorf("打开后台日志失败: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		logFile.Close()
		return false, fmt.Errorf("解析程序路径失败: %w", err)
	}
	childArgs := make([]string, 0, len(args))
	for _, argument := range args {
		if argument == "--background" || argument == "--background=true" || strings.HasPrefix(argument, "--autostart") || strings.HasPrefix(argument, "--no-autostart") {
			continue
		}
		childArgs = append(childArgs, argument)
	}
	command := exec.Command(executable, childArgs...)
	command.Env = make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GBCSERVER_DAEMON=") || strings.HasPrefix(entry, "GBCSERVER_INSTANCE_LOCK_FD=") {
			continue
		}
		command.Env = append(command.Env, entry)
	}
	command.Env = append(command.Env, "GBCSERVER_DAEMON=1", "GBCSERVER_INSTANCE_LOCK_FD="+strconv.Itoa(3))
	command.ExtraFiles = []*os.File{lockFile}
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		logFile.Close()
		return false, fmt.Errorf("启动后台进程失败: %w", err)
	}
	_ = logFile.Close()
	pid := command.Process.Pid
	_ = command.Process.Release()
	if err := waitForControlState(dataDir, 10*time.Second); err != nil {
		fmt.Fprintf(os.Stdout, "GBC Server 已转入后台运行，但管理通道尚未就绪：%v\n", err)
		fmt.Fprintf(os.Stdout, "PID=%d\n日志：%s\n", pid, logPath)
		return true, nil
	}
	fmt.Fprintf(os.Stdout, "GBC Server 已转入后台运行。PID=%d\n日志：%s\n", pid, logPath)
	return true, nil
}

// waitForControlState 避免 start 返回后立即 stop/reload 读不到子进程状态文件。
func waitForControlState(dataDir string, timeout time.Duration) error {
	path := filepath.Join(dataDir, controlFileName)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			var state controlState
			if json.Unmarshal(data, &state) == nil && strings.TrimSpace(state.Address) != "" && strings.TrimSpace(state.Token) != "" {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("等待 %s 超时", controlFileName)
}

func writeBackgroundPID(dataDir string) error {
	pidPath := filepath.Join(dataDir, "gbcserver.pid")
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		return fmt.Errorf("写入后台 PID 失败: %w", err)
	}
	return nil
}

// cleanupBackgroundPID 仅由后台子进程退出时调用，避免删除其他实例的新 PID。
func cleanupBackgroundPID(dataDir string) {
	if os.Getenv("GBCSERVER_DAEMON") != "1" {
		return
	}
	pidPath := filepath.Join(dataDir, "gbcserver.pid")
	data, err := os.ReadFile(pidPath)
	if err != nil || strings.TrimSpace(string(data)) != fmt.Sprintf("%d", os.Getpid()) {
		return
	}
	_ = os.Remove(pidPath)
}
