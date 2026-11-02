//go:build !windows

package platform

import (
	"fmt"
	"os"
	"syscall"
)

// Harden makes a file reachable by its owner only. On these systems that means
// the mode bits, which is also what the credential store checks before reading.
func Harden(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", path, err)
	}

	return nil
}

// HardenDir is [Harden] for a directory, which needs the execute bit to be
// entered at all: 0600 on a directory locks its owner out of its own files.
func HardenDir(path string) error {
	if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // a directory needs the execute bit to be entered; 0700 is the tightest that works
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

// CurrentUID is the user this process runs as.
func CurrentUID() (int, bool) {
	return os.Getuid(), true
}

// OwnerUID is the user that owns the file, when the system records one.
func OwnerUID(info os.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}

	return int(stat.Uid), true
}

// CheckOwner refuses a file that belongs to somebody else. The mode bits stop
// other users from reading it later; they say nothing about how it got here.
func CheckOwner(path string, info os.FileInfo) error {
	owner, ok := OwnerUID(info)
	if !ok {
		return nil
	}

	current, ok := CurrentUID()
	if !ok || owner == current {
		return nil
	}

	return fmt.Errorf("Refusing to read %s: it belongs to uid %d, not %d.", path, owner, current)
}
