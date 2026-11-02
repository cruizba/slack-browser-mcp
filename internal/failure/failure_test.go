package failure_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/failure"
)

// These types are how the CLI decides an exit code and how a hint reaches the user, so
// what is pinned here is the two things callers rely on: the text each error renders,
// and that `errors.As` finds it through a wrapping chain.

func TestErrorText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"auth", &failure.AuthError{Message: "that session expired", Hint: "sign in again"}, "that session expired"},
		{"api without detail", &failure.APIError{Method: "conversations.list", Code: "invalid_auth"}, "conversations.list failed: invalid_auth"},
		{"api with detail", &failure.APIError{Method: "chat.postMessage", Code: "invalid_arguments", Detail: "no_text"}, "chat.postMessage failed: invalid_arguments (no_text)"},
		{"store", &failure.StoreError{Message: "the archive is unreadable"}, "the archive is unreadable"},
		{"schema", &failure.SchemaError{Message: "newer layout", Hint: "update"}, "newer layout"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := test.err.Error(); got != test.want {
				t.Fatalf("Error() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestErrorsAsFindsTheTypeThroughAWrap(t *testing.T) {
	t.Parallel()

	inner := &failure.AuthError{Message: "expired", Hint: "sign in again"}
	wrapped := errors.Join(errors.New("reading the session"), inner)

	var auth *failure.AuthError
	if !errors.As(wrapped, &auth) {
		t.Fatal("errors.As did not find the AuthError")
	}

	if auth.Hint != "sign in again" {
		t.Fatalf("hint = %q", auth.Hint)
	}

	// The hint stays out of the message: a message is the failure and a hint is what to
	// do about it, and they are printed as separate lines.
	if strings.Contains(auth.Error(), auth.Hint) {
		t.Fatalf("the hint leaked into the message: %q", auth.Error())
	}
}
