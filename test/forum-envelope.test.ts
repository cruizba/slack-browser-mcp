import assert from 'node:assert/strict'
import { test } from 'node:test'

import {
  assertFits,
  decodeFence,
  decodeJson,
  decodeMarkdown,
  encode,
  encodeJson,
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

function kinds(): Envelope[] {
  return [
    task(),
    {
      v: 1,
      kind: 'register',
      id: OTHER,
      from: 'builder@laptop',
      sent: '2026-10-28T09:00:00Z',
      name: 'builder',
      host: 'laptop',
      capabilities: ['code', 'review'],
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
}

test('every kind survives a round trip through the file', () => {
  for (const envelope of kinds()) {
    const text = encode(envelope)
    assert.equal(decodeMarkdown(text)?.kind, envelope.kind)
    assert.deepEqual(decodeMarkdown(text), envelope, `${envelope.kind} did not survive`)
  }
})

test('the file opens as frontmatter over the body', () => {
  const text = encode(task())
  assert.match(text, /^---\n/)
  assert.match(text, /\nkind: "task"\n/)
  assert.match(text, /\nfrom: "builder@laptop"\n/)
  assert.ok(text.endsWith('\n\nmira el diff de sync.ts'), 'the body is the tail, verbatim')
  assert.equal(text.includes('{'), false, 'no braces: this is not JSON any more')
})

test('every kind still survives the fenced form, which nothing writes any more', () => {
  for (const envelope of kinds()) {
    const text = render(summaryFor(envelope), envelope)
    assert.deepEqual(decodeFence(text), envelope, `${envelope.kind} did not survive`)
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

test('prose, an unknown version and a schema miss are not envelopes', () => {
  assert.equal(decodeMarkdown('just a message'), undefined)
  assert.equal(decodeMarkdown('---\nkind: "task"\n---\n'), undefined)
  assert.equal(decodeMarkdown('---\nv: 2\nkind: "task"\n---\n'), undefined)
  assert.equal(decodeMarkdown('---\nv: 1\nkind: "nope"\n---\n'), undefined)
  assert.equal(decodeMarkdown('---\nv: 1\nkind: "task"\nid: "x"\n---\n'), undefined)

  assert.equal(decodeFence('just a message'), undefined)
  assert.equal(decodeFence('```forum\nnot json\n```'), undefined)
  assert.equal(decodeFence('```forum\n{"v":2,"kind":"task"}\n```'), undefined)
  assert.equal(
    decodeFence('```forum\n' + JSON.stringify({ v: 1, kind: 'task', id: 'x' }) + '\n```'),
    undefined,
  )

  // A carrier that exists but cannot be read is still recognisable, which is how the
  // ledger counts what it could not understand.
  assert.equal(carriesEnvelope({ text: '```forum\nnot json\n```' }), true)
  assert.equal(carriesEnvelope({ text: 'no fence here' }), false)
  assert.equal(carriesEnvelope({ text: 'prosa', forum: '{"v":1}' }), true)
  assert.equal(carriesEnvelope({ text: 'prosa', files: [{ name: forumFileName(ID) }] }), true)
})

test('the body keeps what YAML would have eaten', () => {
  const body = '# Plan\n\n---\n\nno es un delimitador\n\n```ts\nconst x = 1\n```\n\nfin:'
  const envelope = task({ body } as Partial<Envelope>)
  assert.deepEqual(decodeMarkdown(encode(envelope)), envelope)
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
  assert.deepEqual(decodeFence(flattened), envelope)
})

test('a body may hold the backticks a pasted thread carries', () => {
  const body = 'mira\n\n```ts\nconst x = 1\n```\n\ny esto\n\n```bash\nnpm test\n```'
  const envelope = task({ body } as Partial<Envelope>)
  assert.deepEqual(decodeMarkdown(encode(envelope)), envelope)
  assert.equal((JSON.parse(encodeJson(envelope)) as { body: string }).body, body)
})

test('a title may now carry a fence and a comment marker', () => {
  // Only the fenced writer could be broken by a backtick, and nothing writes a fence
  // any more, so the writer stopped refusing them.
  const envelope = task({ title: 'fix the ``` parser #42' } as Partial<Envelope>)
  assert.deepEqual(decodeMarkdown(encode(envelope)), envelope)
  const presence: Envelope = {
    v: 1,
    kind: 'presence',
    id: OTHER,
    from: 'builder@laptop',
    sent: '2026-10-28T09:00:00Z',
    name: 'builder',
    host: 'laptop',
    state: 'away',
    note: 'dejo esto ``` por aqui',
  }
  assert.deepEqual(decodeMarkdown(encode(presence)), presence)
})

test('a fence in the middle of prose is found, and the first one wins', () => {
  const first = task()
  const second = task({ title: 'second' } as Partial<Envelope>)
  const text = `antes\n${render('uno', first)}\n\ny después\n${render('dos', second)}`
  assert.equal(decodeFence(text)?.id, first.id)
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
  assert.deepEqual(decodeFence(stored), envelope)
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
  assert.equal(decodeFence(stored)?.kind, 'task')
  assert.equal((decodeFence(stored) as Extract<Envelope, { kind: 'task' }>).body, body)
})

test('a body that already quotes entities survives as written', () => {
  // Unescaping twice, or before the JSON parse, would turn entity text a body
  // deliberately carries into the character it names.
  const envelope = task({ body: 'ya escribí &lt;c&gt; a mano' } as Partial<Envelope>)
  const first = decodeFence(escapeEntities(render(summaryFor(envelope), envelope)))
  assert.deepEqual(first, envelope)
  const again = decodeFence(escapeEntities(render(summaryFor(first!), first!)))
  assert.deepEqual(again, envelope)
})

test('the file is the wire: no fence, and nothing escaped on the way', () => {
  const body = 'deploy con `<c>` & sin sustos, y &lt; tal cual'
  const envelope = task({ body } as Partial<Envelope>)
  const text = encode(envelope)
  assert.deepEqual(decodeMarkdown(text), envelope)
  // Slack only escapes `text`, so the bytes of a file come back exactly as sent: a
  // body that quotes an entity on purpose must not be rewritten.
  const decoded = decodeMarkdown(text)
  assert.equal(decoded?.kind === 'task' ? decoded.body : undefined, body)
})

test('a message is read from its file first, and from the fence when there is none', () => {
  const envelope = task()
  const markdown = encode(envelope)
  const json = encodeJson(envelope)
  const fenced = render(summaryFor(envelope), envelope)
  const mdFile = [{ name: forumFileName(envelope.id), size: markdown.length }]
  const jsonFile = [{ name: `forum-${envelope.id}.json`, size: json.length }]

  assert.deepEqual(envelopeOf({ text: fenced, forum: markdown, files: mdFile }), envelope)
  assert.deepEqual(envelopeOf({ text: 'a human line', forum: json, files: jsonFile }), envelope)
  // Both present: the file wins, because it is the one Slack cannot have rewritten.
  assert.deepEqual(envelopeOf({ text: fenced, forum: markdown, files: mdFile }), envelope)
  assert.deepEqual(envelopeOf({ text: fenced }), envelope)
  assert.equal(envelopeOf({ text: 'solo prosa' }), undefined)
  assert.equal(envelopeOf({ forum: 'not an envelope' }), undefined)
})

test('a record that lost its file name is read by the shape of its first line', () => {
  const envelope = task()
  assert.deepEqual(envelopeOf({ forum: encode(envelope) }), envelope)
  assert.deepEqual(envelopeOf({ forum: encodeJson(envelope) }), envelope, 'a v0.3.2 record')
})

test('an envelope file is named after its id, and anything else is an attachment', () => {
  const id = newId()
  assert.equal(forumFileName(id), `forum-${id}.md`)
  assert.equal(isForumFileName(`forum-${id}.md`), true)
  assert.equal(isForumFileName(`forum-${id}.json`), true, 'what v0.3.2 wrote is still read')
  assert.equal(isForumFileName('forum-x.md'), true)
  assert.equal(isForumFileName('forum-.md'), false)
  assert.equal(isForumFileName('informe-forum-x.md'), false)
  assert.equal(isForumFileName('forum-x.txt'), false)
  assert.equal(isForumFileName(undefined), false)
})

test('decodeJson reads a file Slack never touched, and the fence undoes escaping', () => {
  const envelope = task({ body: 'a &lt; b' } as Partial<Envelope>)
  assert.deepEqual(decodeJson(encodeJson(envelope)), envelope)
  assert.equal(decodeJson('{"v":1'), undefined)
})
