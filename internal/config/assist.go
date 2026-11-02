package config

import (
	"strconv"
	"strings"

	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// AssistedNicknameEnv overrides the stored nickname for one shell. An empty
// value turns signing off rather than falling back to the default, which is the
// only way to opt out without editing shared state.
const AssistedNicknameEnv = "SLACK_BROWSER_MCP_ASSISTED_NICKNAME"

const (
	maxNicknameChars = 60
	// The generated default also holds the words around the two names, so each
	// name is capped separately.
	maxAgentChars = 30
	maxUserChars  = 24
)

// SigningKind is what signs a posted message. Three states, not two: a
// configured nickname, the generated default and off are different answers, and
// the last two cannot be collapsed.
type SigningKind int

const (
	// SigningDefault means nothing is configured and the generated default applies.
	SigningDefault SigningKind = iota
	// SigningNickname means the user chose the text.
	SigningNickname
	// SigningOff means post bare, with no header at all.
	SigningOff
)

// Signing is the answer to "what follows Message Sent by AI". Nickname is only
// meaningful when Kind is [SigningNickname].
type Signing struct {
	Kind     SigningKind
	Nickname string
}

// CheckedNickname returns the nickname with its surrounding space removed, or
// the reason it cannot be used. The header is one bold line of Slack mrkdwn,
// which has no escape for `*` and cannot span lines, so both are refused instead
// of silently mangled.
func CheckedNickname(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", &InvalidError{Message: "The assisted nickname cannot be empty; clear it instead."}
	}

	if problem := nicknameProblem(value); problem != "" {
		return "", &InvalidError{Message: "The assisted nickname cannot be used: " + problem + "."}
	}

	return value, nil
}

// SigningFor resolves what signs a message: the environment over the settings
// file over the generated default.
func SigningFor(settings Settings, env platform.Env) (Signing, error) {
	if raw, present := platform.Lookup(env, AssistedNicknameEnv); present {
		value := strings.TrimSpace(raw)
		if value == "" {
			return Signing{Kind: SigningOff}, nil
		}

		if problem := nicknameProblem(value); problem != "" {
			return Signing{}, &InvalidError{
				Message: `"` + AssistedNicknameEnv + `" cannot be used: ` + problem + ".",
			}
		}

		return Signing{Kind: SigningNickname, Nickname: value}, nil
	}

	if settings.AssistedNickname != nil && *settings.AssistedNickname != "" {
		nickname, err := CheckedNickname(*settings.AssistedNickname)
		if err != nil {
			return Signing{}, err
		}

		return Signing{Kind: SigningNickname, Nickname: nickname}, nil
	}

	return Signing{Kind: SigningDefault}, nil
}

// DefaultNickname names both ends of the message — the client that produced the
// text and the account it was posted from — so a reader in a channel sees who is
// behind it. An unidentified client says "unknown" rather than leaving the
// sentence half-built; an account with no stored handle drops the "of ..." part.
func DefaultNickname(agent, user string) string {
	named := oneLine(agent, maxAgentChars)
	if named == "" {
		named = "unknown"
	}

	signature := "Agent '" + named + "'"
	if account := oneLine(user, maxUserChars); account != "" {
		signature += " of " + account
	}

	return truncate(signature, maxNicknameChars)
}

// ResolveNickname is the text to sign with, or nil when signing is off.
func ResolveNickname(signing Signing, agent, user string) *string {
	switch signing.Kind {
	case SigningOff:
		return nil
	case SigningNickname:
		nickname := signing.Nickname

		return &nickname
	case SigningDefault:
		generated := DefaultNickname(agent, user)

		return &generated
	}

	// Unreachable: SigningKind has three values and all are handled above. Go
	// cannot infer exhaustiveness for an integer type, so the switch needs a tail.
	return nil
}

func nicknameProblem(value string) string {
	if len([]rune(value)) > maxNicknameChars {
		return "at most 60 characters, got " + strconv.Itoa(len([]rune(value)))
	}

	if strings.ContainsAny(value, "*\r\n") {
		return "no * and no line break: the header is one bold line of Slack mrkdwn"
	}

	return ""
}

// oneLine flattens text that is about to sit inside a single bold line, then
// caps it. The cap counts runes, which is not the same count the TypeScript used:
// it measured UTF-16 code units, so an emoji-heavy name can come out one
// character longer here. Nothing downstream depends on the exact boundary.
func oneLine(raw string, limit int) string {
	flattened := strings.Map(func(r rune) rune {
		if r == '*' || r == '\r' || r == '\n' || r == '\t' {
			return ' '
		}

		return r
	}, raw)

	return strings.TrimSpace(truncate(strings.Join(strings.Fields(flattened), " "), limit))
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}

	return string(runes[:limit])
}
