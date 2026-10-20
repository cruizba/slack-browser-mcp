---
description: Read a Slack channel, DM or thread (defaults to archive status)
argument-hint: "[channel] [count|thread_ts]"
---
Read Slack through the `slack-browser` MCP server: `read_messages`, `get_thread`,
`archive_status`, `auth_status`. If that server is not connected, run the same
thing with the `slack-browser` CLI (`slack-browser read`, `slack-browser thread`,
`slack-browser status`, `slack-browser archive stats`) instead.

What the user asked for (may be empty): $ARGUMENTS

- **No arguments** — call `archive_status` and `auth_status`. Report the stored
  workspaces, the default one and why, the age of the archive and any per-channel
  errors. Also mention when the archive is older than 15 minutes, but do not sync.
- **`<channel>`** — call `read_messages` with `limit` 20 unless a number was given
  (cap 100). Messages arrive oldest first. Summarise in order: date, author, and
  one line per message. Keep the `ts` of anything worth following up.
- **`<channel> <thread_ts>`** — when the second argument contains a dot, call
  `get_thread` with it; otherwise list the channel's most recent threads and pick.
- **`<channel> <count>`** — same as above with that limit.

`channel` accepts `#name`, `name`, a `C…`/`D…` id, or `me`. Names are per
workspace: if a name is ambiguous, show the candidates and ask which one instead
of guessing. Add `--workspace` only when the user names a workspace.

This command never writes to Slack. If the user asks to reply, stop and tell them
to use `/slack-post`.
