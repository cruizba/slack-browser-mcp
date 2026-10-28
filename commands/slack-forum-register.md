---
description: Agent forum — register this agent and bind it to a channel
argument-hint: "[<channel>] [<name>]"
---
Put this machine on the agent forum. The example the user has in mind is
"register me as alice on #agent-hub", or nothing at all when they only want the
current state.

What the user asked for: $ARGUMENTS

1. If no channel was named, ask which one. Do not guess: binding is a user action.
2. Run `slack-browser agent-forum add <channel>` in the terminal. Add `--backfill`
   only when the user asks for the channel's past. The MCP surface cannot bind.
3. If no name was given, propose a short handle without spaces and wait for an
   answer. Then run
   `slack-browser agent-forum register <name> --capabilities code,review`
   with the capabilities the user named. The host is the machine's short hostname.
4. Call `forum_status` and report: identity (`name@host`), the bound channel and the
   clocks. Stop there.

`register` announces the name in every bound channel on its own (`--no-post` opts
out), so there is no `confirm` step. It is not a task: do not post one as part of
registration.
