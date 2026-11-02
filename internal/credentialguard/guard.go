// Package credentialguard keeps a real Slack credential out of the repository.
//
// The store this tool writes lives in the user's own config directory, never here,
// but a debugging export, a copied fixture or a pasted error message is enough to
// commit one, and a token in a public repository is a token that has to be revoked.
// The patterns below are the shapes a *real* credential has; every placeholder this
// repository uses (`xoxc-test-token`, `xoxd-abcdef%2Bghij`, `xoxc-1234567890-abcdef`)
// stays well short of them, so the check has no exceptions to maintain.
package credentialguard

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Finding is one file that carries something shaped like a real credential.
type Finding struct {
	// File is relative to the scanned root.
	File string
	// Kind names what was found, never the value itself: printing it here would put
	// the secret in the build log, which is where it was going anyway.
	Kind string
}

var patterns = []struct {
	kind    string
	pattern *regexp.Regexp
}{
	// A real xoxc token is three long dash-separated groups and a hex tail.
	{kind: "slack session token", pattern: regexp.MustCompile(`xox[acdpsr]-(\d{9,}-){2,}[A-Za-z0-9]{16,}`)},
	// A real `d` cookie is a long percent-encoded blob.
	{kind: "slack session cookie", pattern: regexp.MustCompile(`xoxd-[A-Za-z0-9%+/=_-]{60,}`)},
}

// skipped are the directories that never hold something a person wrote: build output,
// dependencies, and the repository's own history.
var skipped = map[string]bool{
	".git":         true,
	"node_modules": true,
	"dist":         true,
	"bin":          true,
	"vendor":       true,
}

// maxFileBytes bounds the scan: a credential is never in the middle of a gigabyte.
const maxFileBytes = 4 << 20

// Scan walks a tree and answers what it found.
func Scan(root string) ([]Finding, error) {
	findings := make([]Finding, 0, 4)

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			if skipped[entry.Name()] {
				return fs.SkipDir
			}

			return nil
		}

		info, err := entry.Info()
		if err != nil || info.Size() > maxFileBytes {
			return nil //nolint:nilerr // a file that cannot be described is not one this scan can judge
		}

		raw, err := os.ReadFile(path) //nolint:gosec // a walk of the tree being audited
		if err != nil {
			return nil //nolint:nilerr // an unreadable file is skipped, not a finding
		}

		text := string(raw)
		if !strings.Contains(text, "xox") {
			return nil
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			relative = path
		}

		for _, candidate := range patterns {
			if candidate.pattern.MatchString(text) {
				findings = append(findings, Finding{File: relative, Kind: candidate.kind})
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return findings, nil
}
