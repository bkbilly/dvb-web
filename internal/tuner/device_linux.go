//go:build !windows

package tuner

import (
	"context"
	"syscall"

	"golang.org/x/sys/unix"
)

func openFrontendDevice(path string) (int, error) {
	return syscall.Open(path, syscall.O_RDWR, 0)
}

func openDVRDevice(path string) (int, error) {
	return syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
}

func closeDeviceFD(fd int) error {
	if fd > 0 {
		return syscall.Close(fd)
	}
	return nil
}

func drainDVRDevice(fd int, buf []byte) (int, error) {
	return syscall.Read(fd, buf)
}

// pollAndReadDVR waits up to 50ms for data ready on dvrFD and reads into buf
func pollAndReadDVR(ctx context.Context, dvrFD int, buf []byte) (int, error) {
	pfd := []unix.PollFd{{Fd: int32(dvrFD), Events: unix.POLLIN}}
	nReady, err := unix.Poll(pfd, 50)
	if err != nil {
		if err == unix.EINTR {
			return 0, nil
		}
		return 0, err
	}
	if nReady == 0 {
		return 0, nil
	}

	n, err := syscall.Read(dvrFD, buf)
	if err != nil {
		if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK || err == syscall.EINTR {
			return 0, nil
		}
		return 0, err
	}
	return n, nil
}
