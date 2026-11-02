//go:build linux

package main

func platformCandidates() []string {
	return []string{
		"/opt/google/chrome/chrome",
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/snap/bin/chromium",
	}
}
