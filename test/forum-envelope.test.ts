import assert from 'node:assert/strict'
import { test } from 'node:test'

import {
  assertFits,
  decode,
  decodeAttachment,
  encode,
  MAX_BODY_BYTES,
  render,
  summaryFor,
  type Envelope,
} from '../src/forum/envelope.ts'
import {
  carriesEnvelope,
  envelopeOf,
  forumFileName,
  isForumFileName,
} from '../src/forum/carrier.ts'
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
    assert.equal(carriesEnvelope({ text }), true)
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

  // A carrier that exists but cannot be read is still recognisable, which is how the
  // ledger counts what it could not understand.
  assert.equal(carriesEnvelope({ text: '```forum\nnot json\n```' }), true)
  assert.equal(carriesEnvelope({ text: 'no fence here' }), false)
  assert.equal(carriesEnvelope({ text: 'prosa', forum: '{"v":1}' }), true)
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

test('a body may hold the backticks a pasted thread carries', () => {
  // The whole point of the file: this is what a summary pasted out of Slack looks
  // like, and the fence it used to break is no longer on the wire.
  const body = 'mira\n\n```ts\nconst x = 1\n```\n\ny esto\n\n```bash\nnpm test\n```'
  const json = encode(task({ body } as Partial<Envelope>))
  assert.equal((JSON.parse(json) as { body: string }).body, body)
})

test('the short fields still refuse a fence', () => {
  assert.throws(() => encode(task({ title: 'a ```b' } as Partial<Envelope>)), /triple backticks/)
  assert.throws(
    () =>
      encode({
        v: 1,
        kind: 'presence',
        id: OTHER,
        from: 'builder@laptop',
        sent: '2026-10-28T09:00:00Z',
        name: 'builder',
        host: 'laptop',
        state: 'away',
        note: 'dejo esto ``` por aqui',
      }),
    /triple backticks/,
  )
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

test('a megabyte of body is accepted, and one byte more is not', () => {
  const big = 'x'.repeat(MAX_BODY_BYTES)
  assert.doesNotThrow(() => encode(task({ body: big } as Partial<Envelope>)))
  assert.throws(
    () => encode(task({ body: `${big}x` } as Partial<Envelope>)),
    /at most 1048576 bytes/,
  )
})

test('the budget counts bytes, so an accented body weighs more than its length', () => {
  // 600k `ñ` is 1.2 MB: characters would let it through and the file would not fit.
  const body = 'ñ'.repeat(600_000)
  assert.ok(body.length < MAX_BODY_BYTES)
  assert.throws(
    () => encode(task({ body } as Partial<Envelope>)),
    /at most 1048576 bytes/,
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

test('the attachment is the wire: no fence, and nothing escaped on the way', () => {
  const body = 'deploy con `<c>` & sin sustos, y &lt; tal cual'
  const envelope = task({ body } as Partial<Envelope>)
  const json = encode(envelope)
  assert.deepEqual(decodeAttachment(json), envelope)
  // Slack only escapes `text`, so the bytes of a file come back exactly as sent: a
  // body that quotes an entity on purpose must not be rewritten.
  const decoded = decodeAttachment(json)
  assert.equal(decoded?.kind === 'task' ? decoded.body : undefined, body)
})

test('a message is read from its file first, and from the fence when there is none', () => {
  const envelope = task()
  const file = encode(envelope)
  const fenced = render(summaryFor(envelope), envelope)

  assert.deepEqual(envelopeOf({ forum: file, text: 'a human line' }), envelope)
  assert.deepEqual(envelopeOf({ text: fenced }), envelope)
  // Both present: the file wins, because it is the one Slack cannot have rewritten.
  assert.deepEqual(envelopeOf({ forum: file, text: fenced }), envelope)
  assert.equal(envelopeOf({ text: 'solo prosa' }), undefined)
  assert.equal(envelopeOf({ forum: 'not json' }), undefined)
})

test('an envelope file is named after its id, and anything else is an attachment', () => {
  const id = newId()
  assert.equal(forumFileName(id), `forum-${id}.json`)
  assert.equal(isForumFileName(`forum-${id}.json`), true)
  assert.equal(isForumFileName('forum-.json'), false)
  assert.equal(isForumFileName('informe-forum-x.json'), false)
  assert.equal(isForumFileName('forum-x.txt'), false)
  assert.equal(isForumFileName(undefined), false)
})
