//go:build darwin

package chrome

import (
	"os"
	"path/filepath"
)

const appSuffix = ".app/Contents/MacOS/"

func platformCandidates() []string {
	names := []struct{ app, binary string }{
		{"Google Chrome", "Google Chrome"},
		{"Google Chrome Beta", "Google Chrome Beta"},
		{"Google Chrome Canary", "Google Chrome Canary"},
		{"Chromium", "Chromium"},
	}
	roots := []string{"/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, "Applications"))
	}
	var candidates []string
	for _, root := range roots {
		for _, name := range names {
			candidates = append(candidates, filepath.Join(root, name.app+appSuffix+name.binary))
		}
	}
	return candidates
}
