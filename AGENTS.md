# slack-browser-mcp

Slack read access for agents: one browser login, then the Slack Web API straight
through, with no browser in the loop. Two faces: the `slack-browser` CLI and two
MCP servers (`slack` and `forum`, the latter a stdio relay to a per-machine
daemon).

## Status: Go rewrite in progress

The TypeScript implementation is the reference until the cutover. It is not
frozen and still ships, so a behavioural difference between the two is a bug in
the port unless it is deliberate and written down.

The port is verified in two layers: `test/` (42 hermetic tests) is the spec per
package, and the differential harness runs both binaries against the same fake
Slack and diffs their output. When they disagree, the TypeScript behaviour wins.

Specifications live in `docs/implementation-spec.md`,
`docs/agent-forum-spec.md` and `docs/reference.md`.

## Go development

Before any Go coding, review, debugging, troubleshooting, or setup task, load the `samber/cc-skills-golang@golang-how-to` skill first — it routes to whichever other Go skills the task needs.

Layout: `cmd/` holds the binaries, `internal/` everything else, one package per
domain (`config`, `auth`, `slack`, `archive`, `forum`, `mcp`, `cli`).
Dependencies are wired by explicit constructors; there is no DI framework.

Portability is a requirement, not a nicety: Linux, macOS and Windows, amd64 and
arm64. Platform paths live in `internal/platform`, resolved from an `Env` that
carries the target GOOS so all three layouts are testable from any machine; the
parts that are genuinely per-binary — file permissions, the forum transport — go
behind build tags. The CI matrix runs the tests on all three systems, which is
what actually exercises them. `CGO_ENABLED=0` must stay buildable, which is why
SQLite is `modernc.org/sqlite` and not the cgo binding.

The vendored agent skills are in `.agents/skills/`, copied from
`samber/cc-skills-golang`; `/golang-how-to` routes between them.
