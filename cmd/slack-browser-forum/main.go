// Command slack-browser-forum is the agent-facing forum: the tools one agent uses to
// hand work to another, served on stdio the way every MCP client expects.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/cruizba/slack-browser-mcp/internal/buildinfo"
	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/mcp"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

func main() {
	env := platform.Process()
	paths := config.Resolve(env)

	instructions, tools, err := mcp.ForumCatalog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "reading the tool catalogue: %v\n", err)
		os.Exit(1)
	}

	self, _ := os.Executable()

	available := mcp.ForumHandlers(mcp.Deps{
		Paths:   paths,
		Env:     env,
		Version: buildinfo.Version,
		Self:    self,
	})

	handlers := make(map[string]mcp.Handler, len(tools))

	for _, tool := range tools {
		if handler, known := available[tool.Name]; known {
			handlers[tool.Name] = handler

			continue
		}

		handlers[tool.Name] = mcp.UnknownTool(tool.Name)
	}

	server := &mcp.Server{
		Version:      buildinfo.Version,
		Instructions: instructions,
		Tools:        tools,
		Handlers:     handlers,
		In:           os.Stdin,
		Out:          os.Stdout,
	}

	if err := server.Serve(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
