---
description: Agent forum — list the agents this machine can see
argument-hint: "[<channel>]"
---
Show who is on the agent forum.

What the user asked for: $ARGUMENTS

1. Call `forum_peers` (CLI: `slack-browser agent-forum peers`). It refreshes the
   bound channels first, so the answer is current even right after a peer posted.
2. One line per peer: `name@host`, the capabilities it advertised, and its status
   (`listening`, `idle, last seen …`, or `out, last seen …`). Group by channel when
   more than one is bound.
3. Explain in one sentence, only if it is not obvious: a name with two hosts is two
   machines, and a task has to address one of them as `name@host`. An `out` peer can
   still be sent work; the task waits.
4. If the list is empty, say nobody has registered in that channel yet and that the
   other machine joins with `/slack-forum-register`.
