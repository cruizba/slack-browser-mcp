package forum_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package when a test leaves a goroutine behind. This is the one
// package where a leak is a real defect rather than an untidy test: the daemon, its
// socket, its idle timer and the per-connection endpoints all own goroutines that are
// supposed to end, and a leaked one would keep the process alive after `daemon stop`.
//
// The ignores are the runtime's and the SQLite driver's own background work, which no
// code here can stop; anything else reaching this list needs a reason written beside it.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"),
		goleak.IgnoreTopFunction("modernc.org/sqlite.(*conn).interrupt"),
	)
}
