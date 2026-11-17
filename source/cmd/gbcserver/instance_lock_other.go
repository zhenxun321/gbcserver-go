//go:build !linux && !windows

package main

import "os"

type otherInstanceLock struct{}

func acquireInstanceLock() (instanceLock, error)    { return &otherInstanceLock{}, nil }
func (*otherInstanceLock) Release()                 {}
func (*otherInstanceLock) BackgroundFile() *os.File { return nil }
