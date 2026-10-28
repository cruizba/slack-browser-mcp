import assert from 'node:assert/strict'
import { test } from 'node:test'

import {
  assertFits,
  decode,
  encode,
  looksLikeEnvelope,
  render,
  summaryFor,
  type Envelope,
} from '../src/forum/envelope.ts'
import { newId } from '../src/forum/id.ts'

const ID = '01J9Z7K3QX4MB8WN2R5T6Y7Z8A'
const OTHER = '01J9Z7K3QX4MB8WN2R5T6Y7Z9B'

function task(overrides: Partial<Envelope> = {}): Envelope {
  return {
    v: 1,
    kind: 'task',
    id: ID,
    from: 'builder@laptop',
    to: 'reviewer@desktop',
    sent: '2026-10-28T09:12:04Z',
    title: 'revisar el PR',
    body: 'mira el diff de sync.ts',
    ...overrides,
  } as Envelope
}

test('every kind survives a round trip through the fence', () => {
  const envelopes: Envelope[] = [
    task(),
    {
      v: 1,
      kind: 'register',
      id: OTHER,
      from: 'builder@laptop',
      sent: '2026-10-28T09:00:00Z',
      name: 'builder',
      host: 'laptop',
      capabilities: ['code'],
    },
    {
      v: 1,
      kind: 'accept',
      id: OTHER,
      from: 'reviewer@desktop',
      sent: '2026-10-28T09:20:00Z',
      task: ID,
    },
    {
      v: 1,
      kind: 'result',
      id: OTHER,
      from: 'reviewer@desktop',
      sent: '2026-10-28T10:00:00Z',
      task: ID,
      body: 'listo, 3 comentarios',
      ok: true,
    },
  ]

  for (const envelope of envelopes) {
    const text = render(summaryFor(envelope), envelope)
    const decoded = decode(text)
    assert.deepEqual(decoded, envelope, `${envelope.kind} did not survive`)
    assert.equal(looksLikeEnvelope(text), true)
  }
})

test('the summary is readable and keeps the task outline', () => {
  assert.match(summaryFor(task()), /builder@laptop asks reviewer@desktop: revisar el PR/)
  assert.match(
    summaryFor(task({ to: '*', tag: 'review' } as Partial<Envelope>)),
    /asks anyone who can review/,
  )
  assert.match(
    summaryFor({
      v: 1,
      kind: 'result',
      id: OTHER,
      from: 'reviewer@desktop',
      sent: '2026-10-28T10:00:00Z',
      task: ID,
      body: 'done',
      ok: false,
    }),
    /could not finish/,
  )
})

test('prose, malformed JSON and unknown versions are not envelopes', () => {
  assert.equal(decode('just a message'), undefined)
  assert.equal(decode('```forum\nnot json\n```'), undefined)
  assert.equal(decode('```forum\n{"v":2,"kind":"task"}\n```'), undefined)
  assert.equal(decode('```forum\n{"v":1,"kind":"nope"}\n```'), undefined)
  assert.equal(
    decode('```forum\n' + JSON.stringify({ v: 1, kind: 'task', id: 'x' }) + '\n```'),
    undefined,
  )

  // A fence that exists but cannot be read is still recognisable, which is how the
  // ledger counts what it could not understand.
  assert.equal(looksLikeEnvelope('```forum\nnot json\n```'), true)
  assert.equal(looksLikeEnvelope('no fence here'), false)
})

test('a fence in the middle of prose is found, and the first one wins', () => {
  const first = task()
  const second = task({ title: 'second' } as Partial<Envelope>)
  const text = `antes\n${render('uno', first)}\n\ny después\n${render('dos', second)}`
  assert.equal(decode(text)?.id, first.id)
})

test('validation refused what Slack would mangle', () => {
  assert.throws(() => encode(task({ tag: 'review' } as Partial<Envelope>)), /tag only narrows/)
  assert.throws(
    () =>
      encode({
        v: 1,
        kind: 'register',
        id: OTHER,
        from: 'someone-else@laptop',
        sent: '2026-10-28T09:00:00Z',
        name: 'builder',
        host: 'laptop',
        capabilities: [],
      }),
    /from must be name@host/,
  )
  assert.throws(() => encode(task({ id: newId().slice(0, 10) } as Partial<Envelope>)), /forum id/)
})

test('the size budget is refused, never truncated', () => {
  assert.throws(() => assertFits('x'.repeat(4001)), /Slack accepts 4000/)
  assert.doesNotThrow(() => assertFits('x'.repeat(4000)))
})
