package cli

import (
	"slices"
	"testing"
)

// TestEveryAdvertisedCommandIsReachable is the guard for the failure it was written
// after: `forget` and `say` sat in the help table for a while with nothing behind them,
// so the surface promised commands that answered "not ported yet". A help entry is a
// promise, and this test is where it gets kept.
func reachable(name string) bool {
	if _, known := dispatchTable[name]; known {
		return true
	}

	_, aliased := aliases[name]

	return aliased
}

func TestEveryAdvertisedCommandIsReachable(t *testing.T) {
	t.Parallel()

	for _, name := range commandOrder {
		if _, documented := commandTable[name]; !documented {
			t.Fatalf("%q is in commandOrder but has no page", name)
		}

		if !reachable(name) {
			t.Fatalf("%q is advertised in the help but nothing dispatches it", name)
		}
	}
}

func TestNothingDispatchesThatTheHelpHides(t *testing.T) {
	t.Parallel()

	// The other direction: a command nobody can find is as bad as a page with no
	// command.
	for name := range dispatchTable {
		if !slices.Contains(commandOrder, name) {
			t.Fatalf("%q dispatches but the help never lists it", name)
		}
	}

	for alias, target := range aliases {
		if _, known := dispatchTable[target]; !known {
			t.Fatalf("alias %q points at %q, which dispatches nothing", alias, target)
		}
	}

	if len(commandOrder) == 0 {
		t.Fatal("the index is empty")
	}

	for name := range commandTable {
		if !slices.Contains(commandOrder, name) {
			t.Fatalf("the page for %q is unreachable from the index", name)
		}
	}
}

// TestEveryForgedSubcommandAnswers covers the same rule one level down, where the
// forum's moves live in two places: a map, and the reply kinds that differ only in the
// name they carry.
func TestEveryForgedSubcommandAnswers(t *testing.T) {
	t.Parallel()

	page, documented := commandTable["agent-forum"]
	if !documented {
		t.Fatal("the forum has no help page")
	}

	answeredByReply := map[string]bool{
		"accept": true, "decline": true, "progress": true, "result": true,
		"ack": true, "reopen": true, "cancel": true,
	}

	for _, name := range page.order {
		if _, known := forumMoves[name]; known {
			continue
		}

		if answeredByReply[name] {
			continue
		}

		t.Fatalf("agent-forum %q is documented but nothing answers it", name)
	}

	if len(page.order) != len(page.subcommands) {
		t.Fatalf("the forum lists %d subcommands but documents %d", len(page.order), len(page.subcommands))
	}
}
