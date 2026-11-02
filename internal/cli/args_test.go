package cli

import (
	"reflect"
	"strings"
	"testing"
)

func knownFlags() map[string]flagDoc {
	return map[string]flagDoc{
		"send":   {},
		"thread": {value: "<ts>"},
		"limit":  {value: "<n>"},
	}
}

func TestParseArgsReadsASwitch(t *testing.T) {
	t.Parallel()

	args, err := parseArgs([]string{"--send", "general"}, knownFlags(), "send")
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}

	if !args.Bool("send") {
		t.Fatal("--send was not read as a switch")
	}

	if !reflect.DeepEqual(args.Positional, []string{"general"}) {
		t.Fatalf("positional = %v", args.Positional)
	}
}

func TestParseArgsTakesAValue(t *testing.T) {
	t.Parallel()

	args, err := parseArgs([]string{"--thread", "1792500000.000100", "--limit=5"}, knownFlags(), "send")
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}

	if value, _ := args.String("thread"); value != "1792500000.000100" {
		t.Fatalf("thread = %q", value)
	}

	if value, _ := args.String("limit"); value != "5" {
		t.Fatalf("limit = %q", value)
	}

	if len(args.Positional) != 0 {
		t.Fatalf("positional = %v", args.Positional)
	}
}

func TestParseArgsRefusesAnUnknownFlag(t *testing.T) {
	t.Parallel()

	_, err := parseArgs([]string{"--chek"}, knownFlags(), "update")
	if err == nil || !strings.Contains(err.Error(), "unknown flag --chek") {
		t.Fatalf("err = %v", err)
	}

	if !strings.Contains(err.Error(), "run `slack-browser update --help`") {
		t.Fatalf("the error does not say how to see the flags: %v", err)
	}
}

func TestParseArgsRefusesAValueOnASwitch(t *testing.T) {
	t.Parallel()

	if _, err := parseArgs([]string{"--send=yes"}, knownFlags(), "send"); err == nil ||
		!strings.Contains(err.Error(), "--send takes no value") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseArgsRefusesAMissingValue(t *testing.T) {
	t.Parallel()

	if _, err := parseArgs([]string{"--limit"}, knownFlags(), "read"); err == nil ||
		!strings.Contains(err.Error(), "--limit expects <n>") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseArgsStopsAtABareDoubleDash(t *testing.T) {
	t.Parallel()

	args, err := parseArgs([]string{"general", "--", "--send", "hi"}, knownFlags(), "send")
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}

	if args.Bool("send") {
		t.Fatal("a flag after a bare -- was read as a flag")
	}

	if !reflect.DeepEqual(args.Positional, []string{"general", "--send", "hi"}) {
		t.Fatalf("positional = %v", args.Positional)
	}
}
