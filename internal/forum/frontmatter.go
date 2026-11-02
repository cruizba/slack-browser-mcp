// Package forum is the channel where agents hand each other work: the envelope it
// travels in, the roster it implies, and the moves of the workflow.
package forum

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// The frontmatter dialect: YAML's shape (`key: value` between two `---` lines) with
// JSON's values. A string is always a double-quoted JSON string, an array a JSON
// array, a number or a boolean the bare literal. Real YAML would mean a parser and
// the rules nobody remembers (# starting a comment, : splitting a scalar), and a
// task title is exactly where those bite.
const (
	delimiter = "---"
	// maxFrontmatterChars bounds the scan: a megabyte of prose ending in `---` must
	// not be read to the end looking for a delimiter that was never there.
	maxFrontmatterChars = 16 * 1024
)

var (
	frontmatterKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
	numberValue    = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
)

// Document is a frontmatter block and the body under it.
type Document struct {
	Values map[string]any
	// Body is absent when the document carries nothing but whitespace.
	Body string
}

// Field is one frontmatter entry. It is a slice rather than a map because the order
// is part of what a person reading the file sees.
type Field struct {
	Key   string
	Value any
}

// WriteFrontmatter renders a document. A nil body leaves the block alone.
func WriteFrontmatter(fields []Field, body *string) (string, error) {
	lines := []string{delimiter}

	for _, field := range fields {
		if field.Value == nil {
			continue
		}

		if !frontmatterKey.MatchString(field.Key) {
			return "", errors.New(field.Key + " is not a frontmatter key")
		}

		encoded, err := encodeValue(field.Key, field.Value)
		if err != nil {
			return "", err
		}

		lines = append(lines, field.Key+": "+encoded)
	}

	lines = append(lines, delimiter)

	head := strings.Join(lines, "\n") + "\n"

	if body == nil {
		return head, nil
	}

	return head + "\n" + *body, nil
}

func encodeValue(key string, value any) (string, error) {
	switch typed := value.(type) {
	case string:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "", err
		}

		return string(encoded), nil
	case int:
		return strconv.Itoa(typed), nil
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(typed), nil
	case []string:
		parts := make([]string, 0, len(typed))

		for _, item := range typed {
			encoded, err := json.Marshal(item)
			if err != nil {
				return "", err
			}

			parts = append(parts, string(encoded))
		}

		return "[" + strings.Join(parts, ", ") + "]", nil
	default:
		return "", errors.New(key + " does not fit the frontmatter dialect")
	}
}

// ReadFrontmatter answers the document, or nil when the text is not one.
func ReadFrontmatter(text string) *Document {
	if !strings.HasPrefix(text, delimiter+"\n") && !strings.HasPrefix(text, delimiter+"\r\n") {
		return nil
	}

	values := map[string]any{}
	offset := strings.Index(text, "\n") + 1

	for {
		if offset > maxFrontmatterChars {
			return nil
		}

		breakAt := strings.Index(text[offset:], "\n")
		if breakAt < 0 {
			return nil
		}

		breakAt += offset

		line := strings.TrimSuffix(text[offset:breakAt], "\r")
		next := breakAt + 1

		if line == delimiter {
			if breakAt > maxFrontmatterChars {
				return nil
			}

			// The blank line the writer leaves under the delimiter is a separator, not
			// the start of the body.
			body := strings.TrimPrefix(text[next:], "\r\n")
			if body == text[next:] {
				body = strings.TrimPrefix(text[next:], "\n")
			}

			document := &Document{Values: values}

			if strings.TrimSpace(body) != "" {
				document.Body = body
			}

			return document
		}

		key, value, ok := readEntry(line)
		if !ok {
			return nil
		}

		if _, seen := values[key]; seen {
			return nil
		}

		values[key] = value
		offset = next
	}
}

func readEntry(line string) (string, any, bool) {
	colon := strings.Index(line, ":")
	if colon < 1 {
		return "", nil, false
	}

	key := line[:colon]
	if !frontmatterKey.MatchString(key) {
		return "", nil, false
	}

	raw := strings.TrimSpace(line[colon+1:])
	if raw == "" {
		return "", nil, false
	}

	switch raw[0] {
	case '"', '[':
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return "", nil, false
		}

		return key, value, true
	}

	switch raw {
	case "true":
		return key, true, true
	case "false":
		return key, false, true
	}

	if numberValue.MatchString(raw) {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil {
			return key, parsed, true
		}
	}

	return "", nil, false
}
