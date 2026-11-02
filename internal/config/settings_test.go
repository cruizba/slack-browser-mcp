package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

func settingsFileIn(t *testing.T) (config.Store, string) {
	t.Helper()

	file := filepath.Join(t.TempDir(), testFile)

	return *config.NewStore(file), file
}

func readFile(t *testing.T, file string) string {
	t.Helper()

	raw, err := os.ReadFile(file) //nolint:gosec // a path this test just created
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}

	return string(raw)
}

func writeFile(t *testing.T, file, content string) {
	t.Helper()

	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", file, err)
	}
}

func invalidError(err error) (*config.InvalidError, bool) {
	return errors.AsType[*config.InvalidError](err)
}

func TestStore_Read(t *testing.T) {
	t.Parallel()

	t.Run("a missing file is empty settings", func(t *testing.T) {
		t.Parallel()

		store, _ := settingsFileIn(t)

		settings, err := store.Read()
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}

		if settings.DefaultWorkspace != nil || settings.StaleAfterMinutes != nil {
			t.Fatalf("Read() = %+v, want empty", settings)
		}
	})

	t.Run("an empty file is empty settings", func(t *testing.T) {
		t.Parallel()

		store, file := settingsFileIn(t)
		writeFile(t, file, "  \n")

		if _, err := store.Read(); err != nil {
			t.Fatalf("Read() error = %v", err)
		}
	})
}

func TestStore_Read_Rejects(t *testing.T) {
	t.Parallel()

	t.Run("broken JSON names the file", func(t *testing.T) {
		t.Parallel()

		store, file := settingsFileIn(t)
		writeFile(t, file, `{"default_workspace": `)

		invalid, ok := invalidError(mustFail(t, store.Read))
		if !ok {
			t.Fatalf("Read() error is not an InvalidError")
		}

		if !strings.Contains(invalid.Error(), "not valid JSON") || !strings.Contains(invalid.Error(), testFile) {
			t.Fatalf("message %q, want it to name the file and the problem", invalid.Error())
		}
	})

	t.Run("unknown keys instead of ignoring a typo", func(t *testing.T) {
		t.Parallel()

		store, file := settingsFileIn(t)
		writeFile(t, file, `{"default_workspace": "x", "defaultWorkspace": "y"}`)

		if _, ok := invalidError(mustFail(t, store.Read)); !ok {
			t.Fatalf("Read() accepted an unknown key")
		}
	})

	t.Run("an empty nickname explains how to remove it", func(t *testing.T) {
		t.Parallel()

		store, file := settingsFileIn(t)
		writeFile(t, file, `{"assisted_nickname": "  "}`)

		err := mustFail(t, store.Read)
		if !strings.Contains(err.Error(), "empty; remove the key") {
			t.Fatalf("error = %v, want it to say how to remove the key", err)
		}
	})

	t.Run("a threshold that would refresh on every read", func(t *testing.T) {
		t.Parallel()

		store, file := settingsFileIn(t)
		writeFile(t, file, `{"stale_after_minutes": 0}`)

		err := mustFail(t, store.Read)
		if !strings.Contains(err.Error(), "stale_after_minutes") {
			t.Fatalf("error = %v, want it to name the field", err)
		}
	})
}

func TestStore_Write(t *testing.T) {
	t.Parallel()

	store, file := settingsFileIn(t)

	if _, err := store.SetDefaultWorkspace(new("T01ACME")); err != nil {
		t.Fatalf("SetDefaultWorkspace: %v", err)
	}

	// Windows has no mode bits, so the contract there is "nobody else can reach
	// it" rather than a number; internal/platform says why.
	private, mode, err := platform.Private(file)
	if err != nil {
		t.Fatalf("checking permissions: %v", err)
	}

	if !private {
		t.Fatalf("the settings file is reachable by other users (mode %#o)", mode)
	}

	if runtime.GOOS != "windows" && mode != 0o600 {
		t.Fatalf("mode = %#o, want 0600", mode)
	}

	if raw := readFile(t, file); !strings.Contains(raw, `"default_workspace": "T01ACME"`) {
		t.Fatalf("file = %q, want the default workspace", raw)
	}

	settings, err := store.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if settings.DefaultWorkspace == nil || *settings.DefaultWorkspace != "T01ACME" {
		t.Fatalf("Read() = %+v, want T01ACME", settings)
	}
}

func TestStore_Write_ClearingRemovesTheKey(t *testing.T) {
	t.Parallel()

	store, file := settingsFileIn(t)

	if _, err := store.SetDefaultWorkspace(new("T01ACME")); err != nil {
		t.Fatalf("setting: %v", err)
	}

	if _, err := store.SetDefaultWorkspace(nil); err != nil {
		t.Fatalf("clearing: %v", err)
	}

	if got := strings.TrimSpace(readFile(t, file)); got != "{}" {
		t.Fatalf("file = %q, want {}", got)
	}
}

func TestStore_Write_Thresholds(t *testing.T) {
	t.Parallel()

	t.Run("keeps them next to the workspace", func(t *testing.T) {
		t.Parallel()

		store, _ := settingsFileIn(t)

		want := config.Settings{
			DefaultWorkspace:   new("T01ACME"),
			StaleAfterMinutes:  new(1440.0),
			RecentAfterMinutes: new(30.0),
		}

		if err := store.Write(want); err != nil {
			t.Fatalf("Write: %v", err)
		}

		got, err := store.Read()
		if err != nil {
			t.Fatalf("Read: %v", err)
		}

		if *got.StaleAfterMinutes != 1440 || *got.RecentAfterMinutes != 30 {
			t.Fatalf("thresholds = %v/%v, want 1440/30", *got.StaleAfterMinutes, *got.RecentAfterMinutes)
		}
	})

	t.Run("refuses one that is not positive", func(t *testing.T) {
		t.Parallel()

		store, _ := settingsFileIn(t)

		for _, settings := range []config.Settings{
			{StaleAfterMinutes: new(0.0)},
			{RecentAfterMinutes: new(-1.0)},
		} {
			if err := store.Write(settings); err == nil {
				t.Fatalf("Write(%+v) accepted a non-positive threshold", settings)
			}
		}
	})
}

// The settings file is meant to be diffable and hand-editable, so its exact
// encoding is part of the contract: field order, two-space indent, no escaping,
// and a trailing newline.
func TestStore_Write_IsStable(t *testing.T) {
	t.Parallel()

	store, file := settingsFileIn(t)

	settings := config.Settings{
		DefaultWorkspace:   new("T01ACME"),
		StaleAfterMinutes:  new(90.0),
		RecentAfterMinutes: new(2.5),
	}

	if err := store.Write(settings); err != nil {
		t.Fatalf("Write: %v", err)
	}

	want := "{\n" +
		`  "default_workspace": "T01ACME",` + "\n" +
		`  "stale_after_minutes": 90,` + "\n" +
		`  "recent_after_minutes": 2.5` + "\n" +
		"}\n"

	if got := readFile(t, file); got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestStore_SetAssistedNickname(t *testing.T) {
	t.Parallel()

	t.Run("stores the nickname next to the other preferences", func(t *testing.T) {
		t.Parallel()

		store, _ := settingsFileIn(t)

		if _, err := store.SetDefaultWorkspace(new("T01ACME")); err != nil {
			t.Fatalf("setting the default: %v", err)
		}

		settings, err := store.SetAssistedNickname(new(testNickname))
		if err != nil {
			t.Fatalf("SetAssistedNickname: %v", err)
		}

		if settings.AssistedNickname == nil || *settings.AssistedNickname != testNickname {
			t.Fatalf("nickname = %v, want %s", settings.AssistedNickname, testNickname)
		}

		if settings.DefaultWorkspace == nil || *settings.DefaultWorkspace != "T01ACME" {
			t.Fatalf("the default workspace was lost: %+v", settings)
		}
	})

	t.Run("clearing removes the key", func(t *testing.T) {
		t.Parallel()

		store, _ := settingsFileIn(t)

		if _, err := store.SetAssistedNickname(new(testNickname)); err != nil {
			t.Fatalf("setting: %v", err)
		}

		if _, err := store.SetAssistedNickname(nil); err != nil {
			t.Fatalf("clearing: %v", err)
		}

		settings, err := store.Read()
		if err != nil {
			t.Fatalf("Read: %v", err)
		}

		if settings.AssistedNickname != nil {
			t.Fatalf("nickname = %v, want nil", settings.AssistedNickname)
		}
	})
}

func TestStore_SetAssistedNickname_Rejects(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"asterisk":   "a*b",
		"blank":      "  ",
		"too long":   strings.Repeat("x", 61),
		"line break": "a\nb",
		"carriage":   "a\rb",
	}

	for name, nickname := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store, _ := settingsFileIn(t)

			if _, err := store.SetAssistedNickname(&nickname); err == nil {
				t.Fatalf("SetAssistedNickname(%q) was accepted", nickname)
			}
		})
	}
}

func TestEnsureDefaultWorkspace(t *testing.T) {
	t.Parallel()

	t.Run("fills an empty default and says so", func(t *testing.T) {
		t.Parallel()

		store, _ := settingsFileIn(t)

		filled, err := config.EnsureDefaultWorkspace(&store, "T01ACME")
		if err != nil {
			t.Fatalf("EnsureDefaultWorkspace: %v", err)
		}

		if !filled {
			t.Fatal("filled = false, want true")
		}
	})

	t.Run("never overrides a default the user chose", func(t *testing.T) {
		t.Parallel()

		store, _ := settingsFileIn(t)

		if _, err := store.SetDefaultWorkspace(new("T0CHOSEN")); err != nil {
			t.Fatalf("setting the default: %v", err)
		}

		filled, err := config.EnsureDefaultWorkspace(&store, "T01ACME")
		if err != nil {
			t.Fatalf("EnsureDefaultWorkspace: %v", err)
		}

		if filled {
			t.Fatal("filled = true, want false")
		}

		settings, err := store.Read()
		if err != nil {
			t.Fatalf("Read: %v", err)
		}

		if *settings.DefaultWorkspace != "T0CHOSEN" {
			t.Fatalf("default = %q, want T0CHOSEN", *settings.DefaultWorkspace)
		}
	})
}

func mustFail(t *testing.T, read func() (config.Settings, error)) error {
	t.Helper()

	_, err := read()
	if err == nil {
		t.Fatal("the call was expected to fail")
	}

	return err
}

// The reference a person types is stored and quoted back, so the two things worth
// pinning are that the value returned is the value written (a caller that trusts the
// return must not get zeroes back) and that the length limit lands on 128 rather than
// on 127 or 129.
func TestSetDefaultWorkspaceReturnsWhatItStored(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := config.NewStore(filepath.Join(dir, "config.json"))
	reference := strings.Repeat("w", 128)

	settings, err := store.SetDefaultWorkspace(&reference)
	if err != nil {
		t.Fatalf("SetDefaultWorkspace: %v", err)
	}

	if settings.DefaultWorkspace == nil || *settings.DefaultWorkspace != reference {
		t.Fatalf("returned = %+v, want the reference it stored", settings.DefaultWorkspace)
	}

	// A reference one over the limit is refused and nothing is written, because a
	// half-validated file is worse than no change at all.
	tooLong := strings.Repeat("w", 129)

	if _, err := store.SetDefaultWorkspace(&tooLong); err == nil ||
		!strings.Contains(err.Error(), "must be between 1 and 128 characters") {
		t.Fatalf("SetDefaultWorkspace(129) = %v, want the length rule", err)
	}

	read, err := store.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if read.DefaultWorkspace == nil || *read.DefaultWorkspace != reference {
		t.Fatalf("the refused write changed the file: %+v", read.DefaultWorkspace)
	}
}
