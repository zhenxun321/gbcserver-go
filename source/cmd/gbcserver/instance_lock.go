package main

import (
	"errors"
	"os"
)

var errInstanceAlreadyRunning = errors.New("GBC Server 已有实例运行")

type instanceLock interface {
	Release()
	BackgroundFile() *os.File
}
