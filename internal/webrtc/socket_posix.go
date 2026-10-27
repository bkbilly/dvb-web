//go:build !windows

package webrtc

import "syscall"

func setReuseSocketOptions(fd uintptr) {
	_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, 15, 1) // SO_REUSEPORT
	_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
}
