package forum

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// MCP over a Unix domain socket.
//
// A socket carries bytes, not messages, so the transport has to invent boundaries.
// The framing is the one the stdio transport already uses — one JSON object per line
// — which keeps the proxy between an agent and the daemon a line relay instead of a
// second protocol with its own rules about what a message is.
//
// The socket is the daemon's only door, so its permissions are the whole access
// policy: a directory only the user can enter and a socket file only the user can
// connect to. Anything in this process can write the archive, which is why there is
// no token on top.
const (
	// DaemonDirEnv moves the socket somewhere shorter, which a deep temporary
	// directory on macOS sometimes needs.
	DaemonDirEnv = "SLACK_BROWSER_MCP_DAEMON_DIR"
	// DaemonSocketEnv names the socket itself.
	DaemonSocketEnv = "SLACK_BROWSER_MCP_DAEMON_SOCKET"
	// DaemonIdleEnv is how long the daemon waits with nobody connected.
	DaemonIdleEnv = "SLACK_BROWSER_MCP_DAEMON_IDLE_SECONDS"
	// DefaultIdleSeconds is the window before the daemon leaves on its own.
	DefaultIdleSeconds = 600

	// maxSocketPath is the limit of a Unix address field.
	maxSocketPath = 100
)

// DaemonPaths is where the daemon's files live.
type DaemonPaths struct {
	Dir    string
	Socket string
	Pid    string
}

// PathsFor answers the daemon's paths, honouring the environment overrides first.
func PathsFor(forumDir string, env platform.Env) DaemonPaths {
	dir := forumDir

	if override, present := platform.Lookup(env, DaemonDirEnv); present && override != "" {
		dir = override
	}

	socket := filepath.Join(dir, "forum.sock")

	if override, present := platform.Lookup(env, DaemonSocketEnv); present && override != "" {
		socket = override
	}

	return DaemonPaths{Dir: dir, Socket: socket, Pid: filepath.Join(dir, "forum.pid")}
}

// CheckSocketPath refuses a path a Unix address cannot hold, before bind says
// something that does not explain itself.
func CheckSocketPath(path string) error {
	if len(path) > maxSocketPath {
		return errors.New("the forum socket path is too long for a Unix address (" +
			strconv.Itoa(len(path)) + " bytes): " + path +
			". Set " + DaemonDirEnv + " to a shorter directory.")
	}

	return nil
}

// Listen opens the socket. A file left behind by a daemon that was killed is not a
// listener and bind refuses to reuse it; a live one must not be removed, which is why
// this asks the path rather than trusting the caller's check from a moment ago.
func Listen(path string) (net.Listener, error) {
	if err := CheckSocketPath(path); err != nil {
		return nil, err
	}

	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	if err := platform.HardenDir(dir); err != nil {
		return nil, err
	}

	if Answers(path, 250*time.Millisecond) {
		return nil, errors.New("something is already listening on " + path)
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	var config net.ListenConfig

	listener, err := config.Listen(context.Background(), "unix", path)
	if err != nil {
		return nil, err
	}

	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()

		return nil, err
	}

	return listener, nil
}

// Connect opens a client connection with a deadline, so a missing daemon costs a
// time-out instead of a hang.
func Connect(path string, timeout time.Duration) (net.Conn, error) {
	if err := CheckSocketPath(path); err != nil {
		return nil, err
	}

	dialer := net.Dialer{Timeout: timeout}

	return dialer.DialContext(context.Background(), "unix", path)
}

// Answers reports whether something is listening and accepting.
func Answers(path string, timeout time.Duration) bool {
	connection, err := Connect(path, timeout)
	if err != nil {
		return false
	}

	_ = connection.Close()

	return true
}
