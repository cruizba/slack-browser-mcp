//go:build windows

package chrome

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// isExecutable only checks for a file: Windows has no executable mode bit.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// terminateProcess kills the browser process only. Chrome's children exit with
// it in practice; a Job Object is the airtight answer if the port needs one.
func terminateProcess(cmd *exec.Cmd, exited chan struct{}) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
	}
}
