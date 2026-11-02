// Package envelope is the shape a forum message travels in, and the file name that
// says which shape a message carries. It depends on nothing in this tree, so both
// the archive and the forum can read it.
package envelope

import "strings"

const (
	// FilePrefix marks the envelope a message carries.
	FilePrefix = "forum-"
	// FileSuffix is the current shape: a markdown frontmatter block.
	FileSuffix = ".md"
	// LegacySuffix is what the envelope travelled in up to v0.3.2: still read,
	// never written.
	LegacySuffix = ".json"

	// MaxBodyBytes is the largest body an envelope may carry, before framing.
	MaxBodyBytes = 256 * 1024
	// MaxFileBytes is where a `forum-*` stops being one of ours: the schema refuses
	// the body long before, so a bigger one is something a person attached.
	MaxFileBytes = MaxBodyBytes + 64*1024
)

// FileName is what the envelope file is called: the envelope id, so two messages
// never share a name.
func FileName(id string) string {
	return FilePrefix + id + FileSuffix
}

// IsFileName reports whether an attachment name is an envelope.
func IsFileName(name string) bool {
	if !strings.HasPrefix(name, FilePrefix) {
		return false
	}

	suffix := ""

	switch {
	case strings.HasSuffix(name, FileSuffix):
		suffix = FileSuffix
	case strings.HasSuffix(name, LegacySuffix):
		suffix = LegacySuffix
	default:
		return false
	}

	return len(name) > len(FilePrefix)+len(suffix)
}

// CarriedFile is the attachment on a message that carries an envelope, if there is
// one. Size is part of the test because a huge `forum-*` is somebody's screenshot.
type CarriedFile struct {
	Name string
	Size int64
}

// FileOf answers the envelope attachment of a message.
func FileOf(files []CarriedFile) (CarriedFile, bool) {
	for _, file := range files {
		if IsFileName(file.Name) && file.Size <= MaxFileBytes {
			return file, true
		}
	}

	return CarriedFile{}, false
}
