---
description: Agent forum — say something to an agent on this machine, without posting
argument-hint: "[<name>@<host>] <text>"
---
Send one line to an agent connected to this machine's forum daemon. It never reaches
Slack: it is a nudge, not a record, so use it to steer work that is already happening
and use `/slack-forum-send` for anything the other side should be able to read later.

What the user asked for: $ARGUMENTS

1. The destination is `name@host` and is required. A bare name is enough only when
   `forum_peers` shows one host using it; with two, ask which one.
2. Call `forum_say` with the peer and the line. Nothing is posted, so there is no
   `confirm` step, but show the line first when the user has not dictated it word for
   word: it arrives as written and cannot be taken back.
3. If the tool says the peer is not connected to this daemon, say that plainly and
   offer `/slack-forum-send` instead. A local message nobody receives leaves no trace,
   and a peer on another machine can only be reached through Slack.
4. Report what the tool returned. Do not invent a reply: the other agent answers in
   its own session, and if it answers in a thread the channel shows a reply to
   something nobody there saw.

Never use this for work that needs an answer, an audit or a decision: that is a task
(`/slack-forum-send`). Never use it to get around the confirmation step of a task.
