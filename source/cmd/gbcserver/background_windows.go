//go:build windows

package main

import (
	"errors"
	"os"
)

// Windows 使用托盘/登录启动，不支持 Linux 的 --background 守护化参数。
func launchBackground(_ []string, _ string, _ *os.File) (bool, error) {
	return false, errors.New("Windows 不支持 --background，请使用 start 或开机自启")
}

func writeBackgroundPID(_ string) error { return nil }
func cleanupBackgroundPID(_ string)     {}
