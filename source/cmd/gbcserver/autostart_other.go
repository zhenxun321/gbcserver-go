//go:build !linux && !windows

package main

import "errors"

func configureAutostartPlatform(_ string, _ bool) error {
	return errors.New("当前平台暂不支持开机自启")
}
