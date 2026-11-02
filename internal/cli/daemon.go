package cli

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/buildinfo"
	"github.com/cruizba/slack-browser-mcp/internal/forum"
	"github.com/cruizba/slack-browser-mcp/internal/mcp"
)

// daemonStatusTimeout is how long a status check waits for the socket to answer: a
// missing daemon should cost that, not a hang.
const daemonStatusTimeout = 300 * time.Millisecond

// commandDaemon is `agent-forum daemon`.
func commandDaemon(ctx Context, forumContext forum.Context, rest []string) int {
	action := "status"
	if len(rest) > 0 {
		action = rest[0]
	}

	paths := forum.PathsFor(ctx.Paths.ForumDir, ctx.Env)

	switch action {
	case "serve":
		return daemonServe(ctx, forumContext)
	case "start":
		return daemonStart(ctx, paths)
	case "stop":
		return daemonStop(ctx, paths)
	default:
		return daemonReport(ctx, paths)
	}
}

// daemonReport says whether the daemon is up, which is what `list` and `status` print.
func daemonReport(ctx Context, paths forum.DaemonPaths) int {
	status := forum.Status(paths, daemonStatusTimeout)

	if !status.Running {
		emit(ctx.Stdout, "daemon: not running; the next forum client starts it (%s)\n", status.Socket)

		return 0
	}

	if status.Pid > 0 {
		emit(ctx.Stdout, "daemon: running at %s (pid %d)\n", status.Socket, status.Pid)
	} else {
		emit(ctx.Stdout, "daemon: running at %s\n", status.Socket)
	}

	return 0
}

// daemonStart launches the daemon detached, so it outlives the command that asked for
// it, and waits for the socket to answer before claiming it started.
func daemonStart(ctx Context, paths forum.DaemonPaths) int {
	if forum.Answers(paths.Socket, daemonStatusTimeout) {
		emit(ctx.Stdout, "the forum daemon was already running\n")

		return 0
	}

	self, err := os.Executable()
	if err != nil {
		return fail(ctx, err)
	}

	command := exec.CommandContext(context.Background(), self, "agent-forum", "daemon", "serve") //nolint:gosec // the binary is this one
	command.Stdout = nil
	command.Stderr = nil
	command.Stdin = nil

	if err := command.Start(); err != nil {
		return fail(ctx, err)
	}

	pid := command.Process.Pid

	_ = command.Process.Release()

	if err := forum.WritePid(paths.Pid, pid); err != nil {
		return fail(ctx, err)
	}

	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		if forum.Answers(paths.Socket, daemonStatusTimeout) {
			emit(ctx.Stdout, "started the forum daemon (pid %s); it leaves after a quiet window\n", strconv.Itoa(pid))

			return 0
		}

		time.Sleep(100 * time.Millisecond)
	}

	return fail(ctx, errors.New("the forum daemon did not start; check `slack-browser agent-forum daemon status`"))
}

// daemonStop ends the daemon the pid file names.
func daemonStop(ctx Context, paths forum.DaemonPaths) int {
	status := forum.Status(paths, daemonStatusTimeout)

	if !status.Running {
		emit(ctx.Stdout, "the forum daemon is not running\n")

		return 0
	}

	if status.Pid <= 0 {
		return fail(ctx, errors.New("the daemon is running but its pid file does not say which process; stop it by hand"))
	}

	process, err := os.FindProcess(status.Pid)
	if err != nil {
		return fail(ctx, err)
	}

	if err := process.Kill(); err != nil {
		return fail(ctx, err)
	}

	emit(ctx.Stdout, "stopped the forum daemon (pid %d)\n", status.Pid)

	return 0
}

// daemonServe runs the daemon in the foreground: it owns the socket and serves every
// local agent, and it is not meant to be typed.
func daemonServe(ctx Context, _ forum.Context) int {
	paths := forum.PathsFor(ctx.Paths.ForumDir, ctx.Env)

	listener, err := forum.Listen(paths.Socket)
	if err != nil {
		return fail(ctx, err)
	}

	if err := forum.WritePid(paths.Pid, os.Getpid()); err != nil {
		_ = listener.Close()

		return fail(ctx, err)
	}

	instructions, tools, err := mcp.Catalog()
	if err != nil {
		return fail(ctx, err)
	}

	available := mcp.Handlers(mcp.Deps{
		Paths: ctx.Paths, Env: ctx.Env, Version: buildinfo.Version, Self: selfPath(),
	})

	daemon := &forum.Daemon{
		Paths: paths,
		Idle:  time.Duration(forum.IdleSeconds(ctx.Env)) * time.Second,
		NewServer: func(_ func() string, connection net.Conn) forum.Server {
			handlers := make(map[string]mcp.Handler, len(tools))

			for _, tool := range tools {
				if handler, known := available[tool.Name]; known {
					handlers[tool.Name] = handler

					continue
				}

				handlers[tool.Name] = mcp.UnknownTool(tool.Name)
			}

			return &socketServer{instructions: instructions, version: buildinfo.Version, tools: tools, handlers: handlers, connection: connection}
		},
	}

	if err := daemon.Serve(context.Background(), listener); err != nil {
		return fail(ctx, err)
	}

	return 0
}

// socketServer is one client's MCP endpoint, reading and writing the connection the
// daemon accepted.
type socketServer struct {
	instructions string
	version      string
	tools        []mcp.Tool
	handlers     map[string]mcp.Handler
	connection   net.Conn
}

// Serve answers the client until it goes, using the same line-framed protocol the
// stdio transport uses.
func (s *socketServer) Serve(ctx context.Context) error {
	server := &mcp.Server{
		Version:      s.version,
		Instructions: s.instructions,
		Tools:        s.tools,
		Handlers:     s.handlers,
		In:           s.connection,
		Out:          s.connection,
	}

	return server.Serve(ctx)
}

func selfPath() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}

	return self
}
