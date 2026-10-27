//go:build windows

package transcoder

import (
	"os/exec"
	"syscall"
)

func checkProcessExited(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	// Open process handle with query and synchronize rights
	const (
		processQueryLimitedInformation = 0x1000
		synchronize                    = 0x00100000
		waitObject0                    = 0
	)
	h, err := syscall.OpenProcess(processQueryLimitedInformation|synchronize, false, uint32(cmd.Process.Pid))
	if err != nil {
		return true // Process is already dead or cannot be opened
	}
	defer syscall.CloseHandle(h)

	s, err := syscall.WaitForSingleObject(h, 0)
	return err == nil && s == waitObject0
}
