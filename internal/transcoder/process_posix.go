//go:build !windows

package transcoder

import (
	"os/exec"
	"syscall"
)

func checkProcessExited(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	var ws syscall.WaitStatus
	pid, err := syscall.Wait4(cmd.Process.Pid, &ws, syscall.WNOHANG, nil)
	return err == nil && pid == cmd.Process.Pid
}
