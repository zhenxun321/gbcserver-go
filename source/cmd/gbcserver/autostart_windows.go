//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

func configureAutostartPlatform(dataDir string, install bool) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开当前用户启动项失败: %w", err)
	}
	defer key.Close()
	const valueName = "GBCServer"
	if !install {
		if err := key.DeleteValue(valueName); err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("删除 Windows 自启项失败: %w", err)
		}
		return nil
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
	command := windowsCommandArg(executable) + " start --tray --data-dir " + windowsCommandArg(dataDir)
	if err := key.SetStringValue(valueName, command); err != nil {
		return fmt.Errorf("写入 Windows 自启项失败: %w", err)
	}
	return nil
}

func windowsCommandArg(value string) string {
	// Run 项由 ShellExecute 解析；普通路径中的反斜杠不需要加倍，只有双引号需要转义。
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}
