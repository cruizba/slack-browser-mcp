package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Settings is the file the user may edit by hand. Absent fields are nil rather
// than zero, because "not configured" and "configured to zero" are different
// answers and only the second one is an error.
type Settings struct {
	DefaultWorkspace   *string  `json:"default_workspace,omitempty"`
	StaleAfterMinutes  *float64 `json:"stale_after_minutes,omitempty"`
	RecentAfterMinutes *float64 `json:"recent_after_minutes,omitempty"`
	AssistedNickname   *string  `json:"assisted_nickname,omitempty"`
}

// InvalidError is a problem the user has to fix by hand. Its text is shown
// verbatim, so it is a sentence rather than a wrapped cause.
type InvalidError struct {
	Message string
}

func (e *InvalidError) Error() string {
	return e.Message
}

const (
	maxWorkspaceRefChars = 128
	settingsIndent       = "  "
)

// Store reads and writes the settings file.
type Store struct {
	file string
}

// NewStore points at the settings file. The path comes from [Paths] so there is
// one answer to where it lives.
func NewStore(file string) *Store {
	return &Store{file: file}
}

// Read returns the stored settings. A missing or empty file is not an error: it
// means nothing has been configured yet.
func (s *Store) Read() (Settings, error) {
	raw, err := os.ReadFile(s.file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Settings{}, nil
		}

		return Settings{}, fmt.Errorf("reading %s: %w", s.file, err)
	}

	if strings.TrimSpace(string(raw)) == "" {
		return Settings{}, nil
	}

	var settings Settings

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&settings); err != nil {
		return Settings{}, &InvalidError{Message: s.file + " is not valid JSON. Fix it or delete it."}
	}

	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Settings{}, &InvalidError{Message: s.file + " is not valid JSON. Fix it or delete it."}
	}

	if problem := validate(settings); problem != "" {
		return Settings{}, &InvalidError{
			Message: fmt.Sprintf("%s is not a valid settings file (%s).", s.file, problem),
		}
	}

	return settings, nil
}

// Write replaces the file atomically at 0600: a half-written settings file would
// lose the default workspace that every other command reads.
func (s *Store) Write(settings Settings) error {
	if problem := validate(settings); problem != "" {
		return &InvalidError{Message: fmt.Sprintf("%s is not a valid settings file (%s).", s.file, problem)}
	}

	encoded, err := json.MarshalIndent(settings, "", settingsIndent)
	if err != nil {
		return fmt.Errorf("encoding settings: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.file), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(s.file), err)
	}

	temporary := fmt.Sprintf("%s.%d.tmp", s.file, os.Getpid())

	handle, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // the path is the settings file, not request data
	if err != nil {
		return fmt.Errorf("opening %s: %w", temporary, err)
	}

	if _, err := handle.Write(append(encoded, '\n')); err != nil {
		_ = handle.Close()

		return fmt.Errorf("writing %s: %w", temporary, err)
	}

	if err := handle.Sync(); err != nil {
		_ = handle.Close()

		return fmt.Errorf("syncing %s: %w", temporary, err)
	}

	if err := handle.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", temporary, err)
	}

	if err := os.Chmod(temporary, 0o600); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", temporary, err)
	}

	if err := os.Rename(temporary, s.file); err != nil {
		return fmt.Errorf("replacing %s: %w", s.file, err)
	}

	return nil
}

// SetDefaultWorkspace stores a workspace reference, or removes the default when
// given nil.
func (s *Store) SetDefaultWorkspace(reference *string) (Settings, error) {
	settings, err := s.Read()
	if err != nil {
		return Settings{}, err
	}

	settings.DefaultWorkspace = reference

	if err := s.Write(settings); err != nil {
		return Settings{}, err
	}

	return settings, nil
}

// SetAssistedNickname stores the nickname that signs messages, or removes it when
// given nil so the generated default applies again.
func (s *Store) SetAssistedNickname(nickname *string) (Settings, error) {
	settings, err := s.Read()
	if err != nil {
		return Settings{}, err
	}

	if nickname == nil {
		settings.AssistedNickname = nil
	} else {
		// Checked here rather than in Write so the caller gets a readable reason
		// instead of the field-level "not valid settings file" wrapper.
		checked, err := CheckedNickname(*nickname)
		if err != nil {
			return Settings{}, err
		}

		settings.AssistedNickname = &checked
	}

	if err := s.Write(settings); err != nil {
		return Settings{}, err
	}

	return settings, nil
}

// EnsureDefaultWorkspace fills an unset default with the workspace just stored,
// and reports whether it did. A default the user chose is never overridden.
func EnsureDefaultWorkspace(store *Store, teamID string) (bool, error) {
	settings, err := store.Read()
	if err != nil {
		return false, err
	}

	if settings.DefaultWorkspace != nil && *settings.DefaultWorkspace != "" {
		return false, nil
	}

	reference := teamID

	if _, err := store.SetDefaultWorkspace(&reference); err != nil {
		return false, err
	}

	return true, nil
}

func validate(settings Settings) string {
	if settings.DefaultWorkspace != nil {
		reference := strings.TrimSpace(*settings.DefaultWorkspace)
		if reference == "" || len(reference) > maxWorkspaceRefChars {
			return "default_workspace: must be between 1 and 128 characters"
		}
	}

	for _, field := range []struct {
		name  string
		value *float64
	}{
		{name: "stale_after_minutes", value: settings.StaleAfterMinutes},
		{name: "recent_after_minutes", value: settings.RecentAfterMinutes},
	} {
		if field.value != nil && *field.value <= 0 {
			return field.name + ": must be greater than 0"
		}
	}

	if settings.AssistedNickname != nil {
		value := strings.TrimSpace(*settings.AssistedNickname)
		if value == "" {
			return "assisted_nickname: empty; remove the key to post without a header"
		}

		if problem := nicknameProblem(value); problem != "" {
			return "assisted_nickname: " + problem
		}
	}

	return ""
}
