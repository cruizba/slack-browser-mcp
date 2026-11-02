// Package redact keeps Slack secrets out of logs, tool results and error
// messages. Anything that leaves this process goes through it.
package redact

import "regexp"

var patterns = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(^|[?&\s])token=[^&\s"']+`), "${1}token=<redacted>"},
	{regexp.MustCompile(`xox[a-z]-[A-Za-z0-9%_.-]{3,}`), "xox?-<redacted>"},
	{regexp.MustCompile(`(Bearer\s+)[A-Za-z0-9%_.-]{8,}`), "${1}<redacted>"},
}

// Text strips the token that may be in a URL, an xoxc session token and a bearer
// header, which are the three shapes a secret leaves Slack in.
func Text(value string) string {
	for _, entry := range patterns {
		value = entry.pattern.ReplaceAllString(value, entry.replacement)
	}

	return value
}
