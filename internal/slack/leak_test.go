package slack_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package when a test leaves a goroutine behind. The directory
// fetch fans out four lookups and waits for all of them, which is correct today and is
// the kind of thing a later edit breaks quietly: a missing `Done` strands three
// goroutines per call and nothing else would notice.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// http keeps its connection reaper and its idle-connection loops for the life
		// of the client, and no code here owns them.
		goleak.IgnoreTopFunction("net/http.(*Transport).queueForIdleConn"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"),
	)
}
