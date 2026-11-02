// Package failure holds the errors the CLI decides on by type, which is why
// they live together instead of next to whichever package raised them. Every
// message here is safe to show: none carries a token or a cookie.
package failure

// AuthError is a session Slack refused, or a login that could not finish. It
// carries the next step, and the CLI exits 2 on it because the command can be
// retried after signing in.
type AuthError struct {
	Message string
	Hint    string
}

func (e *AuthError) Error() string { return e.Message }

// APIError is a Slack method that answered with an error code. Method and Code
// are kept apart so a caller can decide on the code without parsing the text.
type APIError struct {
	Method string
	Code   string
	Detail string
}

func (e *APIError) Error() string {
	if e.Detail == "" {
		return e.Method + " failed: " + e.Code
	}

	return e.Method + " failed: " + e.Code + " (" + e.Detail + ")"
}

// StoreError is something wrong with what is on disk: a session file, the
// settings, the archive layout.
type StoreError struct {
	Message string
}

func (e *StoreError) Error() string { return e.Message }

// SchemaError is an archive written by a layout this build does not know. It
// carries what to do, and the CLI exits 2 on it.
type SchemaError struct {
	Message string
	Hint    string
}

func (e *SchemaError) Error() string { return e.Message }

// ForumError is a refusal from the agent forum: a channel that is not bound, a
// peer that is ambiguous, a limit that was reached.
type ForumError struct {
	Message string
	Hint    string
}

func (e *ForumError) Error() string { return e.Message }

// ChannelError is a conversation reference that names nothing, or too much.
type ChannelError struct {
	Message string
}

func (e *ChannelError) Error() string { return e.Message }
