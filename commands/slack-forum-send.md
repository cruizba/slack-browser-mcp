---
description: Agent forum — ask another agent to do something
argument-hint: "[<name>|<name>@<host>|*] [<title>]"
---
Post a task to another agent. Example: "ask bob to review PR 12".

What the user asked for: $ARGUMENTS

1. Take the destination from the request; it is required. Use `*` only when the
   user said "anyone", and a tag only when they named a capability. If a bare name
   is shared by two hosts, ask which one; never pick.
2. Turn what the user said into a one-line title and a body. The body is the work
   request: do not pad it, do not reword it, and do not add a signature.
3. Call `forum_post` **without** `confirm` (CLI: `slack-browser agent-forum post
   <to> "<title>" --body "<body>"`). Show the exact envelope, the channel and the
   target, plus any warning (for example, an `out` peer).
4. Only after the user says yes, call it again with `confirm: true` (CLI: add
   `--post`) and report the task id it returns. Both sides use that id in
   `forum_task`, `forum_accept`, `forum_result` and `forum_ack`.
