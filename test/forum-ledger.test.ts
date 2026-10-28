import assert from 'node:assert/strict'
import { test } from 'node:test'

import type { ArchivedMessage } from '../src/archive/store.ts'
import { render, summaryFor, type Envelope } from '../src/forum/envelope.ts'
import { newId } from '../src/forum/id.ts'
import { buildLedger, findTask, stateLabel, type ForumTask } from '../src/forum/ledger.ts'
import { DEFAULT_LIMITS } from '../src/forum/limits.ts'
import { rosterFrom } from '../src/forum/roster.ts'

const ME = 'reviewer@desktop'
const PEER = 'builder@laptop'
const TASK = '01J9Z7K3QX4MB8WN2R5T6Y7Z8A'
const NOW = new Date('2026-10-28T10:00:00Z')

function at(minutes: number): string {
  const base = Date.UTC(2026, 9, 28, 9, 0, 0)
  return ((base + minutes * 60_000) / 1000).toFixed(6)
}

function message(ts: string, envelope: Envelope): ArchivedMessage {
  return {
    ts,
    channel: 'C0HUB',
    channel_name: 'agent-hub',
    text: render(summaryFor(envelope), envelope),
    synced_at: NOW.toISOString(),
  }
}

function register(sent: string, envelope: Envelope): ArchivedMessage {
  return message(sent, envelope)
}

function registers(): ArchivedMessage[] {
  return [
    register(at(0), {
      v: 1,
      kind: 'register',
      id: newId(),
      from: PEER,
      sent: '2026-10-28T09:00:00Z',
      name: 'builder',
      host: 'laptop',
      capabilities: ['code'],
    }),
    register(at(0), {
      v: 1,
      kind: 'register',
      id: newId(),
      from: ME,
      sent: '2026-10-28T09:00:00Z',
      name: 'reviewer',
      host: 'desktop',
      capabilities: ['review'],
    }),
  ]
}

function taskAt(ts: string, overrides: Partial<Envelope> = {}): ArchivedMessage {
  return { ...message(ts, {
    v: 1,
    kind: 'task',
    id: TASK,
    from: PEER,
    to: ME,
    sent: '2026-10-28T09:05:00Z',
    title: 'revisar el PR',
    body: 'mira el diff',
    ...overrides,
  } as Envelope), user: 'U0BUILDER' }
}

function reply(ts: string, kind: Envelope['kind'], from: string, extra: object = {}): ArchivedMessage {
  return message(ts, {
    v: 1,
    kind,
    id: newId(),
    from,
    sent: '2026-10-28T09:10:00Z',
    task: TASK,
    ...extra,
  } as Envelope)
}

function ledger(messages: ArchivedMessage[], now = NOW, limits = DEFAULT_LIMITS) {
  const roster = rosterFrom(messages)
  return buildLedger({
    messages,
    teamId: 'T0TEST',
    channelId: 'C0HUB',
    roster,
    me: ME,
    limits,
    now,
  })
}

function only(messages: ArchivedMessage[], now = NOW): ForumTask {
  const built = ledger(messages, now)
  assert.equal(built.tasks.length, 1)
  return built.tasks[0]!
}

test('a task remembers whose agent posted it', () => {
  const task = only([...registers(), taskAt(at(5))])
  assert.equal(task.owner, 'U0BUILDER', 'the Slack account, not the asserted from')
})

test('a task walks open, accepted, done and acknowledged', () => {
  const messages = [
    ...registers(),
    taskAt(at(5)),
    reply(at(10), 'accept', ME),
    reply(at(20), 'progress', ME, { body: 'voy por la mitad' }),
    reply(at(30), 'result', ME, { body: 'listo', ok: true }),
    reply(at(40), 'ack', PEER),
  ]
  const task = only(messages)
  assert.equal(task.state, 'acknowledged')
  assert.equal(task.closed, true)
  assert.equal(task.takenBy, ME)
  assert.deepEqual(
    task.events.map((event) => event.kind),
    ['accept', 'progress', 'result', 'ack'],
  )
})

test('done is not closed until the requester acks it', () => {
  const task = only([...registers(), taskAt(at(5)), reply(at(10), 'accept', ME), reply(at(20), 'result', ME, { body: 'ok' })])
  assert.equal(task.state, 'done')
  assert.equal(task.closed, false)
})

test('an accept from someone else does not claim the task', () => {
  const messages = [
    ...registers(),
    taskAt(at(5)),
    reply(at(10), 'accept', 'intruder@elsewhere'),
  ]
  const built = ledger(messages)
  assert.equal(built.unvouched, 1)
  assert.equal(built.tasks[0]?.state, 'open')
})

test('an accept from a roster peer that is not the addressee is ignored, not applied', () => {
  const messages = [
    ...registers(),
    taskAt(at(5)),
    reply(at(10), 'accept', PEER),
  ]
  const task = only(messages)
  assert.equal(task.state, 'open')
  assert.equal(task.ignored.length, 1)
  assert.equal(task.ignored[0]?.kind, 'accept')
})

test('decline closes, cancel closes, and a late result changes nothing', () => {
  const declined = only([...registers(), taskAt(at(5)), reply(at(10), 'decline', ME, { body: 'no me da' })])
  assert.equal(declined.state, 'declined')
  assert.equal(declined.closed, true)

  const cancelled = only([...registers(), taskAt(at(5)), reply(at(10), 'cancel', PEER)])
  assert.equal(cancelled.state, 'cancelled')

  const late = only([
    ...registers(),
    taskAt(at(5)),
    reply(at(10), 'accept', ME),
    reply(at(20), 'cancel', PEER),
    reply(at(30), 'result', ME, { body: 'tarde' }),
  ])
  assert.equal(late.state, 'cancelled')
  assert.equal(late.ignored.at(-1)?.kind, 'result')
})

test('reopen returns to accepted and respects the limit', () => {
  const once = only([
    ...registers(),
    taskAt(at(5)),
    reply(at(10), 'accept', ME),
    reply(at(20), 'result', ME, { body: 'ok' }),
    reply(at(30), 'reopen', PEER, { body: 'falta el test' }),
  ])
  assert.equal(once.state, 'accepted')
  assert.equal(once.reopens, 1)

  const exhausted = only([
    ...registers(),
    taskAt(at(5)),
    reply(at(10), 'accept', ME),
    reply(at(20), 'result', ME, { body: 'ok' }),
    reply(at(30), 'reopen', PEER, { body: 'uno' }),
    reply(at(40), 'result', ME, { body: 'ok' }),
    reply(at(50), 'reopen', PEER, { body: 'dos' }),
    reply(at(60), 'result', ME, { body: 'ok' }),
    reply(at(70), 'reopen', PEER, { body: 'tres' }),
  ])
  assert.equal(exhausted.state, 'done')
  assert.equal(exhausted.reopens, 2)
  assert.equal(exhausted.ignored.at(-1)?.kind, 'reopen')
})

test('a deadline or the ttl expires what nobody answered', () => {
  const byDeadline = only([
    ...registers(),
    taskAt(at(5), { deadline: '2026-10-28T09:30:00Z' }),
  ])
  assert.equal(byDeadline.state, 'expired')

  const byTtl = ledger([...registers(), taskAt(at(5))], new Date('2026-10-30T09:00:00Z')).tasks[0]!
  assert.equal(byTtl.state, 'expired')
})

test('silence marks a task stalled without changing its state', () => {
  const open = ledger([...registers(), taskAt(at(5))], new Date('2026-10-28T09:30:00Z')).tasks[0]!
  assert.equal(open.stalled, true)
  assert.match(stateLabel(open, new Date('2026-10-28T09:30:00Z')), /^open\(stalled 25m\)$/)

  const accepted = ledger(
    [...registers(), taskAt(at(5)), reply(at(10), 'accept', ME)],
    new Date('2026-10-28T11:00:00Z'),
  ).tasks[0]!
  assert.equal(accepted.state, 'accepted')
  assert.equal(accepted.stalled, true)
})

test('unreadable fences, orphan replies and strangers are counted, not hidden', () => {
  const built = ledger([
    ...registers(),
    taskAt(at(5)),
    { ts: at(6), channel: 'C0HUB', text: '```forum\nbroken\n```', synced_at: NOW.toISOString() },
    reply(at(7), 'accept', 'someone@elsewhere'),
    message(at(8), {
      v: 1,
      kind: 'accept',
      id: newId(),
      from: ME,
      sent: '2026-10-28T09:08:00Z',
      task: '01J9Z7K3QX4MB8WN2R5T6Y7Z9C',
    }),
  ])
  assert.equal(built.unreadable, 1)
  assert.equal(built.unvouched, 1)
  assert.equal(built.orphans, 1)
})

test('a task id is found by a unique prefix and refused when ambiguous', () => {
  const task = only([...registers(), taskAt(at(5))])
  assert.equal(findTask([task], '01j9z7').id, TASK)
  assert.throws(() => findTask([task], 'zzzz'), /No task matching/)
  assert.throws(() => findTask([{ ...task }, { ...task, id: '01J9Z7K3QX4MB8WN2R5T6Y7Z8B' }], '01j9z7'), /matches 2 tasks/)
})
