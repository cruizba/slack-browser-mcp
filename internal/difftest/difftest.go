// Package difftest compares what two builds of the same command produce.
//
// The TypeScript build is the oracle while the Go port is in progress: a golden
// file records what the oracle said, and the port has to reproduce it. What
// cannot match between two runs of the same command — timestamps, the port the
// fake Slack happened to take, the directory the test used — is rewritten by
// [Normalizer] before the comparison, and nothing else is.
package difftest

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ErrBadGolden means a golden file is not in the shape [Result.Render] writes.
var ErrBadGolden = errors.New("golden is not in the expected shape")

const (
	stdoutMarker = "# stdout"
	stderrMarker = "# stderr"
)

// Result is everything a command produced that a user could see.
type Result struct {
	Stdout string
	Stderr string
	Exit   int
}

// Render writes the golden form. Each stream carries its byte length rather
// than a closing marker, so an output that does not end in a newline, or that
// ends in three, is still stored and compared exactly.
func (r Result) Render() string {
	var b strings.Builder

	fmt.Fprintf(&b, "# exit: %d\n", r.Exit)
	fmt.Fprintf(&b, "%s %d\n", stdoutMarker, len(r.Stdout))
	b.WriteString(r.Stdout)
	b.WriteString("\n")
	fmt.Fprintf(&b, "%s %d\n", stderrMarker, len(r.Stderr))
	b.WriteString(r.Stderr)
	b.WriteString("\n")

	return b.String()
}

// Parse reads back what Render wrote.
func Parse(data []byte) (Result, error) {
	var result Result

	line, rest, ok := strings.Cut(string(data), "\n")
	if !ok {
		return result, fmt.Errorf("reading exit line: %w", ErrBadGolden)
	}

	exit, err := strconv.Atoi(strings.TrimPrefix(line, "# exit: "))
	if err != nil || !strings.HasPrefix(line, "# exit: ") {
		return result, fmt.Errorf("reading exit code from %q: %w", line, ErrBadGolden)
	}

	result.Exit = exit

	if result.Stdout, rest, err = readBlock(rest, stdoutMarker); err != nil {
		return result, err
	}

	if result.Stderr, _, err = readBlock(rest, stderrMarker); err != nil {
		return result, err
	}

	return result, nil
}

func readBlock(rest, marker string) (string, string, error) {
	header, after, ok := strings.Cut(rest, "\n")
	if !ok || !strings.HasPrefix(header, marker+" ") {
		return "", "", fmt.Errorf("reading %q header from %q: %w", marker, header, ErrBadGolden)
	}

	size, err := strconv.Atoi(strings.TrimPrefix(header, marker+" "))
	if err != nil {
		return "", "", fmt.Errorf("reading %q size from %q: %w", marker, header, ErrBadGolden)
	}

	if len(after) < size {
		return "", "", fmt.Errorf("reading %q body: %w", marker, ErrBadGolden)
	}

	return after[:size], strings.TrimPrefix(after[size:], "\n"), nil
}

// Normalizer rewrites the parts of an output that differ between two runs of the
// same command. Everything it does not match is compared byte for byte, which is
// the point: a normalizer that is too eager hides the bug it exists to find.
type Normalizer struct {
	// Root is the directory this run was given, and Home the fake home under it.
	Root string
	Home string
	// Addr is the fake Slack's URL, with the port it happened to take.
	Addr string
}

var (
	isoStamp    = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z?`)
	dateTime    = regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}`)
	slackStamp  = regexp.MustCompile(`\b\d{10}\.\d{6}\b`)
	versionText = regexp.MustCompile(`\bv?\d+\.\d+\.\d+(?:-[0-9A-Za-z.\-]+)?\b`)
	// The dirty marker is the state of the working tree at the moment of the run, not
	// the behaviour of the command, and recording it would make the golden depend on
	// whether somebody had an edit open.
	dirtyMarker = regexp.MustCompile(`, local changes`)
	nodeWarning = regexp.MustCompile("\\(node:\\d+\\) [^\\n]*\\n(?:\\(Use `node --trace-warnings \\.\\.\\.` to show where the warning was created\\)\\n)?")
)

// Apply returns the text with the run-specific details replaced.
func (n Normalizer) Apply(text string) string {
	for _, pair := range [][2]string{{n.Root, "<root>"}, {n.Home, "<home>"}, {n.Addr, "<fake>"}} {
		if pair[0] == "" {
			continue
		}

		text = strings.ReplaceAll(text, pair[0], pair[1])
	}

	text = isoStamp.ReplaceAllString(text, "<iso>")
	text = dateTime.ReplaceAllString(text, "<date>")
	text = slackStamp.ReplaceAllString(text, "<ts>")
	text = versionText.ReplaceAllString(text, "<version>")
	text = dirtyMarker.ReplaceAllString(text, "")

	// Node's experimental warning is the runtime talking, not the command, and
	// the Go build will never print it.
	return nodeWarning.ReplaceAllString(text, "")
}

// Diff shows where two results start to disagree, with a little context. A full
// dump of both is unreadable when the difference is one line into a help page
// that is two hundred lines long.
func Diff(want, got string) string {
	if want == got {
		return ""
	}

	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")

	at := 0
	for at < len(wantLines) && at < len(gotLines) && wantLines[at] == gotLines[at] {
		at++
	}

	var b strings.Builder

	fmt.Fprintf(&b, "first difference at line %d\n", at+1)

	for _, line := range wantLines[max(0, at-3):at] {
		fmt.Fprintf(&b, "  %s\n", line)
	}

	if at < len(wantLines) {
		fmt.Fprintf(&b, "- %s\n", wantLines[at])
	} else {
		b.WriteString("- <end of golden>\n")
	}

	if at < len(gotLines) {
		fmt.Fprintf(&b, "+ %s\n", gotLines[at])
	} else {
		b.WriteString("+ <end of output>\n")
	}

	return b.String()
}
