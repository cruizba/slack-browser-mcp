//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// isExecutable honours X_OK, as findChromeBinary does in the TypeScript.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}

// configureProcess puts Chrome in its own session so it survives this process
// and so the whole tree can be signalled at once, like spawn({detached:true}).
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// terminateProcess sends SIGTERM to the group, then SIGKILL after 5s.
func terminateProcess(cmd *exec.Cmd, exited chan struct{}) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	select {
	case <-exited:
		return
	case <-time.After(5 * time.Second):
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
	}
}
