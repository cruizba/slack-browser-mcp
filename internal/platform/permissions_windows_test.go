//go:build windows

package platform_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// The contract on Windows is not a number of bits: it is that the store works,
// because refusing every file whose mode is not 0600 would refuse every file.
// The mode the platform reports is whatever Windows synthesised from the read-only
// attribute, and it exists for messages.
func TestPrivate_ReportsAccessibleOnWindows(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "secrets.json")

	if err := os.WriteFile(file, []byte("{}"), 0o666); err != nil {
		t.Fatalf("writing: %v", err)
	}

	private, _, err := platform.Private(file)
	if err != nil {
		t.Fatalf("Private: %v", err)
	}

	if !private {
		t.Fatal("Private() said no, which would block every credential file on this system")
	}

	if err := platform.Harden(file); err != nil {
		t.Fatalf("Harden: %v", err)
	}
}
