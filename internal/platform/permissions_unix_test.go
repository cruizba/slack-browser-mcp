//go:build !windows

package platform_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

func TestHarden(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "secrets.json")

	if err := os.WriteFile(file, []byte("{}"), 0o644); err != nil { //nolint:gosec // the point of the test is to start from a file anyone can read
		t.Fatalf("writing: %v", err)
	}

	if err := platform.Harden(file); err != nil {
		t.Fatalf("Harden: %v", err)
	}

	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %#o, want 0600", mode)
	}
}

func TestPrivate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode os.FileMode
		want bool
	}{
		{name: "owner read and write", mode: 0o600, want: true},
		{name: "owner read only", mode: 0o400, want: true},
		{name: "group can read", mode: 0o640, want: false},
		{name: "everybody can read", mode: 0o644, want: false},
		{name: "everybody can write", mode: 0o666, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			file := filepath.Join(t.TempDir(), "secrets.json")

			if err := os.WriteFile(file, []byte("{}"), tt.mode); err != nil {
				t.Fatalf("writing: %v", err)
			}

			if err := os.Chmod(file, tt.mode); err != nil {
				t.Fatalf("chmod: %v", err)
			}

			private, mode, err := platform.Private(file)
			if err != nil {
				t.Fatalf("Private: %v", err)
			}

			if private != tt.want {
				t.Fatalf("Private() = %v (mode %#o), want %v", private, mode, tt.want)
			}
		})
	}

	t.Run("a missing file is an error, not a private file", func(t *testing.T) {
		t.Parallel()

		_, _, err := platform.Private(filepath.Join(t.TempDir(), "absent.json"))
		if err == nil {
			t.Fatal("Private() accepted a file that does not exist")
		}
	})
}
