// Package config owns the user's preferences: where they live, what may be in
// them, and the three answers the rest of the tool reads from them — the default
// workspace, how stale the archive may be, and what signs a posted message.
package config

import (
	"path/filepath"
	"strings"

	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// Paths is every location the tool writes to, resolved once so a command cannot
// disagree with itself about where the account it just read came from.
type Paths struct {
	ConfigDir    string
	SettingsFile string
	// Accounts holds one JSON file per Slack workspace, each a session that can
	// post as the user.
	Accounts string
	// DataDir carries the rebuildable half: the archive, the login profile and
	// the audit log.
	DataDir      string
	ProfileDir   string
	LoginLock    string
	LoginLog     string
	AuditLog     string
	ForumFile    string
	ForumDir     string
	ArchiveDir   string
	ArchiveIndex string
}

// Resolve answers where everything goes, honouring the environment overrides
// first: they are what lets a test, or a user with an unusual layout, put the
// whole installation somewhere else.
func Resolve(env platform.Env) Paths {
	configDir := filepath.Join(platform.ConfigHome(env), "slack-browser-mcp")
	if dir := envValue(env, "SLACK_BROWSER_MCP_CONFIG_DIR"); dir != "" {
		configDir = dir
	}

	settingsFile := filepath.Join(configDir, "config.json")
	if file := envValue(env, "SLACK_BROWSER_MCP_CONFIG_FILE"); file != "" {
		settingsFile = file
	}

	dataDir := filepath.Join(platform.DataHome(env), "slack-browser-mcp")

	return Paths{
		ConfigDir:    configDir,
		SettingsFile: settingsFile,
		Accounts:     filepath.Join(configDir, "accounts"),
		DataDir:      dataDir,
		ProfileDir:   filepath.Join(dataDir, "profile"),
		LoginLock:    filepath.Join(dataDir, "login.lock"),
		LoginLog:     filepath.Join(dataDir, "login.log"),
		AuditLog:     filepath.Join(dataDir, "audit.jsonl"),
		ForumFile:    filepath.Join(configDir, "forum.json"),
		ForumDir:     filepath.Join(dataDir, "forum"),
		ArchiveDir:   filepath.Join(dataDir, "archive"),
		ArchiveIndex: filepath.Join(dataDir, "archive.sqlite"),
	}
}

func envValue(env platform.Env, name string) string {
	if env.Getenv == nil {
		return ""
	}

	return strings.TrimSpace(env.Getenv(name))
}
