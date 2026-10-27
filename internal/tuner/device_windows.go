//go:build windows

package tuner

import (
	"context"
	"errors"
)

func openFrontendDevice(path string) (int, error) {
	return -1, errWindowsDVBUnsupported
}

func openDVRDevice(path string) (int, error) {
	return -1, errWindowsDVBUnsupported
}

func closeDeviceFD(fd int) error {
	return nil
}

func drainDVRDevice(fd int, buf []byte) (int, error) {
	return 0, nil
}

func pollAndReadDVR(ctx context.Context, dvrFD int, buf []byte) (int, error) {
	return 0, errors.New("DVR polling not supported on Windows")
}
