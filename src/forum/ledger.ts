/**
 * The channel state of every task, derived from the archive on every read.
 *
 * Nothing is stored: a `task` envelope opens a task, replies move it, and a
 * missing reply is a task that never moved. That is what makes the thread the
 * record — a crashed agent cannot leave a ledger that disagrees with the channel.
 *
 * Two rules keep the derivation honest. An event only moves the state if it comes
 * from the side that is allowed to send it (the addressee claims, reports and
 * fails; the requester acks, reopens and cancels), and anything else is kept as an
 * ignored event instead of being dropped, so a spoofed `accept` shows up rather
 * than vanishing.
 */

import type { ArchivedMessage } from '../archive/store.ts'
import { ForumError } from '../errors.ts'
import { decode, looksLikeEnvelope, type ReplyKind } from './envelope.ts'
import { matchesId } from './id.ts'
import { forumTimings, type ForumLimits } from './limits.ts'
import { peerFor, type Peer } from './roster.ts'

export type ChannelState =
  | 'open'
  | 'accepted'
  | 'declined'
  | 'done'
  | 'failed'
  | 'acknowledged'
  | 'cancelled'
  | 'expired'

/** Closed means nobody is waiting for anything else; `done` and `failed` are not. */
const CLOSED: ReadonlySet<ChannelState> = new Set<ChannelState>([
  'declined',
  'acknowledged',
  'cancelled',
  'expired',
])

const REPORTED: ReadonlySet<ChannelState> = new Set<ChannelState>(['done', 'failed'])

export interface TaskEvent {
  kind: ReplyKind
  from: string
  ts: string
  sent: string
  body?: string
  ok?: boolean
}

export interface ForumTask {
  id: string
  team: string
  channel: string
  ts: string
  from: string
  to: string
  tag?: string
  title: string
  body: string
  deadline?: string
  parent?: string
  depth: number
  sent: string
  state: ChannelState
  closed: boolean
  reopens: number
  takenBy?: string
  events: TaskEvent[]
  ignored: TaskEvent[]
  lastActivityTs: string
  stalled: boolean
}

export interface Ledger {
  tasks: ForumTask[]
  /** Fences that carry no readable envelope. */
  unreadable: number
  /** Replies whose task is not in this window. */
  orphans: number
  /** Readable envelopes whose author is not in the roster. */
  unvouched: number
}

export interface LedgerArgs {
  messages: readonly ArchivedMessage[]
  teamId: string
  channelId: string
  roster: readonly Peer[]
  me: string
  limits: ForumLimits
  now: Date
}

function secondsOf(ts: string): number {
  const value = Number(ts)
  return Number.isFinite(value) ? value * 1000 : 0
}

/** `sent` and `deadline` are ISO 8601, while `ts` is Slack's numeric stamp. */
function epochOf(value: string): number {
  const numeric = Number(value)
  if (Number.isFinite(numeric) && numeric > 1_000_000) return numeric * 1000
  const parsed = Date.parse(value)
  return Number.isFinite(parsed) ? parsed : 0
}

function isAddressee(task: ForumTask, from: string): boolean {
  return task.to === '*' || task.to === from
}

function applyEvent(task: ForumTask, event: TaskEvent, limits: ForumLimits): boolean {
  const addressee = isAddressee(task, event.from)
  const requester = event.from === task.from

  switch (event.kind) {
    case 'accept':
      if (task.state === 'open' && addressee) {
        task.state = 'accepted'
        task.takenBy = event.from
        return true
      }
      return false
    case 'decline':
      if (task.state === 'open' && addressee) {
        task.state = 'declined'
        return true
      }
      return false
    case 'progress':
      return task.state === 'accepted' && addressee
    case 'result':
      if (task.state === 'accepted' && addressee) {
        task.state = event.ok === false ? 'failed' : 'done'
        return true
      }
      return false
    case 'error':
      if (task.state === 'accepted' && addressee) {
        task.state = 'failed'
        return true
      }
      return false
    case 'ack':
      if (REPORTED.has(task.state) && requester) {
        task.state = 'acknowledged'
        return true
      }
      return false
    case 'reopen':
      if (REPORTED.has(task.state) && requester) {
        if (task.reopens >= limits.reopens) return false
        task.state = 'accepted'
        task.reopens += 1
        return true
      }
      return false
    case 'cancel':
      if ((task.state === 'open' || task.state === 'accepted') && requester) {
        task.state = 'cancelled'
        return true
      }
      return false
  }
}

export function buildLedger(args: LedgerArgs): Ledger {
  const { messages, roster, me, limits, now } = args
  const timings = forumTimings(limits)
  const tasks = new Map<string, ForumTask>()
  let unreadable = 0
  let orphans = 0
  let unvouched = 0

  for (const message of messages) {
    if (message.deleted_at) continue
    const envelope = decode(message.text)
    if (!envelope) {
      if (looksLikeEnvelope(message.text)) unreadable += 1
      continue
    }
    if (envelope.kind === 'register') continue
    if (envelope.from !== me && !peerFor(roster, envelope.from)) {
      unvouched += 1
      continue
    }

    if (envelope.kind === 'task') {
      const existing = tasks.get(envelope.id)
      // A re-posted task id is the same task: the later envelope wins the header,
      // the earlier events stay, so an edit cannot reset a claim.
      if (!existing) {
        tasks.set(envelope.id, {
          id: envelope.id,
          team: args.teamId,
          channel: args.channelId,
          ts: message.ts,
          from: envelope.from,
          to: envelope.to,
          ...(envelope.tag !== undefined ? { tag: envelope.tag } : {}),
          title: envelope.title,
          body: envelope.body,
          ...(envelope.deadline !== undefined ? { deadline: envelope.deadline } : {}),
          ...(envelope.parent !== undefined ? { parent: envelope.parent } : {}),
          depth: envelope.depth ?? 0,
          sent: envelope.sent,
          state: 'open',
          closed: false,
          reopens: 0,
          events: [],
          ignored: [],
          lastActivityTs: message.ts,
          stalled: false,
        })
      }
      continue
    }

    const task = tasks.get(envelope.task)
    if (!task) {
      orphans += 1
      continue
    }
    const event: TaskEvent = {
      kind: envelope.kind,
      from: envelope.from,
      ts: message.ts,
      sent: envelope.sent,
      ...(envelope.body !== undefined ? { body: envelope.body } : {}),
      ...('ok' in envelope && envelope.ok !== undefined ? { ok: envelope.ok } : {}),
    }
    if (applyEvent(task, event, limits)) {
      task.events.push(event)
      task.lastActivityTs = message.ts
    } else {
      task.ignored.push(event)
    }
  }

  for (const task of tasks.values()) {
    if (!CLOSED.has(task.state) && !REPORTED.has(task.state)) {
      const age = now.getTime() - epochOf(task.sent)
      const deadlinePassed =
        task.deadline !== undefined && epochOf(task.deadline) < now.getTime()
      if (deadlinePassed || age > timings.ttlMs) {
        task.state = 'expired'
      }
    }
    task.closed = CLOSED.has(task.state)
    const silence = now.getTime() - secondsOf(task.lastActivityTs)
    task.stalled =
      (task.state === 'open' && silence > timings.ackMs) ||
      (task.state === 'accepted' && silence > timings.stallMs)
  }

  return {
    tasks: [...tasks.values()].sort((left, right) => Number(left.ts) - Number(right.ts)),
    unreadable,
    orphans,
    unvouched,
  }
}

export function matchingTasks(tasks: readonly ForumTask[], prefix: string): ForumTask[] {
  return tasks.filter((task) => matchesId(task.id, prefix))
}

export function findTask(tasks: readonly ForumTask[], prefix: string): ForumTask {
  const matches = matchingTasks(tasks, prefix)
  if (matches.length === 1) return matches[0]!
  if (matches.length === 0) {
    throw new ForumError(
      `No task matching "${prefix}".`,
      tasks.length === 0
        ? 'This channel has no tasks yet.'
        : `${tasks.length} task(s) are known here; run \`agent-forum inbox --all\` to list them.`,
    )
  }
  throw new ForumError(
    `"${prefix}" matches ${matches.length} tasks: ${matches
      .slice(0, 5)
      .map((task) => `${task.id} (${task.title})`)
      .join(', ')}.`,
    'Pass a longer prefix.',
  )
}

export function stateLabel(task: ForumTask, now: Date): string {
  if (!task.stalled) return task.state
  const silent = Math.round((now.getTime() - secondsOf(task.lastActivityTs)) / 60_000)
  return `${task.state}(stalled ${silent}m)`
}
