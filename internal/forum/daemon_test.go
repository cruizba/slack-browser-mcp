package forum_test

import (
	"os"
	"testing"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/forum"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

func TestDaemonPathsHonourTheOverrides(t *testing.T) {
	t.Parallel()

	env := platform.Env{
		LookupEnv: func(name string) (string, bool) {
			switch name {
			case forum.DaemonDirEnv:
				return "/short", true
			case forum.DaemonSocketEnv:
				return "/short/custom.sock", true
			default:
				return "", false
			}
		},
	}

	paths := forum.PathsFor("/home/ana/.local/share/slack-browser-mcp/forum", env)

	if paths.Socket != "/short/custom.sock" {
		t.Fatalf("socket = %q", paths.Socket)
	}

	if paths.Pid != "/short/forum.pid" {
		t.Fatalf("pid = %q", paths.Pid)
	}
}

func TestSocketPathTooLongIsNamed(t *testing.T) {
	t.Parallel()

	long := "/tmp/" + string(make([]byte, 120))

	if err := forum.CheckSocketPath(long); err == nil {
		t.Fatal("a path a Unix address cannot hold was accepted")
	}
}

func TestIdleSecondsIgnoresAnEmptyOverride(t *testing.T) {
	t.Parallel()

	// An empty value means unset: parsing it as zero would make the daemon exit on its
	// first check, which is exactly the bug this keeps out.
	env := platform.Env{LookupEnv: func(string) (string, bool) { return "", true }}

	if got := forum.IdleSeconds(env); got != forum.DefaultIdleSeconds {
		t.Fatalf("IdleSeconds = %d, want the default", got)
	}

	env = platform.Env{LookupEnv: func(string) (string, bool) { return "120", true }}

	if got := forum.IdleSeconds(env); got != 120 {
		t.Fatalf("IdleSeconds = %d, want 120", got)
	}
}

func TestListenAnswersAndCleansUp(t *testing.T) {
	t.Parallel()

	// A Unix address is short: a macOS temporary directory is 90-odd bytes on its own,
	// which is what made this fail on that runner and not on Linux.
	path := shortSocket(t)

	listener, err := forum.Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	defer func() { _ = listener.Close() }()

	if !forum.Answers(path, time.Second) {
		t.Fatal("the socket does not answer its own listener")
	}

	// A second listener on a live socket is refused rather than replacing it.
	if _, err := forum.Listen(path); err == nil {
		t.Fatal("a second listener took over a live socket")
	}
}

func TestSayNeedsAConnectedPeer(t *testing.T) {
	t.Parallel()

	daemon := &forum.Daemon{}

	if err := daemon.Say("builder@laptop", "ana@laptop", "hello"); err == nil {
		t.Fatal("a line was accepted for a peer that is not connected")
	}

	daemon.Attach("connection-1", "builder@laptop")

	if err := daemon.Say("builder@laptop", "ana@laptop", "hello"); err != nil {
		t.Fatalf("Say: %v", err)
	}

	drained := daemon.Drain("builder@laptop")
	if len(drained) != 1 || drained[0].Text != "hello" || drained[0].From != "ana@laptop" {
		t.Fatalf("Drain = %+v", drained)
	}

	if len(daemon.Drain("builder@laptop")) != 0 {
		t.Fatal("a drain left the message behind")
	}
}

func TestLocalLinesAreSpooledPerPeer(t *testing.T) {
	t.Parallel()

	dir := shortSocket(t) + ".dir"

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating the directory: %v", err)
	}

	path := dir + "/forum.sock"

	listener, err := forum.Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	defer func() { _ = listener.Close() }()

	// Without a daemon a line is refused: nothing in Slack records what was said.
	if err := forum.DeliverSay(t.TempDir(), dir+"/absent.sock", "builder@laptop", "ana@laptop", "hi", 200*time.Millisecond); err == nil {
		t.Fatal("a line was accepted with no daemon running")
	}

	if err := forum.DeliverSay(dir, path, "builder@laptop", "ana@laptop", "hi", time.Second); err != nil {
		t.Fatalf("DeliverSay: %v", err)
	}

	lines := forum.DrainSay(dir, "builder@laptop")
	if len(lines) != 1 || lines[0].Text != "hi" || lines[0].From != "ana@laptop" {
		t.Fatalf("DrainSay = %+v", lines)
	}

	// A drain forgets what it answered.
	if len(forum.DrainSay(dir, "builder@laptop")) != 0 {
		t.Fatal("a drain left the line behind")
	}
}

// shortSocket is a socket path the kernel can hold, which t.TempDir() cannot promise.
func shortSocket(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "forum") //nolint:usetesting // t.TempDir() is a macOS path too long for a Unix address
	if err != nil {
		t.Fatalf("creating a temporary directory: %v", err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return dir + "/forum.sock"
}
