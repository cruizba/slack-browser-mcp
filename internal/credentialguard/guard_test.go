package credentialguard_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/credentialguard"
)

func TestRepositoryCarriesNoRealCredential(t *testing.T) {
	t.Parallel()

	findings, err := credentialguard.Scan("../..")
	if err != nil {
		t.Fatalf("scanning the repository: %v", err)
	}

	for _, finding := range findings {
		t.Errorf("%s carries a real %s; remove it and revoke it before pushing", finding.File, finding.Kind)
	}
}

func TestPatternsSeparateRealFromPlaceholder(t *testing.T) {
	t.Parallel()

	realShapes := []string{
		"xoxc-1234567890123-1234567890123-1234567890123-" + strings.Repeat("a", 24),
		"xoxd-" + strings.Repeat("A", 80),
	}

	placeholders := []string{
		"xoxc-test-token",
		"xoxc-difftest-token",
		"xoxd-abcdef%2Bghij",
		"xoxc-1234567890-abcdef",
		"xoxc-spike-v1",
	}

	for _, value := range realShapes {
		if !looksReal(t, value) {
			t.Errorf("the guard does not catch %q", value)
		}
	}

	for _, value := range placeholders {
		if looksReal(t, value) {
			t.Errorf("the guard would refuse the placeholder %q", value)
		}
	}
}

// looksReal puts the value in a tree of its own and asks the scan, which is the same
// path CI takes.
func looksReal(t *testing.T, value string) bool {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "candidate.txt")

	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatalf("writing the candidate: %v", err)
	}

	findings, err := credentialguard.Scan(dir)
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}

	return len(findings) > 0
}
