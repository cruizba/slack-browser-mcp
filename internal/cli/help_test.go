package cli

import (
	"strings"
	"testing"
)

func TestPagesCoverTheTable(t *testing.T) {
	t.Parallel()

	for _, name := range commandNames() {
		page, ok := commandPage(name, "")
		if !ok {
			t.Fatalf("%s has no page", name)
		}

		if !strings.HasPrefix(page, "Usage: "+program+" ") {
			t.Fatalf("%s does not start with its usage:\n%s", name, page)
		}

		if strings.Contains(page, undocument) {
			t.Fatalf("%s lists a flag nobody documented", name)
		}

		for flag := range flagSpecs(name) {
			if !strings.Contains(page, "--"+flag) {
				t.Fatalf("%s does not mention --%s", name, flag)
			}
		}

		for _, sub := range subcommandNames(name) {
			subPage, ok := commandPage(name, sub)
			if !ok {
				t.Fatalf("%s %s has no page", name, sub)
			}

			if !strings.HasPrefix(subPage, "Usage: "+program+" ") {
				t.Fatalf("%s %s does not start with its usage:\n%s", name, sub, subPage)
			}

			if strings.Contains(subPage, undocument) {
				t.Fatalf("%s %s lists a flag nobody documented", name, sub)
			}
		}

		if len(subcommandNames(name)) > 0 {
			if _, ok := commandPage(name, "nope"); ok {
				t.Fatalf("%s answered a page for a subcommand it does not have", name)
			}
		}
	}
}

func TestForumMovesHaveTheirOwnPageAndWhitelist(t *testing.T) {
	t.Parallel()

	moves := []string{"accept", "decline", "progress", "result", "ack", "reopen", "cancel"}

	known := map[string]bool{}
	for _, sub := range subcommandNames("agent-forum") {
		known[sub] = true
	}

	for _, move := range moves {
		if !known[move] {
			t.Fatalf("agent-forum has no %s subcommand", move)
		}

		page, ok := commandPage("agent-forum", move)
		if !ok || !strings.Contains(page, "agent-forum "+move+" ") {
			t.Fatalf("agent-forum %s does not appear in its own page", move)
		}

		if problem := usageProblem("agent-forum", move, []string{"post"}); problem != "" {
			t.Fatalf("agent-forum %s refused --post: %s", move, problem)
		}
	}

	if problem := usageProblem("agent-forum", "result", []string{"fail"}); problem != "" {
		t.Fatalf("result refused its own --fail: %s", problem)
	}

	if problem := usageProblem("agent-forum", "ack", []string{"fail"}); !strings.Contains(problem, "unknown flag --fail") {
		t.Fatalf("ack accepted --fail, which belongs to result: %q", problem)
	}
}

func TestIndexListsEveryCommand(t *testing.T) {
	t.Parallel()

	index := indexPage()

	for _, name := range commandNames() {
		if !strings.Contains(index, program+" "+name) {
			t.Fatalf("the index does not mention %s", name)
		}
	}
}

func TestHelpOnItsOwnCommand(t *testing.T) {
	t.Parallel()

	for _, flag := range []string{"--help", "-h"} {
		page, ok := helpPageFor("update", []string{flag})
		if !ok || !strings.HasPrefix(page, "Usage: "+program+" update ") {
			t.Fatalf("update %s did not answer with its page", flag)
		}
	}
}

func TestHelpFollowsAnAlias(t *testing.T) {
	t.Parallel()

	page, ok := helpPageFor("forum", []string{"--help"})
	if !ok || !strings.HasPrefix(page, "Usage: "+program+" agent-forum ") {
		t.Fatal("forum did not answer with the agent-forum page")
	}

	page, ok = helpPageFor("workspaces", []string{"list", "--help"})
	if !ok || !strings.Contains(page, "workspace [list]") {
		t.Fatal("workspaces list did not answer with the workspace list page")
	}
}

func TestHelpTakesTheSubcommandItFollows(t *testing.T) {
	t.Parallel()

	page, ok := helpPageFor("archive", []string{"search", "--help"})
	if !ok || !strings.HasPrefix(page, "Usage: "+program+" archive search ") {
		t.Fatalf("archive search: %q", page)
	}

	if strings.Contains(page, "Subcommands:") {
		t.Fatal("a subcommand page listed the subcommands again")
	}
}

func TestHelpLeavesMessageTextAlone(t *testing.T) {
	t.Parallel()

	if _, ok := helpPageFor("send", []string{"general", "--help"}); ok {
		t.Fatal("send took a --help after the channel, which is message text")
	}

	if _, ok := helpPageFor("update", []string{"--", "--help"}); ok {
		t.Fatal("--help after a bare -- was read as a flag")
	}
}

func TestHelpIgnoresAnUnknownCommand(t *testing.T) {
	t.Parallel()

	if _, ok := helpPageFor("nope", []string{"--help"}); ok {
		t.Fatal("a page was served for an unknown command")
	}

	if _, ok := resolveCommand("nope"); ok {
		t.Fatal("resolveCommand accepted an unknown command")
	}
}

func TestUsageProblem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		sub   string
		flags []string
		want  string
	}{
		{name: "read", sub: "general", flags: []string{"limit", "workspace"}},
		{name: "archive", sub: "search", flags: []string{"team"}, want: "unknown flag --team"},
		{name: "agent-forum", sub: "list", flags: []string{"json"}, want: "unknown flag --json"},
		{name: "archive", sub: "stats", flags: []string{"channel"}, want: "unknown flag --channel"},
		{name: "agent-forum", sub: "post", flags: []string{"body", "parent", "deadline", "post"}},
		{name: "agent-forum", sub: "wait", flags: []string{"offline"}},
		{name: "agent-forum", sub: "poll", flags: []string{"offline"}, want: "unknown flag --offline"},
		{name: "archive", sub: "nope", want: `unknown subcommand "nope"`},
		{name: "nope", sub: "", flags: []string{"whatever"}},
	}

	for _, tt := range tests {
		if got := usageProblem(tt.name, tt.sub, tt.flags); !strings.Contains(got, tt.want) {
			t.Errorf("usageProblem(%q, %q, %v) = %q, want %q", tt.name, tt.sub, tt.flags, got, tt.want)
		}
	}
}

func TestDefaultSubcommands(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]string{"agent-forum": "list", "archive": "stats", "read": ""} {
		if got := defaultSubcommand(name); got != want {
			t.Fatalf("defaultSubcommand(%q) = %q, want %q", name, got, want)
		}
	}
}
