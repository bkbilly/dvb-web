package transcoder

import (
	"os/exec"
	"syscall"
)

func checkProcessExited(cmd *exec.Cmd) (bool, int) {
	if cmd == nil || cmd.Process == nil {
		return false, 0
	}
	var ws syscall.WaitStatus
	pid, err := syscall.Wait4(cmd.Process.Pid, &ws, syscall.WNOHANG, nil)
	if err == nil && pid == cmd.Process.Pid {
		exitCode := ws.ExitStatus()
		if ws.Signaled() {
			exitCode = 128 + int(ws.Signal())
		}
		return true, exitCode
	}
	return false, 0
}
