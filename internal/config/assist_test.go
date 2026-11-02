package config_test

import (
	"strings"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/config"
)

func TestSigningFor(t *testing.T) {
	t.Parallel()

	t.Run("nothing configured means the generated default", func(t *testing.T) {
		t.Parallel()

		signing, err := config.SigningFor(config.Settings{}, env(nil))
		if err != nil {
			t.Fatalf("SigningFor: %v", err)
		}

		if signing.Kind != config.SigningDefault {
			t.Fatalf("kind = %v, want default", signing.Kind)
		}
	})

	t.Run("an empty variable turns signing off instead of falling back", func(t *testing.T) {
		t.Parallel()

		signing, err := config.SigningFor(
			config.Settings{AssistedNickname: new("testNickname")},
			env(map[string]string{config.AssistedNicknameEnv: ""}),
		)
		if err != nil {
			t.Fatalf("SigningFor: %v", err)
		}

		if signing.Kind != config.SigningOff {
			t.Fatalf("kind = %v, want off", signing.Kind)
		}
	})

	t.Run("the environment wins over the file", func(t *testing.T) {
		t.Parallel()

		signing, err := config.SigningFor(
			config.Settings{AssistedNickname: new("from file")},
			env(map[string]string{config.AssistedNicknameEnv: "  from env  "}),
		)
		if err != nil {
			t.Fatalf("SigningFor: %v", err)
		}

		if signing.Kind != config.SigningNickname || signing.Nickname != "from env" {
			t.Fatalf("signing = %+v, want the trimmed environment value", signing)
		}
	})

	t.Run("the file is used when the environment is silent", func(t *testing.T) {
		t.Parallel()

		signing, err := config.SigningFor(config.Settings{AssistedNickname: new("from file")}, env(nil))
		if err != nil {
			t.Fatalf("SigningFor: %v", err)
		}

		if signing.Kind != config.SigningNickname || signing.Nickname != "from file" {
			t.Fatalf("signing = %+v, want the stored nickname", signing)
		}
	})

	t.Run("refuses a variable that cannot be used", func(t *testing.T) {
		t.Parallel()

		_, err := config.SigningFor(config.Settings{}, env(map[string]string{config.AssistedNicknameEnv: "a*b"}))
		if err == nil || !strings.Contains(err.Error(), config.AssistedNicknameEnv) {
			t.Fatalf("error = %v, want it to name the variable", err)
		}
	})
}

func TestDefaultNickname(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		agent string
		user  string
		want  string
	}{
		{name: "names both ends", agent: "claude-code", user: "carlos.ruiz", want: "Agent 'claude-code' of carlos.ruiz"},
		{name: "drops the account when there is none", agent: "pi", user: "", want: "Agent 'pi'"},
		{name: "says unknown rather than naming nobody", agent: "", user: "carlos.ruiz", want: "Agent 'unknown' of carlos.ruiz"},
		{name: "flattens a line break", agent: "pi\nbeta", user: "", want: "Agent 'pi beta'"},
		{name: "strips the asterisk that would end the bold", agent: "pi*", user: "", want: "Agent 'pi'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := config.DefaultNickname(tt.agent, tt.user); got != tt.want {
				t.Fatalf("DefaultNickname() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("stays inside the header limit", func(t *testing.T) {
		t.Parallel()

		got := config.DefaultNickname(strings.Repeat("a", 80), strings.Repeat("b", 80))
		if len([]rune(got)) > 60 {
			t.Fatalf("DefaultNickname() is %d runes, want at most 60: %q", len([]rune(got)), got)
		}
	})
}

func TestResolveNickname(t *testing.T) {
	t.Parallel()

	t.Run("off resolves to nothing to sign with", func(t *testing.T) {
		t.Parallel()

		if got := config.ResolveNickname(config.Signing{Kind: config.SigningOff}, "pi", "ana"); got != nil {
			t.Fatalf("ResolveNickname() = %q, want nil", *got)
		}
	})

	t.Run("a chosen nickname is used verbatim", func(t *testing.T) {
		t.Parallel()

		got := config.ResolveNickname(config.Signing{Kind: config.SigningNickname, Nickname: "testNickname"}, "pi", "ana")
		if got == nil || *got != "testNickname" {
			t.Fatalf("ResolveNickname() = %v, want testNickname", got)
		}
	})

	t.Run("the default names the client and the account", func(t *testing.T) {
		t.Parallel()

		got := config.ResolveNickname(config.Signing{Kind: config.SigningDefault}, "pi", "carlos.ruiz")
		if got == nil || *got != "Agent 'pi' of carlos.ruiz" {
			t.Fatalf("ResolveNickname() = %v, want the generated default", got)
		}
	})
}

func TestCheckedNickname(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		want     string
		contains string
	}{
		{name: "trims what it accepts", input: "  testNickname ", want: "testNickname"},
		{name: "refuses an empty nickname", input: "   ", contains: "cannot be empty"},
		{name: "refuses an asterisk", input: "a*b", contains: "no * and no line break"},
		{name: "refuses a line break", input: "a\nb", contains: "no * and no line break"},
		{name: "refuses one that is too long", input: strings.Repeat("x", 61), contains: "at most 60"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := config.CheckedNickname(tt.input)

			if tt.contains == "" {
				if err != nil {
					t.Fatalf("CheckedNickname(%q) error = %v", tt.input, err)
				}

				if got != tt.want {
					t.Fatalf("CheckedNickname(%q) = %q, want %q", tt.input, got, tt.want)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), tt.contains) {
				t.Fatalf("CheckedNickname(%q) error = %v, want it to contain %q", tt.input, err, tt.contains)
			}
		})
	}
}

// The nickname is cut to a length and the cut is off by one at the boundary: a name
// of exactly the limit that loses its last character reads as a typo, and nothing
// else in the tree would notice.
func TestDefaultNicknameCutsAtTheBoundary(t *testing.T) {
	t.Parallel()

	// 7 runes of prefix, 5 of " of ", 30 of agent and 18 of account: 60 exactly.
	agent := strings.Repeat("a", 30)
	user := strings.Repeat("b", 18)
	exact := "Agent '" + agent + "' of " + user

	if got := config.DefaultNickname(agent, user); got != exact {
		t.Fatalf("DefaultNickname at the limit = %q, want %q", got, exact)
	}

	// One rune over the agent limit, and the cut lands on the account rather than
	// swallowing it: the sentence still says who posted.
	if got := config.DefaultNickname(strings.Repeat("a", 31), user); got != exact {
		t.Fatalf("DefaultNickname one rune over = %q, want %q", got, exact)
	}

	// A huge agent name is capped on its own first, so the signature never eats the
	// account that would have fitted: 90 runes of agent become 30, and with no
	// account the sentence is still whole.
	if got := config.DefaultNickname(strings.Repeat("c", 90), ""); got != "Agent '"+strings.Repeat("c", 30)+"'" {
		t.Fatalf("DefaultNickname with a huge agent = %q", got)
	}
}

func TestCheckedNicknameBoundary(t *testing.T) {
	t.Parallel()

	// Sixty runes is usable; sixty-one is refused with the count. A boundary off by
	// one turns a working nickname into an error nobody can explain.
	sixty := strings.Repeat("x", 60)

	if got, err := config.CheckedNickname(sixty); err != nil || got != sixty {
		t.Fatalf("CheckedNickname(60 runes) = %q, %v", got, err)
	}

	if _, err := config.CheckedNickname(strings.Repeat("x", 61)); err == nil ||
		!strings.Contains(err.Error(), "at most 60 characters, got 61") {
		t.Fatalf("CheckedNickname(61 runes) = %v, want the count in the message", err)
	}
}
