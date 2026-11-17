//go:build !windows

package main

import "errors"

func runWindowsTray(_, _, _ string, _ <-chan trayUpdate) error {
	return errors.New("当前平台不支持 Windows 托盘")
}
