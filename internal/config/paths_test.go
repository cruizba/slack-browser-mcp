package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

func slash(path string) string {
	return strings.ReplaceAll(path, `\`, "/")
}

func TestResolve(t *testing.T) {
	t.Parallel()

	t.Run("linux keeps everything under the dotfile layout", func(t *testing.T) {
		t.Parallel()

		paths := config.Resolve(platform.Env{Home: testHome, OS: "linux"})

		for _, tc := range []struct {
			name string
			got  string
			want string
		}{
			{name: "config dir", got: paths.ConfigDir, want: "/home/ana/.config/slack-browser-mcp"},
			{name: "settings", got: paths.SettingsFile, want: "/home/ana/.config/slack-browser-mcp/config.json"},
			{name: "accounts", got: paths.Accounts, want: "/home/ana/.config/slack-browser-mcp/accounts"},
			{name: "data dir", got: paths.DataDir, want: "/home/ana/.local/share/slack-browser-mcp"},
			{name: "archive", got: paths.ArchiveDir, want: "/home/ana/.local/share/slack-browser-mcp/archive"},
			{name: "index", got: paths.ArchiveIndex, want: "/home/ana/.local/share/slack-browser-mcp/archive.sqlite"},
		} {
			if tc.got != filepath.FromSlash(tc.want) {
				t.Fatalf("%s = %q, want %q", tc.name, tc.got, tc.want)
			}
		}
	})

	t.Run("the config directory override moves the store, not the data", func(t *testing.T) {
		t.Parallel()

		paths := config.Resolve(platform.Env{
			Getenv: func(name string) string {
				if name == "SLACK_BROWSER_MCP_CONFIG_DIR" {
					return "/tmp/store"
				}

				return ""
			},
			Home: testHome,
			OS:   "linux",
		})

		if paths.ConfigDir != "/tmp/store" || paths.Accounts != "/tmp/store/accounts" {
			t.Fatalf("config = %q/%q, want /tmp/store", paths.ConfigDir, paths.Accounts)
		}

		if paths.DataDir != "/home/ana/.local/share/slack-browser-mcp" {
			t.Fatalf("data = %q, want it untouched", paths.DataDir)
		}
	})

	t.Run("the settings file override wins over the directory", func(t *testing.T) {
		t.Parallel()

		paths := config.Resolve(platform.Env{
			Getenv: func(name string) string {
				if name == "SLACK_BROWSER_MCP_CONFIG_FILE" {
					return "/tmp/elsewhere.json"
				}

				return ""
			},
			Home: testHome,
			OS:   "linux",
		})

		if paths.SettingsFile != "/tmp/elsewhere.json" {
			t.Fatalf("settings = %q, want /tmp/elsewhere.json", paths.SettingsFile)
		}
	})

	t.Run("windows puts configuration and data in their own roots", func(t *testing.T) {
		t.Parallel()

		paths := config.Resolve(platform.Env{
			Getenv: func(name string) string {
				switch name {
				case "APPDATA":
					return `C:\Users\ana\AppData\Roaming`
				case "LOCALAPPDATA":
					return `C:\Users\ana\AppData\Local`
				}

				return ""
			},
			Home: `C:\Users\ana`,
			OS:   "windows",
		})

		// filepath is the host's, so a Windows path resolved on Linux keeps a mix of
		// separators. The shape is what matters, not which slash joined it.
		if got := slash(paths.Accounts); !strings.HasSuffix(got, "AppData/Roaming/slack-browser-mcp/accounts") {
			t.Fatalf("accounts = %q, want it under AppData/Roaming", paths.Accounts)
		}

		if got := slash(paths.ArchiveDir); !strings.HasSuffix(got, "AppData/Local/slack-browser-mcp/archive") {
			t.Fatalf("archive = %q, want it under AppData/Local", paths.ArchiveDir)
		}
	})
}
