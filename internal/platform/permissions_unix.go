//go:build !windows

package platform

import (
	"fmt"
	"os"
)

// Harden makes a file reachable by its owner only. On these systems that means
// the mode bits, which is also what the credential store checks before reading.
func Harden(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", path, err)
	}

	return nil
}

// Private reports whether only the owner can reach the file, and the mode it
// found, so the caller can put the number in its own message.
func Private(path string) (bool, os.FileMode, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, 0, fmt.Errorf("checking %s: %w", path, err)
	}

	mode := info.Mode().Perm()

	// Any bit outside the owner's own is a way in for somebody else.
	return mode&0o077 == 0, mode, nil
}
