package slack

import (
	"regexp"
	"strings"
)

var (
	printedID   = regexp.MustCompile(`\s*\(([CGD][A-Z0-9]{5,})\)$`)
	memberCount = regexp.MustCompile(`(?i)\s+\d+\s+members?$`)
)

// PrintedReference is what a label carries once its decoration is removed: the
// id it printed, if any, and the rest as the person typed it.
type PrintedReference struct {
	ID        string
	Reference string
}

// ParsePrintedReference splits the id out of a printed label, member count
// included, so `dm:Ana Ruiz (D01ANA) 2 members` reads as the id `D01ANA`.
func ParsePrintedReference(value string) PrintedReference {
	reference := strings.TrimSpace(value)

	if at := memberCount.FindStringIndex(reference); at != nil {
		reference = strings.TrimRight(reference[:at[0]], " \t\n")
	}

	if at := printedID.FindStringSubmatchIndex(reference); at != nil {
		id := reference[at[2]:at[3]]
		rest := strings.TrimRight(reference[:at[0]], " \t\n")

		return PrintedReference{ID: id, Reference: rest}
	}

	return PrintedReference{Reference: reference}
}
