//go:build windows

package forum

import "os"

// alive asks Windows whether a pid exists: FindProcess fails when there is nobody to
// open a handle for.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	_ = process.Release()

	return true
}
