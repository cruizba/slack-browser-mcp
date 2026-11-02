package redact_test

import (
	"strings"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/redact"
)

func TestTextHidesEverySecretShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "a token in a query string",
			in:   "POST /api/chat.postMessage?token=xoxc-1234-5678&text=hi",
			want: "POST /api/chat.postMessage?token=<redacted>&text=hi",
		},
		{
			name: "a session token on its own",
			in:   "the token is xoxc-1234567890-abcdef and it works",
			want: "the token is xox?-<redacted> and it works",
		},
		{
			name: "a cookie-shaped token",
			in:   "Cookie: d=xoxd-abc%2Bdef",
			want: "Cookie: d=xox?-<redacted>",
		},
		{
			name: "a bearer header",
			in:   "Authorization: Bearer abcdef1234567890",
			want: "Authorization: Bearer <redacted>",
		},
		{
			name: "text with nothing to hide",
			in:   "a normal sentence about tokens",
			want: "a normal sentence about tokens",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := redact.Text(tt.in); got != tt.want {
				t.Fatalf("Text(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTextKeepsTheLineReadable(t *testing.T) {
	t.Parallel()

	// The redaction is what a person reads in an error, so it must not eat the sentence
	// around the secret: only the token itself goes.
	got := redact.Text("slack rejected xoxc-1234567890-abcdef while posting to #general")

	if !strings.Contains(got, "while posting to #general") {
		t.Fatalf("the redaction ate the sentence: %q", got)
	}

	if strings.Contains(got, "1234567890") {
		t.Fatalf("the token survived: %q", got)
	}
}
