// Package cli is the human entry point: argument parsing, the help pages, and
// the dispatch that turns them into one command.
package cli

import (
	"strings"
)

// ParsedArgs is what a command reads. Positionals keep their order, and a flag
// that takes a value is kept apart from one that does not, because `--send yes`
// must not pass for a send and `--limit` must not be read as a boolean.
type ParsedArgs struct {
	Positional []string

	values  map[string]string
	present map[string]bool
	order   []string
}

// String answers a flag's value and whether it was given.
func (a ParsedArgs) String(name string) (string, bool) {
	value, ok := a.values[name]

	return value, ok
}

// Bool answers whether a flag that takes no value was given.
func (a ParsedArgs) Bool(name string) bool {
	return a.present[name]
}

// Flags lists every flag the call carried, in the order it was typed, which is
// what the help whitelist reports back when one is out of place.
func (a ParsedArgs) Flags() []string {
	return a.order
}

// parseArgs reads only the flags the command declares, which is what keeps a
// typo from being taken for a flag the command ignores: `update --chek` stops
// instead of moving the checkout, and `--send yes` cannot pass for a send.
func parseArgs(tokens []string, known map[string]flagDoc, commandName string) (ParsedArgs, error) {
	parsed := ParsedArgs{
		values:  make(map[string]string),
		present: make(map[string]bool),
	}

	for index := 0; index < len(tokens); index++ {
		token := tokens[index]

		if token == "--" {
			parsed.Positional = append(parsed.Positional, tokens[index+1:]...)

			break
		}

		if !strings.HasPrefix(token, "--") {
			parsed.Positional = append(parsed.Positional, token)

			continue
		}

		body := token[2:]
		name, value, hasValue := strings.Cut(body, "=")

		spec, ok := known[name]
		if !ok {
			return ParsedArgs{}, &usageError{message: "unknown flag --" + name + "\nrun `" + program + " " + commandName + " --help` to see what it takes"}
		}

		parsed.order = append(parsed.order, name)

		if hasValue && name != "" {
			if spec.value == "" {
				return ParsedArgs{}, &usageError{message: "--" + name + " takes no value"}
			}

			parsed.values[name] = value

			continue
		}

		if spec.value == "" {
			parsed.present[name] = true

			continue
		}

		next, ok := peek(tokens, index+1)
		if !ok || strings.HasPrefix(next, "--") {
			return ParsedArgs{}, &usageError{message: "--" + name + " expects " + spec.value}
		}

		parsed.values[name] = next
		parsed.present[name] = true

		index++
	}

	return parsed, nil
}

func peek(tokens []string, at int) (string, bool) {
	if at < 0 || at >= len(tokens) {
		return "", false
	}

	return tokens[at], true
}

// usageError is a call the parser refused: a typo, a missing value or a value
// handed to a flag that takes none. Its text is what the user sees, so it is a
// sentence with the fix rather than a wrapped cause.
type usageError struct {
	message string
}

func (e *usageError) Error() string {
	return e.message
}
