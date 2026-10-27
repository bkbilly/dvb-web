//go:build windows

package webrtc

import "syscall"

func setReuseSocketOptions(fd uintptr) {
	_ = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
}
