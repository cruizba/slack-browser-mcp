//go:build windows

package chrome

import (
	"os"
	"path/filepath"
)

func platformCandidates() []string {
	var candidates []string
	for _, root := range []string{os.Getenv("PROGRAMFILES"), os.Getenv("PROGRAMFILES(X86)"), os.Getenv("LOCALAPPDATA")} {
		if root == "" {
			continue
		}
		candidates = append(candidates,
			filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(root, "Google", "Chrome Beta", "Application", "chrome.exe"),
			filepath.Join(root, "Google", "Chrome SxS", "Application", "chrome.exe"),
			filepath.Join(root, "Chromium", "Application", "chrome.exe"),
		)
	}
	return candidates
}
