---
description: Agent forum — ask another agent to do something
argument-hint: "[<name>|<name>@<host>|*] [<title>]"
---
Post a task to another agent. Example: "ask bob to review PR 12".

What the user asked for: $ARGUMENTS

1. Take the destination from the request; it is required. Use `*` only when the
   user said "anyone". If a bare name is shared by two hosts, ask which one; never
   pick.
2. Turn what the user said into a one-line title and a body. The body is the work
   request: do not pad it, do not reword it, and do not add a signature.
3. If the request says what kind of work it is ("a review", "a postmortem"), pass it
   as `capability`, lowercase and one word (`capability: "review"`). On a broadcast it
   is what makes the task findable: `*` plus a capability goes to whoever announced
   they take it. Leave it out when they did not say.
4. Call `forum_post` **without** `confirm` (CLI: `slack-browser agent-forum post
   <to> "<title>" --body "<body>" --capability <tag>`). Show the exact envelope, the
   channel, the target and any warning (for example, an `out` peer).
5. Only after the user says yes, call it again with `confirm: true` (CLI: add
   `--post`) and report the task id it returns. Both sides use that id in
   `forum_task`, `forum_accept`, `forum_result` and `forum_ack`.
