// Package auth owns the Slack session a browser login captures and the rules
// that keep it private. A token plus its cookie is the whole account: whoever
// reads that file can post as the user, so a file with any bit outside the
// owner's own is refused instead of quietly accepted.
//
// Only the read half lives here so far. Saving, marking a session as validated
// and removing it arrive with the login in the same phase.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// Account is one stored workspace session, secrets included.
type Account struct {
	TeamID      string `json:"team_id"`
	TeamName    string `json:"team_name"`
	TeamDomain  string `json:"team_domain,omitempty"`
	User        string `json:"user,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	Token       string `json:"token"`
	DCookie     string `json:"d_cookie"`
	DCookieName string `json:"d_cookie_name,omitempty"`
	SavedAt     string `json:"saved_at"`
	LastOKAt    string `json:"last_ok_at,omitempty"`
}

// Summary is what output shows: everything except the secrets.
type Summary struct {
	TeamID     string
	TeamName   string
	TeamDomain string
	User       string
	LastOKAt   string
}

// DefaultSource says where the default workspace came from, because the last
// line of `accounts` has to explain the choice rather than only state it.
type DefaultSource string

// The three ways a default workspace comes to be, in the order they are tried.
const (
	SourceEnvironment DefaultSource = "environment"
	SourceConfig      DefaultSource = "config"
	SourceOnly        DefaultSource = "only"
)

// Default is the workspace a call uses when it names none.
type Default struct {
	TeamID string
	Source DefaultSource
}

// TeamEnv names the workspace from the environment, ahead of the saved default.
const TeamEnv = "SLACK_BROWSER_MCP_TEAM"

var (
	teamIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	tokenPattern  = regexp.MustCompile(`^xox[a-z]-`)
)

// Store reads the account directory.
type Store struct {
	dir      string
	settings *config.Store
	env      platform.Env
}

// NewStore points at the account directory, the settings file and the
// environment that may name a workspace.
func NewStore(dir string, settings *config.Store, env platform.Env) *Store {
	return &Store{dir: dir, settings: settings, env: env}
}

// List returns every stored workspace, in file-name order, which is what the
// output order has always been.
func (s *Store) List() ([]Summary, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, fmt.Errorf("reading %s: %w", s.dir, err)
	}

	names := make([]string, 0, len(entries))

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") && !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}

	sort.Strings(names)

	accounts := make([]Summary, 0, len(names))

	for _, name := range names {
		account, err := s.readFile(filepath.Join(s.dir, name))
		if err != nil {
			return nil, err
		}

		accounts = append(accounts, account.summary())
	}

	return accounts, nil
}

// Get reads one workspace by team id.
func (s *Store) Get(teamID string) (Account, error) {
	path, err := s.pathFor(teamID)
	if err != nil {
		return Account{}, err
	}

	account, err := s.readFile(path)
	if err != nil {
		return Account{}, err
	}

	if account.TeamID != teamID {
		return Account{}, fmt.Errorf("%s declares team_id %s", path, account.TeamID)
	}

	return account, nil
}

// Resolve picks the session a call should use. Precedence: the caller's
// reference, then SLACK_BROWSER_MCP_TEAM, then the default saved with
// `slack-browser workspace use`, then the only stored session.
func (s *Store) Resolve(reference string) (Account, error) {
	accounts, err := s.List()
	if err != nil {
		return Account{}, err
	}

	if len(accounts) == 0 {
		return Account{}, errors.New("No Slack session stored yet. Run `slack-browser login`.")
	}

	if explicit := strings.TrimSpace(reference); explicit != "" {
		picked, err := s.pick(accounts, explicit, "")
		if err != nil {
			return Account{}, err
		}

		return s.Get(picked.TeamID)
	}

	if fromEnv := strings.TrimSpace(s.envValue(TeamEnv)); fromEnv != "" {
		picked, err := s.pick(accounts, fromEnv, " from "+TeamEnv)
		if err != nil {
			return Account{}, err
		}

		return s.Get(picked.TeamID)
	}

	configured, err := s.configuredDefault()
	if err != nil {
		return Account{}, err
	}

	if configured != "" {
		picked, err := s.pick(accounts, configured, " from default_workspace in config.json")
		if err != nil {
			return Account{}, err
		}

		return s.Get(picked.TeamID)
	}

	if len(accounts) == 1 {
		return s.Get(accounts[0].TeamID)
	}

	return Account{}, fmt.Errorf(
		"Several Slack sessions are stored: %s. Pass --workspace (or the workspace argument), or choose a permanent default with `slack-browser workspace use <name|domain|team-id>`.",
		formatAccounts(accounts),
	)
}

// Default answers which workspace applies when a call names none, and nil when
// the choice is ambiguous or nothing is stored.
func (s *Store) Default() (*Default, error) {
	accounts, err := s.List()
	if err != nil {
		return nil, err
	}

	if len(accounts) == 0 {
		return nil, nil
	}

	if fromEnv := strings.TrimSpace(s.envValue(TeamEnv)); fromEnv != "" {
		picked, err := s.pick(accounts, fromEnv, " from "+TeamEnv)
		if err != nil {
			return nil, err
		}

		return &Default{TeamID: picked.TeamID, Source: SourceEnvironment}, nil
	}

	configured, err := s.configuredDefault()
	if err != nil {
		return nil, err
	}

	if configured != "" {
		picked, err := s.pick(accounts, configured, " from default_workspace in config.json")
		if err != nil {
			return nil, err
		}

		return &Default{TeamID: picked.TeamID, Source: SourceConfig}, nil
	}

	if len(accounts) == 1 {
		return &Default{TeamID: accounts[0].TeamID, Source: SourceOnly}, nil
	}

	return nil, nil
}

func (s *Store) configuredDefault() (string, error) {
	settings, err := s.settings.Read()
	if err != nil {
		return "", err
	}

	if settings.DefaultWorkspace == nil {
		return "", nil
	}

	return *settings.DefaultWorkspace, nil
}

func (s *Store) envValue(name string) string {
	value, _ := platform.Lookup(s.env, name)

	return value
}

// pick accepts a team id, a domain or a name, exactly first and then as a
// substring, and refuses anything that matches more than one workspace.
func (s *Store) pick(accounts []Summary, wanted, context string) (Summary, error) {
	lowered := strings.ToLower(wanted)

	var exact []Summary

	for _, account := range accounts {
		if strings.ToLower(account.TeamID) == lowered ||
			strings.ToLower(account.TeamDomain) == lowered ||
			strings.ToLower(account.TeamName) == lowered {
			exact = append(exact, account)
		}
	}

	matches := exact

	if len(matches) == 0 {
		for _, account := range accounts {
			if strings.Contains(strings.ToLower(account.TeamName), lowered) ||
				(account.TeamDomain != "" && strings.Contains(strings.ToLower(account.TeamDomain), lowered)) {
				matches = append(matches, account)
			}
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		fix := " Run `slack-browser accounts` to see the stored workspaces."
		if context != "" {
			fix = " Fix it with `slack-browser workspace use <name|domain|team-id>`."
		}

		return Summary{}, fmt.Errorf("No Slack session matches \"%s\"%s. Stored: %s.%s",
			wanted, context, formatAccounts(accounts), fix)
	default:
		return Summary{}, fmt.Errorf("\"%s\"%s is ambiguous: %s.", wanted, context, formatAccounts(matches))
	}
}

func (s *Store) pathFor(teamID string) (string, error) {
	if !teamIDPattern.MatchString(teamID) {
		return "", fmt.Errorf("Invalid team id: %s", teamID)
	}

	return filepath.Join(s.dir, teamID+".json"), nil
}

func (s *Store) readFile(file string) (Account, error) {
	info, err := os.Stat(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Account{}, fmt.Errorf("No stored Slack session at %s. Run `slack-browser login`.", file)
		}

		return Account{}, fmt.Errorf("checking %s: %w", file, err)
	}

	private, mode, err := platform.Private(file)
	if err != nil {
		return Account{}, err
	}

	if !private {
		return Account{}, fmt.Errorf(
			"Refusing to read %s: permissions are %s, expected 600. Run: chmod 600 %s",
			file, octal(mode), file,
		)
	}

	if err := platform.CheckOwner(file, info); err != nil {
		return Account{}, err
	}

	raw, err := os.ReadFile(file) //nolint:gosec // the path comes from the account directory, not from input
	if err != nil {
		return Account{}, fmt.Errorf("reading %s: %w", file, err)
	}

	var account Account
	if err := json.Unmarshal(raw, &account); err != nil {
		return Account{}, fmt.Errorf("Stored session %s is not valid JSON. Run `slack-browser login` again.", file)
	}

	if problems := account.problems(); len(problems) > 0 {
		return Account{}, fmt.Errorf(
			"Stored session %s is not readable as an account (%s). Run `slack-browser login` again.",
			file, strings.Join(problems, "; "),
		)
	}

	return account, nil
}

// problems lists what is wrong with a stored account, one entry per required
// field, in the order the fields appear in the file.
//
// An absent key and an empty one read the same once decoded, so a required field
// reports the empty-string message either way; the TypeScript schema could tell
// them apart and said "Required" for the absent case. The optional fields are
// not checked at all, which is also the only reading available: empty is how they
// are absent.
func (a Account) problems() []string {
	problems := make([]string, 0, 4)

	add := func(field, problem string) {
		if problem != "" {
			problems = append(problems, field+": "+problem)
		}
	}

	add("team_id", regexProblem(teamIDPattern, a.TeamID))
	add("team_name", requiredProblem(a.TeamName))
	add("token", regexProblem(tokenPattern, a.Token))
	add("d_cookie", requiredProblem(a.DCookie))
	add("saved_at", requiredProblem(a.SavedAt))

	return problems
}

func regexProblem(pattern *regexp.Regexp, value string) string {
	if pattern.MatchString(value) {
		return ""
	}

	return "Invalid"
}

func requiredProblem(value string) string {
	if value != "" {
		return ""
	}

	return "String must contain at least 1 character(s)"
}

func (a Account) summary() Summary {
	return Summary{
		TeamID:     a.TeamID,
		TeamName:   a.TeamName,
		TeamDomain: a.TeamDomain,
		User:       a.User,
		LastOKAt:   a.LastOKAt,
	}
}

func formatAccounts(accounts []Summary) string {
	rendered := make([]string, 0, len(accounts))
	for _, account := range accounts {
		rendered = append(rendered, account.TeamID+" ("+account.TeamName+")")
	}

	return strings.Join(rendered, ", ")
}

func octal(mode os.FileMode) string {
	return fmt.Sprintf("%03o", mode.Perm()&0o777)
}
