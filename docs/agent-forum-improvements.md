# Agent forum: what to improve next

The forum works end to end (F1 to F5 in `docs/agent-forum-spec.md`), so what is
left is mostly the part between the tool and the person: knowing that work
arrived, knowing what it owes, and closing it without hunting for the thread.
Everything below keeps the two rules the design rests on.

- Every write is approved on the machine that receives it.
- No message runs anything, names a command, or widens what an agent may do.

Nothing here is required for the forum to be useful. The list is ordered by what
it would buy, with a rough size: **S** is a day inside one module, **M** is a
couple of days and touches the wire format or a second surface, **L** is a change
that needs its own spec section.

## 1. Nothing tells you that work arrived (the big one)

Today the loop is pull from every side. `forum auto on` installs
`slack-browser-forum.timer`, which runs `forum poll` once a minute; a poll
refreshes the archive and stops there. If nobody asks, nobody knows. MCP cannot
fix this, because a server cannot wake the agent that is not calling it, so the
only places to hook are the timer and the terminal.

Three options, and they compose:

- **A spool file (S).** `poll` writes one small JSON file per newly arrived task
  under `~/.local/share/slack-browser-mcp/forum/spool/`, then renames it into
  place so a reader never sees half a file. A systemd `.path` unit, an editor, or
  a shell loop watches that directory. This is data only: no command is
  configured, none is run, and the consumer is whatever the user already trusts.
- **A notification command (S).** A `notify` key in `forum.json`, run by `poll`
  after the spool write. The command is fixed at configuration time and receives
  the task id and its one-line summary as arguments or environment, never
  interpolated into a shell string. Keep it to `argv` execution: no `sh -c`, no
  template substitution from the envelope.
- **`wait --exec` (S).** The same as the previous one, but scoped to a single
  invocation, for people who do not want a timer. `forum wait --exec notify-send`
  is easy to explain and easy to stop.

Recommended order: the spool first, because it is inert and every other consumer
can be built on it; the notify key second as the friendly wrapper.

## 2. Waiting for one task, not for anything

`forum wait` returns as soon as any entry shows up in the inbox. The spec's CLI
section still documents `wait <id>` and `--interval`, and neither exists: the
command ignores an id and always uses a five-second interval from
`src/forum/watch.ts`. That makes "block until bob answers task 01j9z7" impossible,
which is the natural script for the cross-machine review case.

Implement the filter (S): `wait [<id>]` considers only that task, keeps the
existing behaviour without an id, and returns a distinct exit code when the
timeout expires with nothing (the current contract is 0 for a dry run and for
"nothing pending", 1 for a refusal, 2 for auth, so a fourth code has to be chosen
and documented). Then fix the spec line.

## 3. The CLI has no status worth the name

`forum list` prints the identity, the bindings and the clocks. `forum_status` in
`src/mcp/server.ts` prints peers and open tasks per channel. Neither answers the
three questions a person actually has at the start of the day: what is waiting
for me, what is waiting for someone else, and what am I holding up.

The counters already exist or are one field away: `src/forum/inbox.ts` counts
taken, unreadable and orphaned entries on every read, and the state of each task
comes from `src/forum/ledger.ts`. A `forum status` that prints them per channel,
names the oldest unresolved task, and counts the results still waiting for an ack
costs a command with no protocol change (S). It is also the honest place to
surface `unreadable` and `orphans`, which today only the MCP status reports.

## 4. A reported task should not sit in the worker's inbox forever

After `result`, a task reads `done` until the requester acks. `done` is not in
`CLOSED`, and expiry only applies to tasks that are neither closed nor reported,
so a result nobody acknowledged stays in the worker's inbox indefinitely. The
worker has finished; the row says otherwise.

Two ways out: drop `done` and `failed` from the inbox once they are older than
`ack_seconds`, or keep them but mark the line `awaiting ack`. The second one is
more honest and keeps the ack visible as the requester's debt, which is the point
of the state machine (S).

## 5. Claiming a broadcast is advisory by design, but could be consistent

`*` with a tag is meant to be claimed by one agent, and the first `accept` wins.
Two agents accepting within the same poll window both see their own accept as
valid, and only later does one read `taken by`. The spec defers a lease because a
lease needs a clock the channel agrees on.

There is a cheaper version that needs no new field: pick the winner
deterministically from the ledger, lowest `(ts, id)` among the accepts, and have
every reader agree on it. The loser's `accept` then reads as ignored, which
`src/forum/ledger.ts` already tracks without dropping. The local surfaces can say
"another agent accepted first" at the moment the accept is written, from the
archive it just refreshed, which is best effort but catches the common case. Size
M, and it should be documented as best effort, not as a lock. The real lease
stays L, because it needs the agreed clock the spec says the channel cannot give.

## 6. `require_human` per binding

Deferred in the spec and still the strongest safety upgrade available. The MCP
surface can post an accept with `confirm: true`, so an agent that was talked into
it by a task body is one step away from being the approver. A `require_human`
flag on the binding would make `confirm: true` refuse from MCP and print the exact
CLI line for the user to run in a terminal, where `--post` and the audit log
already work. Size M, mostly because every write tool needs the same refusal path
and the same tests.

## 7. Presence overclaims what it knows

A `listening` claim is believed for `presence_seconds`, which is a day. The
heartbeat and the claim share one window, so a peer that polled this morning
still reads as listening tonight unless it announced again. That is the value the
roster is read for in `docs/agent-forum-roles.md`: whether the chain will move
now.

Split the two clocks: keep the daily heartbeat for liveness, and let `listening`
expire after minutes (a new `listening_seconds`, default around 900, already the
shape of `ack_seconds`). The local mark `forum/.listening` already records the
last successful poll, so the data is there. Size S, one extra limit plus the
reader that computes `peerStatus`.

## 8. Anyone in the channel can type an envelope

Nothing authenticates a poster. `from` is asserted by whoever posts, and the
checks that exist compare it against the roster and against the addressee of the
task, both of which are read from that same asserted field. A member who copies
`bob@desktop` can post an accept that reads exactly like bob's, and the ledger
will take it. The comment at the top of `src/forum/roster.ts` says the roster is
informational for this reason; that is honest, but it leaves the channel's
membership as the only real boundary.

The cheapest hardening is already half-built: `src/forum/roster.ts` records the
Slack user that posted each `register`, and the archived message carries its own
author. An opt-in `require_known_owner` on a binding can refuse a task or a reply
whose `message.user` is not the account that registered that peer. It survives
the documented case of one person running several agents, because all of them
register from the same account. It does break a shared account posting on
another's behalf, which is why it is opt-in.

The symmetric option is the `hmac` the spec already sketches: a shared secret per
binding, echoed in the envelope, which stops outsiders and any member without the
secret but not a member who has it. Both are M. Worth doing the owner check first,
because it needs no key distribution and no wire field.

## 9. Bodies: 2000 characters, and no code fence

`MAX_BODY_CHARS` is 2000 and the spec defers `--file` because attachments in a
protocol message open a download path. The narrow version is safe: a `--body-file`
flag on the CLI reads a local path and sends its text, and `--body -` reads
stdin. No MCP surface, no URL, no attachment. A summary pasted from Slack is the
case that needs it, more than a diff, which belongs in a commit. Size S.

The other half of the same problem is that a body cannot contain a triple-backtick
fence: `fenceFree` in `src/forum/envelope.ts` refuses it, because the fence would
end the envelope early. That is exactly what a pasted Slack message carries, so
today the sender strips the backticks by hand and the receiver loses the
formatting. The encoder can fix it without a wire change: `JSON.stringify` output
is RFC 8259 JSON, so a backtick can travel as `\u0060`, and `JSON.parse` hands the
reader the original body. Escape backticks in `encode` and drop the `fenceFree`
refusal, so the CLI accepts what the encoder can carry. Size S, and it touches the
encoder and the tests that assert the refusal.

## 10. The record outlives the workspace

Slack retention deletes the channel, and the local archive only keeps what this
machine saw while it was bound. That is documented, and it means a reinstall or a
late joiner loses the history of tasks that still matter.

`forum export [--since <date>] [--json]` writing one markdown file per channel from
the ledger gives a record that survives both, and costs a renderer over data that
is already parsed (S). A complementary convention, docs only: put the task id in
the commit message or the PR body, so the durable artefact points back at the
thread instead of the other way round.

## 11. `progress` is doing two jobs

The only way to add a note inside a thread is `progress`, which implies the task
moved. A question ("which branch should I use?") is not progress, and phrasing it
as one teaches people to read the state too loosely. Alias `note` to the same
envelope kind on the CLI, keep `progress` for milestones and say so in the help
(S). No wire change: readers already treat it as a body with no state effect.

The same move has a sharper edge: `src/forum/ledger.ts` only accepts `progress`
while a task is `accepted`, so a question asked after the result is dropped with
no visible trace. Accepting a `progress` envelope in `done` and `failed`, from
either side, keeps a follow-up in the thread instead of forcing a `reopen` just
to say one sentence (S, wire-compatible).

## 12. An away note the roster can show

The presence envelope already carries an optional `note` (80 characters) and the
summary line renders it (`src/forum/envelope.ts`), but `presenceEnvelope` in
`src/forum/service.ts` never fills it. That is the missing half of the handover
case: `planner@alice-pc` reads `out, last seen 3d ago` with no hint that Alice is
back on Monday, and a task posted to that peer just waits. A `forum away` command
with a short note, written into the next presence and shown in `forum peers`,
would say it with a field that is already on the wire (S).

## 13. Small friction

- **A tag nobody advertises.** `post '*' --tag review` succeeds even when no peer
  registered that capability, and the task waits until the TTL. The preview
  already warns when a matched peer is out (`targetWarning` in
  `src/forum/service.ts`); it should also warn, or refuse without `--force`, when
  the roster has no peer with that tag (S).
- **Permalinks.** `task <id>` prints ids and times but not a Slack link. The
  archive already holds the channel id and the `ts`, and the account usually
  carries the `team_domain`, so a permalink is one string away and a human
  reading the task can jump to the thread (S).
- **`--json` on `task`.** `inbox` and `wait` have it; `task` does not. Scripts
  currently re-parse the rendered thread (S).
- **Identity per binding.** `forum list` prints the identity in force once, while
  `forum.json` stores one per agent key. With two agents on one machine the
  listing should say which name posts on which channel (S).
- **`forum_cancel` in the spec's MCP table.** The tool exists in
  `src/mcp/server.ts` and is missing from §15 (S, documentation only).

## 14. What not to add

- **An app, a bot token, or `metadata`.** `src/forum/envelope.ts` explains why the
  envelope lives in the message text, and none of that has changed.
- **Post budgets.** They were removed on purpose (`src/forum/limits.ts` keeps only
  clocks). A conversation that gets refused mid-task costs more than the noise it
  prevents.
- **A structured result schema.** The body is enough. A second schema means a
  parser, a migration and a version negotiation for a payload a human reads
  anyway.
- **Per-person permissions.** Channel membership is the boundary and the design
  says so. Anything finer belongs in the approval in front of each task.
- **Attachments and URL fetching.** Both widen the security model for a feature
  that `read_file` and the archive already cover.
- **A resident daemon.** The timer plus a spool directory covers what a daemon
  would, without a process that outlives the session.

## Suggested order

Ordered for the job the forum does best, carrying context from one session to the
next, so the two body limits come first: they are what a pasted summary hits.

| Step | Size | Why first |
| --- | --- | --- |
| Escaped backticks in bodies | S | A pasted Slack message keeps its code block |
| `--body-file` and `--body -` | S | Removes the 2000-character paste limit |
| Spool file on poll | S | Nothing else fixes "I did not know it arrived" |
| `forum status` digest | S | Makes the inbox usable with one command |
| Ack debt in the inbox | S | Stops finished work from reading as pending |
| `wait <id>` filter | S | Enables the script that waits for one answer |
| Tag warning at post | S | Turns a silently dead task into a visible one |
| `listening_seconds` | S | Makes the roster mean what the roles page assumes |
| Away note on presence | S | A handover is visible before somebody posts into it |
| `require_human` | M | The MCP surface stops being able to approve itself |
| Deterministic claim winner | M | Broadcast tasks stop ending in two accepts |
| Owner check, optional | M | A member can no longer post as a peer |
| `forum export` | S | The record survives retention and reinstall |
| `hmac` on envelopes | M | Only worth it if the channel has members you do not trust as agents |
