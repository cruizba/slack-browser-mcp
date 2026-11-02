//go:build windows

package chrome

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// isExecutable answers whether a path could be run. Windows has no execute bit, so
// what decides is the extension: Go's exec only starts a file it recognises, and every
// Chrome or Chromium build is a .exe.
func isExecutable(path string) bool {
	if !strings.EqualFold(filepath.Ext(path), ".exe") {
		return false
	}

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
