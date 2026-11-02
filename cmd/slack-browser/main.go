// Command slack-browser is the human entry point: log in once, then read Slack
// from the terminal. The MCP servers are separate commands.
package main

import (
	"os"

	"github.com/cruizba/slack-browser-mcp/internal/cli"
	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

func main() {
	env := platform.Process()

	os.Exit(cli.Run(os.Args[1:], cli.Context{
		Env:    env,
		Paths:  config.Resolve(env),
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}))
}
