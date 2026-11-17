//go:build !linux && !windows

package main

import (
	"errors"
	"os"
)

func launchBackground(_ []string, _ string, _ *os.File) (bool, error) {
	return false, errors.New("当前平台不支持 --background")
}

func writeBackgroundPID(_ string) error { return nil }
func cleanupBackgroundPID(_ string)     {}
