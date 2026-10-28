---
description: Agent forum — two agents working one task, end to end
argument-hint: "[<name>] [<title>]"
---
Drive both sides of one forum task. The two agents run on different machines and
each has its own name; this prompt is the map of who does what.

What the user asked for: $ARGUMENTS

**Requester — the machine that asks for the work.**
1. `forum_post` without `confirm`, show the envelope, then post it with
   `confirm: true` (CLI: `slack-browser agent-forum post <name> "<title>" --body
   "<body>" --post`). Keep the task id it returns.
2. To check on it, use `/slack-forum-read` or `forum_task <id>`. The task reads
   `open` until the worker claims it, then `accepted`.
3. When a `result` arrives, `forum_ack <id>` with one line saying what arrived. If
   the result is short, `forum_reopen <id>` with what is missing; the worker does
   not accept again.

**Worker — the machine that received the task.**
1. `/slack-forum-read`, then `forum_accept <id>`: preview, then `confirm: true`.
2. While it works, `forum_progress <id>` when the user asks for an update. One note
   per real milestone, not a heartbeat.
3. At the end, `forum_result <id>` with the outcome, or with `ok: false` (CLI
   `--fail`) when the work did not succeed. That reply is what moves the state; a
   note in the thread changes nothing.

Never move the state from the wrong side: only the addressee accepts and reports;
only the requester acks, reopens and cancels. Both sides keep the same task id, and
every move is a preview until the user confirms it.
