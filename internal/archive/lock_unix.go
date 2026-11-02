//go:build !windows

package archive

import (
	"errors"
	"os"
	"syscall"
)

// processAlive asks the kernel whether a pid exists without signalling it.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// On Unix, signal 0 checks for existence; ESRCH means it is gone, EPERM means
	// it exists but belongs to somebody else, which still counts as alive.
	err = process.Signal(syscall.Signal(0))

	return err == nil || errors.Is(err, os.ErrPermission)
}
