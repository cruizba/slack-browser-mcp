package platform_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

func envFor(goos, home string, values map[string]string) platform.Env {
	return platform.Env{
		Getenv:    func(name string) string { return values[name] },
		LookupEnv: func(name string) (string, bool) { value, ok := values[name]; return value, ok },
		Home:      home,
		OS:        goos,
	}
}

func TestConfigHome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  platform.Env
		want string
	}{
		{
			name: "linux uses the dotfile layout",
			env:  envFor("linux", "/home/ana", nil),
			want: "/home/ana/.config",
		},
		{
			name: "macos uses Application Support",
			env:  envFor("darwin", "/home/ana", nil),
			want: "/home/ana/Library/Application Support",
		},
		{
			name: "windows uses APPDATA",
			env:  envFor("windows", `C:\Users\ana`, map[string]string{"APPDATA": `C:\Users\ana\AppData\Roaming`}),
			want: "C:/Users/ana/AppData/Roaming",
		},
		{
			name: "windows without APPDATA still has an answer",
			env:  envFor("windows", `C:\Users\ana`, nil),
			want: "C:/Users/ana/AppData/Roaming",
		},
		{
			name: "XDG wins on every system, so a dotfiles setup keeps working",
			env:  envFor("darwin", "/home/ana", map[string]string{"XDG_CONFIG_HOME": "/home/ana/dotfiles/config"}),
			want: "/home/ana/dotfiles/config",
		},
		{
			name: "an empty override is not an override",
			env:  envFor("linux", "/home/ana", map[string]string{"XDG_CONFIG_HOME": "  "}),
			want: "/home/ana/.config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := slash(platform.ConfigHome(tt.env)); got != tt.want {
				t.Fatalf("ConfigHome() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDataHome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  platform.Env
		want string
	}{
		{
			name: "linux uses the local share directory",
			env:  envFor("linux", "/home/ana", nil),
			want: "/home/ana/.local/share",
		},
		{
			name: "macos uses Application Support too",
			env:  envFor("darwin", "/home/ana", nil),
			want: "/home/ana/Library/Application Support",
		},
		{
			name: "windows uses LOCALAPPDATA",
			env: envFor("windows", `C:\Users\ana`, map[string]string{
				"LOCALAPPDATA": `C:\Users\ana\AppData\Local`,
			}),
			want: "C:/Users/ana/AppData/Local",
		},
		{
			name: "XDG wins",
			env:  envFor("windows", `C:\Users\ana`, map[string]string{"XDG_DATA_HOME": `C:\data`}),
			want: "C:/data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := slash(platform.DataHome(tt.env)); got != tt.want {
				t.Fatalf("DataHome() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLookup(t *testing.T) {
	t.Parallel()

	t.Run("distinguishes set-to-empty from unset", func(t *testing.T) {
		t.Parallel()

		env := envFor("linux", "/home/ana", map[string]string{"SET_EMPTY": ""})

		if value, present := platform.Lookup(env, "SET_EMPTY"); !present || value != "" {
			t.Fatalf("Lookup(set-empty) = %q, %v, want \"\", true", value, present)
		}

		if _, present := platform.Lookup(env, "UNSET"); present {
			t.Fatal("Lookup(unset) reported present")
		}
	})

	t.Run("without LookupEnv an empty value reads as unset", func(t *testing.T) {
		t.Parallel()

		env := platform.Env{Getenv: func(string) string { return "" }, Home: "/home/ana", OS: "linux"}

		if _, present := platform.Lookup(env, "SET_EMPTY"); present {
			t.Fatal("Lookup reported present without a way to know")
		}
	})
}

// slash normalises both path flavours, because filepath.ToSlash only converts the
// separator of the system the test is running on: a Windows layout resolved on
// Linux keeps its backslashes.
func slash(path string) string {
	return strings.ReplaceAll(path, `\`, "/")
}

// An Env built by hand has no obligations: a caller that only needs Home should not
// have to supply a lookup function. `Lookup` is the door that tolerates that, which is
// why production code reads the environment through it and not through the field.
func TestEnvWithNoLookup(t *testing.T) {
	t.Parallel()

	bare := platform.Env{Home: t.TempDir()}

	if value, ok := platform.Lookup(bare, "SLACK_BROWSER_API_BASE"); ok || value != "" {
		t.Fatalf("Lookup on a bare Env = %q, %v", value, ok)
	}

	if got := platform.ConfigHome(bare); got == "" {
		t.Fatal("ConfigHome on a bare Env = empty")
	}
}

func TestWindowsHomes(t *testing.T) {
	t.Parallel()

	home := t.TempDir()

	dirs := map[string]string{
		"APPDATA":      `C:\Users\ana\AppData\Roaming`,
		"LOCALAPPDATA": `C:\Users\ana\AppData\Local`,
	}

	configured := platform.Env{Home: home, OS: "windows", Getenv: func(name string) string {
		return dirs[name]
	}}

	// The two roots are different directories on purpose: the configuration is small
	// and precious, the data is large and rebuildable, and Windows separates them.
	if got := platform.ConfigHome(configured); got != `C:\Users\ana\AppData\Roaming` {
		t.Fatalf("ConfigHome = %q", got)
	}

	if got := platform.DataHome(configured); got != `C:\Users\ana\AppData\Local` {
		t.Fatalf("DataHome = %q", got)
	}

	// Without either variable the paths are built from the profile rather than
	// collapsing to something relative that would land in the working tree.
	bare := platform.Env{Home: home, OS: "windows"}

	if got := platform.ConfigHome(bare); got != filepath.Join(home, "AppData", "Roaming") {
		t.Fatalf("ConfigHome without APPDATA = %q", got)
	}

	if got := platform.DataHome(bare); got != filepath.Join(home, "AppData", "Local") {
		t.Fatalf("DataHome without LOCALAPPDATA = %q", got)
	}

	// XDG wins on every platform, including this one: it is how a user moves both
	// roots somewhere else without asking the system.
	xdgDirs := map[string]string{"XDG_CONFIG_HOME": "/xdg/config", "XDG_DATA_HOME": "/xdg/data"}

	xdg := platform.Env{Home: home, OS: "windows", Getenv: func(name string) string {
		return xdgDirs[name]
	}}

	if got := platform.ConfigHome(xdg); got != "/xdg/config" {
		t.Fatalf("ConfigHome with XDG = %q", got)
	}

	if got := platform.DataHome(xdg); got != "/xdg/data" {
		t.Fatalf("DataHome with XDG = %q", got)
	}
}
