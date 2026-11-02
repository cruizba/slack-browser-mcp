package forum

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// InternalClient is the name the daemon's own MCP client uses, so it never counts
// itself as a peer.
const InternalClient = "slack-browser-daemon-client"

// DaemonStatus is what `agent-forum daemon status` and `list` print.
type DaemonStatus struct {
	Running bool
	Socket  string
	Pid     int
	Peers   []string
}

// LocalMessage is one `forum say` line waiting for the peer it names.
type LocalMessage struct {
	From string
	Text string
}

// Daemon is the process that owns the archive and serves every local agent.
//
// It starts when a client needs it and leaves after a window with nobody connected,
// so there is no service to enable and no timer to keep alive.
type Daemon struct {
	Paths DaemonPaths
	// Handlers is the surface this daemon serves, which is the forum half plus the
	// tools that only make sense inside it.
	NewServer func(self func() string, connection net.Conn) Server
	// Idle is how long it waits with nobody connected.
	Idle time.Duration

	mutex       sync.Mutex
	peers       map[string][]LocalMessage
	labels      map[string]string
	connections int
	lastActive  time.Time
	started     bool
}

// Server is what a connection is served with: one MCP endpoint, created per client
// because it carries that client's name.
type Server interface {
	Serve(ctx context.Context) error
}

// IdleSeconds is the window before the daemon leaves, honouring the override.
func IdleSeconds(env platform.Env) int {
	raw, present := platform.Lookup(env, DaemonIdleEnv)
	if !present {
		return DefaultIdleSeconds
	}

	// An empty value means unset here: `strconv.Atoi("")` would answer zero and the
	// daemon would exit on its first check.
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return DefaultIdleSeconds
	}

	seconds, err := strconv.Atoi(trimmed)
	if err != nil || seconds <= 0 {
		return DefaultIdleSeconds
	}

	return seconds
}

// Serve accepts clients until the idle window passes with nobody connected.
func (d *Daemon) Serve(_ context.Context, listener net.Listener) error {
	if d.Idle == 0 {
		d.Idle = time.Duration(DefaultIdleSeconds) * time.Second
	}

	d.mutex.Lock()
	d.peers = map[string][]LocalMessage{}
	d.labels = map[string]string{}
	d.lastActive = time.Now()
	d.started = true
	d.mutex.Unlock()

	defer func() { _ = os.Remove(d.Paths.Socket) }()

	idle := time.AfterFunc(d.Idle, func() {
		if d.idle() {
			_ = listener.Close()
		}
	})

	defer idle.Stop()

	for {
		connection, err := listener.Accept()
		if err != nil {
			if d.idle() {
				return nil
			}

			return err
		}

		d.noteActivity()

		go d.serve(connection, idle) //nolint:gosec // one goroutine per client, ended by the connection
	}
}

// serve runs one connected client and releases it when it goes.
func (d *Daemon) serve(connection net.Conn, idle *time.Timer) {
	d.mutex.Lock()
	d.connections++
	connectionID := connection.RemoteAddr().String() + "|" + time.Now().String()
	self := ""
	d.mutex.Unlock()

	defer func() {
		_ = connection.Close()

		d.mutex.Lock()
		d.connections--
		delete(d.labels, connectionID)
		d.lastActive = time.Now()
		d.mutex.Unlock()

		idle.Reset(d.Idle)
	}()

	if d.NewServer == nil {
		return
	}

	server := d.NewServer(func() string {
		d.mutex.Lock()
		defer d.mutex.Unlock()

		return self
	}, connection)

	_ = server.Serve(context.Background())
}

// noteActivity resets the idle clock, which is what keeps the daemon up while a peer
// keeps working.
func (d *Daemon) noteActivity() {
	d.mutex.Lock()
	d.lastActive = time.Now()
	d.mutex.Unlock()
}

// idle reports whether the window has passed with nobody connected, and latches the
// answer so the two callers that can observe it agree.
func (d *Daemon) idle() bool {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	if d.connections > 0 {
		return false
	}

	return time.Since(d.lastActive) >= d.Idle/2
}

// Say delivers a line to a peer connected to this machine, without posting to Slack.
// Nothing records a local message, so it is a nudge and not the work itself: a peer
// that is not connected is refused rather than queued forever.
func (d *Daemon) Say(to, from, text string) error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	for _, label := range d.labels {
		if label != to {
			continue
		}

		d.peers[to] = append(d.peers[to], LocalMessage{From: from, Text: text})

		return nil
	}

	return errors.New("no agent called " + to + " is connected to this machine right now; post to Slack instead")
}

// Drain answers the lines waiting for a peer, and forgets them.
func (d *Daemon) Drain(label string) []LocalMessage {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	queued := d.peers[label]
	delete(d.peers, label)

	return queued
}

// Attach records that a client connected as this label, which is what makes it
// addressable before its first tool call.
func (d *Daemon) Attach(connection, label string) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	if d.labels == nil {
		d.labels = map[string]string{}
	}

	if d.peers == nil {
		d.peers = map[string][]LocalMessage{}
	}

	if label != "" {
		d.labels[connection] = label
	}
}

// Status reports whether the daemon is up and who is attached.
func Status(paths DaemonPaths, timeout time.Duration) DaemonStatus {
	status := DaemonStatus{Socket: paths.Socket, Running: Answers(paths.Socket, timeout)}

	if !status.Running {
		return status
	}

	if pid, err := readPid(paths.Pid); err == nil {
		status.Pid = pid
	}

	return status
}

// readPid answers the daemon's pid, when the file says one that is alive.
func readPid(path string) (int, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the path is the daemon's own pid file
	if err != nil {
		return 0, err
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, err
	}

	if !alive(pid) {
		return 0, errors.New("the pid in " + path + " is not running")
	}

	return pid, nil
}

// WritePid records this process as the daemon, so `status` can say which one.
func WritePid(path string, pid int) error {
	if err := os.MkdirAll(filepathDir(path), 0o700); err != nil {
		return err
	}

	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

func filepathDir(path string) string {
	if at := strings.LastIndex(path, "/"); at > 0 {
		return path[:at]
	}

	return "."
}
