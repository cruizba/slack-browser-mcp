package slack

import (
	"regexp"
	"strings"
)

// ReferenceScope is what the resolver can answer. A nil field falls back to the
// raw id, which is what a directory that has never seen the id leaves behind.
type ReferenceScope struct {
	User      func(id string) string
	Channel   func(id string) string
	Usergroup func(id string) string
}

var referenceMarkup = regexp.MustCompile(`<([^<>]+)>`)

var specialMentions = map[string]bool{"here": true, paramChannel: true, "everyone": true}

// ResolveReferences turns message markup into the text a person would have read
// in Slack: `<@U123>` becomes `@ana`, `<#C123|general>` becomes `#general`. Text
// with no markup comes back unchanged, so the common case is one substring test.
//
// Resolution is display only: the archive keeps the markup so a refreshed
// directory improves old messages instead of freezing a stale name.
func ResolveReferences(text string, scope *ReferenceScope) string {
	if !strings.Contains(text, "<") {
		return UnescapeEntities(text)
	}

	resolved := referenceMarkup.ReplaceAllStringFunc(text, func(match string) string {
		return resolveOne(match[1:len(match)-1], scope)
	})

	return UnescapeEntities(resolved)
}

func resolveOne(body string, scope *ReferenceScope) string {
	target, label, hasLabel := strings.Cut(body, "|")

	switch {
	case strings.HasPrefix(target, "@"):
		return "@" + labelOr(label, hasLabel, scopeUser(scope, target[1:]), target[1:])

	case strings.HasPrefix(target, "#"):
		return "#" + labelOr(label, hasLabel, scopeChannel(scope, target[1:]), target[1:])

	case strings.HasPrefix(target, "!subteam^"):
		if hasLabel {
			return label
		}

		id := strings.TrimPrefix(target, "!subteam^")

		return "@" + labelOr("", false, scopeUsergroup(scope, id), id)

	case strings.HasPrefix(target, "!") && specialMentions[target[1:]]:
		return "@" + target[1:]

	// <!date^1518032400^{date_short}|Feb 7, 2018>: Slack always ships the fallback.
	case strings.HasPrefix(target, "!"):
		if hasLabel {
			return label
		}

		return "<" + body + ">"

	case strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:"):
		if hasLabel {
			return label + " (" + target + ")"
		}

		return target
	}

	return "<" + body + ">"
}

func labelOr(label string, hasLabel bool, resolved, fallback string) string {
	if hasLabel && label != "" {
		return label
	}

	if resolved != "" {
		return resolved
	}

	return fallback
}

func scopeUser(scope *ReferenceScope, id string) string {
	if scope == nil || scope.User == nil {
		return ""
	}

	return scope.User(id)
}

func scopeChannel(scope *ReferenceScope, id string) string {
	if scope == nil || scope.Channel == nil {
		return ""
	}

	return scope.Channel(id)
}

func scopeUsergroup(scope *ReferenceScope, id string) string {
	if scope == nil || scope.Usergroup == nil {
		return ""
	}

	return scope.Usergroup(id)
}
