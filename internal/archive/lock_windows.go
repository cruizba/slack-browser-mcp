//go:build windows

package archive

import "os"

// processAlive asks whether a pid exists. On Windows, os.FindProcess opens a
// handle and fails when there is nobody to open one for, which is the answer this
// needs without an extra dependency.
func processAlive(pid int) bool {
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
