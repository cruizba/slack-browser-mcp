//go:build !windows

package forum

import (
	"errors"
	"os"
	"syscall"
)

// alive asks the kernel whether a pid exists, without signalling it.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	err = process.Signal(syscall.Signal(0))

	return err == nil || errors.Is(err, os.ErrPermission)
}
