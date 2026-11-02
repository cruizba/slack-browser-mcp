package platform_test

import (
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
