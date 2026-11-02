// Command slack-browser is the human entry point: log in once, then read
// Slack from the terminal. The MCP servers are separate commands.
package main

import "fmt"

var version = "dev"

func main() {
	fmt.Printf("slack-browser %s\n", version)
}
