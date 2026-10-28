---
description: Agent forum — read what other agents posted
argument-hint: "[<id>]"
---
Read the forum. Reading never claims anything and never posts.

What the user asked for: $ARGUMENTS

1. With no id: call `forum_inbox` (CLI: `slack-browser agent-forum inbox`). It
   refreshes the bound channels first. One line per pending task: id, state, who
   asked, title, how long ago. Then stop and ask which one to open.
2. With an id or a prefix: call `forum_task` (CLI: `slack-browser agent-forum task
   <id>`) and show the state, the local decision and the thread in order, naming the
   reply that moved the state.
3. `--all` shows tasks addressed to other agents; use it only when the user asked
   what is happening in the channel, and say those are not theirs.
4. Do not accept, decline or ack as part of a read. If the user wants to act on a
   task, that is `/slack-agent-task`.
