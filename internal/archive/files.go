package archive

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/cruizba/slack-browser-mcp/internal/platform"
	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

// maxNameChars bounds the part of a file name that comes from the message.
const maxNameChars = 80

// outerNamePattern is what survives of a name taken from a message: printable
// characters, no separators, nothing that could climb out of the directory.
var outerNamePattern = regexp.MustCompile(`[^\w.\- ]+`)

// spaceRun collapses whitespace in a file name.
var spaceRun = regexp.MustCompile(`\s+`)

// leadingDots strips the dots and spaces that would make a hidden or relative name.
var leadingDots = regexp.MustCompile(`^[.\s]+`)

// StoredFile is an attachment the archive holds.
type StoredFile struct {
	Path  string
	Name  string
	Bytes int64
}

// SafeFileName reduces a name taken from a message to a flat, printable basename:
// `../../etc/passwd` must not escape the team directory.
func SafeFileName(file slack.File, fileID string) string {
	raw := file.Name
	if raw == "" {
		raw = file.Title
	}

	if raw == "" {
		raw = file.Mimetype
	}

	if raw == "" {
		raw = "file"
	}

	cleaned := norm.NFKD.String(raw)
	cleaned = outerNamePattern.ReplaceAllString(cleaned, "_")
	cleaned = spaceRun.ReplaceAllString(cleaned, " ")
	cleaned = leadingDots.ReplaceAllString(cleaned, "")
	cleaned = strings.TrimSpace(cleaned)

	if len([]rune(cleaned)) > maxNameChars {
		cleaned = string([]rune(cleaned)[:maxNameChars])
	}

	if cleaned == "" {
		cleaned = "file"
	}

	return fileID + "-" + cleaned
}

// dropOlderCopies forgets the names this file had before somebody renamed it in
// Slack: the id stays, so the older files would otherwise sit beside the new one
// with nothing saying which is current.
func dropOlderCopies(dir, current, fileID string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		name := entry.Name()

		if name == current || strings.HasSuffix(name, ".tmp") {
			continue
		}

		if strings.HasPrefix(name, fileID+"-") {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// FileStore keeps attachment bytes next to the JSONL archive: one directory per
// team, one file per Slack file id.
type FileStore struct {
	Root string
}

// NewFileStore points at the archive root the bytes live under.
func NewFileStore(root string) *FileStore {
	return &FileStore{Root: root}
}

func (s *FileStore) dir(teamID string) (string, error) {
	if !idPattern.MatchString(teamID) {
		return "", errors.New("Invalid team id: " + teamID)
	}

	return filepath.Join(s.Root, teamID, "files"), nil
}

// Find answers the copy of a file the archive already has. A copy left behind
// under an older name is still the same file id, and the newest one is the one
// Slack last described.
func (s *FileStore) Find(teamID, fileID string) (*StoredFile, error) {
	dir, err := s.dir(teamID)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, err
	}

	var newest *StoredFile

	var newestAt time.Time

	for _, entry := range entries {
		name := entry.Name()

		if !strings.HasPrefix(name, fileID+"-") || strings.HasSuffix(name, ".tmp") || entry.IsDir() {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if newest == nil || info.ModTime().After(newestAt) {
			newestAt = info.ModTime()
			newest = &StoredFile{
				Path:  filepath.Join(dir, name),
				Name:  strings.TrimPrefix(name, fileID+"-"),
				Bytes: info.Size(),
			}
		}
	}

	return newest, nil
}

// Save writes the bytes of an attachment, replacing any older copy filed under a
// name the file had before somebody renamed it in Slack.
func (s *FileStore) Save(teamID string, file slack.File, bytes []byte) (*StoredFile, error) {
	fileID := file.ID

	if fileID == "" {
		// The last path segment of the private URL names the file, which is the only
		// id an older message might have led with.
		if at := strings.LastIndex(file.URLPrivate, "/"); at >= 0 {
			fileID = file.URLPrivate[at+1:]
		}

		if at := strings.Index(fileID, "?"); at >= 0 {
			fileID = fileID[:at]
		}
	}

	if fileID == "" {
		return nil, errors.New("Cannot store a file without an id")
	}

	dir, err := s.dir(teamID)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	name := SafeFileName(file, fileID)
	path := filepath.Join(dir, filepath.Base(name))
	temporary := path + ".tmp"

	if err := os.WriteFile(temporary, bytes, 0o600); err != nil {
		return nil, err
	}

	if err := os.Rename(temporary, path); err != nil {
		return nil, err
	}

	if err := platform.Harden(path); err != nil {
		return nil, err
	}

	dropOlderCopies(dir, filepath.Base(name), fileID)

	return &StoredFile{Path: path, Name: name, Bytes: int64(len(bytes))}, nil
}
