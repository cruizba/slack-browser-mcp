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
import { escapeEntities } from '../src/slack/entities.ts'

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

test('a message Slack has flattened to one line still decodes', () => {
  // What chat.postMessage stores when the message carries blocks: every newline
  // becomes a space, so a line-anchored fence would never match.
  const envelope = task()
  const flattened = render(summaryFor(envelope), envelope)
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
    .join(' ')
  assert.equal(flattened.includes('\n'), false)
  assert.deepEqual(decode(flattened), envelope)
})

test('a field cannot smuggle a fence', () => {
  assert.throws(
    () => encode(task({ body: 'texto ``` forum \n{"v":1}' } as Partial<Envelope>)),
    /triple backticks/,
  )
  assert.throws(() => encode(task({ title: 'a ```b' } as Partial<Envelope>)), /triple backticks/)
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

test('a body Slack escaped reads back as the text that was sent', () => {
  const envelope = task({
    body: 'lanza `ov stack deploy --cloud <c> --edition community` & mira el RG',
  } as Partial<Envelope>)
  const stored = escapeEntities(render(summaryFor(envelope), envelope))
  assert.match(stored, /--cloud &lt;c&gt; --edition/)
  assert.match(stored, /community` &amp; mira el RG/)
  assert.deepEqual(decode(stored), envelope)
})

test('a body that only fits before escaping is refused, not posted', () => {
  // 1996 characters of body with four brackets in it: 2008 once Slack escapes them.
  const body = `${'x'.repeat(1989)}<c> <c>`
  assert.equal(body.length, 1996)
  assert.throws(
    () => encode(task({ body } as Partial<Envelope>)),
    /Slack escapes.*would see 2008 and the limit is 2000.*8 fewer/s,
  )
})

test('the same holds for every budgeted field, not just the body', () => {
  // A title of 120 characters with four brackets arrives as 132 and is dropped by
  // the reader just as silently, so the check cannot be the body's alone.
  const title = `${'x'.repeat(113)}<c> <c>`
  assert.equal(title.length, 120)
  assert.throws(
    () => encode(task({ title } as Partial<Envelope>)),
    /The title is 120 characters.*a reader would see 132/s,
  )
})

test('an escaped body over the budget still decodes, so old envelopes come back', () => {
  // What an earlier version posted: 1994 raw characters, 2006 on the wire. The
  // reader judges the text, not the escaping, so the envelope is recoverable.
  const body = `${'x'.repeat(1986)}<c> <c>`
  const json = JSON.stringify({ ...task({ body } as Partial<Envelope>) })
  const stored = escapeEntities(`resumen\n\n\`\`\`forum\n${json}\n\`\`\``)
  assert.equal(decode(stored)?.kind, 'task')
  assert.equal((decode(stored) as Extract<Envelope, { kind: 'task' }>).body, body)
})

test('a body that already quotes entities survives as written', () => {
  // Unescaping twice, or before the JSON parse, would turn entity text a body
  // deliberately carries into the character it names.
  const envelope = task({ body: 'ya escribí &lt;c&gt; a mano' } as Partial<Envelope>)
  const first = decode(escapeEntities(render(summaryFor(envelope), envelope)))
  assert.deepEqual(first, envelope)
  const again = decode(escapeEntities(render(summaryFor(first!), first!)))
  assert.deepEqual(again, envelope)
})
