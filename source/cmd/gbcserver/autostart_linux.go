//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func configureAutostartPlatform(dataDir string, install bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("解析用户主目录失败: %w", err)
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	unitPath := filepath.Join(unitDir, "gbcserver.service")
	if !install {
		// systemd 没有运行时也允许清理 unit，便于离线卸载。
		_ = runSystemctlUser("disable", "gbcserver.service")
		if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除 systemd 自启文件失败: %w", err)
		}
		_ = runSystemctlUser("daemon-reload")
		return nil
	}
	if err := os.MkdirAll(unitDir, 0o700); err != nil {
		return fmt.Errorf("创建 systemd 自启目录失败: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("解析程序路径失败: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("解析程序绝对路径失败: %w", err)
	}
	dataDir, err = filepath.Abs(strings.TrimSpace(dataDir))
	if err != nil {
		return fmt.Errorf("解析 --data-dir 失败: %w", err)
	}
	command := systemdArg(executable) + " start --data-dir " + systemdArg(dataDir)
	content := "[Unit]\nDescription=GBC Server unified Web and plugin service\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\nWorkingDirectory=" + systemdArg(filepath.Dir(executable)) + "\nExecStart=" + command + "\nRestart=on-failure\nRestartSec=3\nUMask=0077\n\n[Install]\nWantedBy=default.target\n"
	if err := os.WriteFile(unitPath, []byte(content), 0o600); err != nil {
		return fmt.Errorf("写入 systemd 自启文件失败: %w", err)
	}
	if err := runSystemctlUser("daemon-reload"); err != nil {
		return err
	}
	if err := runSystemctlUser("enable", "gbcserver.service"); err != nil {
		return err
	}
	return nil
}

func runSystemctlUser(arguments ...string) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("未找到 systemctl，已生成 unit 但无法启用: %w", err)
	}
	commandArgs := append([]string{"--user"}, arguments...)
	command := exec.Command("systemctl", commandArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("systemctl --user %s 失败: %s", strings.Join(arguments, " "), message)
	}
	return nil
}

func systemdArg(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, " ", "\\x20")
	value = strings.ReplaceAll(value, "\t", "\\x09")
	value = strings.ReplaceAll(value, "\"", "\\x22")
	return value
}
