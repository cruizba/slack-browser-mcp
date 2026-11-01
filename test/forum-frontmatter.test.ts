import assert from 'node:assert/strict'
import { test } from 'node:test'

import { readFrontmatter, writeFrontmatter } from '../src/forum/frontmatter.ts'

test('the four value shapes survive a round trip', () => {
  const values = {
    v: 1,
    kind: 'task',
    id: '01J9Z7K3QX4MB8WN2R5T6Y7Z8A',
    ok: true,
    depth: 2,
    capabilities: ['code', 'review'],
    title: 'revisar el PR',
  }
  const document = writeFrontmatter(values, 'el cuerpo')
  assert.equal(document.startsWith('---\n'), true)
  const read = readFrontmatter(document)
  assert.deepEqual(read?.values, values)
  assert.equal(read?.body, 'el cuerpo')
})

test('a value that would read as YAML is quoted instead', () => {
  // The three that bite in real YAML: a comment marker after a space, a colon, and a
  // document delimiter at the start of the line.
  const values = { title: 'fix the #42 bug: --- not a delimiter', note: 'a # b: c' }
  const read = readFrontmatter(writeFrontmatter(values))
  assert.deepEqual(read?.values, values)
})

test('the body is verbatim, delimiters and fences included', () => {
  const body = '# Título\n\n---\n\n```ts\nconst x = 1\n```\n\nfin:'
  const read = readFrontmatter(writeFrontmatter({ v: 1 }, body))
  assert.equal(read?.body, body)
})

test('a document with no body says so, and one with whitespace is no body', () => {
  assert.equal(readFrontmatter(writeFrontmatter({ v: 1 }))?.body, undefined)
  assert.equal(readFrontmatter(`---\nv: 1\n---\n\n\n`)?.body, undefined)
})

test('every shape the writer refuses', () => {
  assert.throws(() => writeFrontmatter({ 'no spaces': 1 }), /not a frontmatter key/)
  assert.throws(() => writeFrontmatter({ nested: { a: 1 } }), /does not fit/)
  assert.throws(() => writeFrontmatter({ mixed: ['a', 1] }), /does not fit/)
})

test('every document the reader refuses', () => {
  assert.equal(readFrontmatter('prose'), undefined)
  assert.equal(readFrontmatter('---\nv: 1'), undefined, 'unclosed frontmatter')
  assert.equal(readFrontmatter('---\nv 1\n---\n'), undefined, 'no colon')
  assert.equal(readFrontmatter('---\nnot a key: 1\n---\n'), undefined, 'key with a space')
  assert.equal(readFrontmatter('---\ntitle: bare\n---\n'), undefined, 'unquoted string')
  assert.equal(readFrontmatter('---\ntitle: null\n---\n'), undefined, 'null is not a shape')
  assert.equal(readFrontmatter('---\nv: 1\nv: 2\n---\n'), undefined, 'duplicate key')
})

test('a frontmatter block is short, so a huge body is not scanned for a delimiter', () => {
  const head = `---\nv: 1\nnote: ${JSON.stringify('x'.repeat(17_000))}\n---\n`
  assert.equal(readFrontmatter(head), undefined)
})

test('an empty frontmatter block is a document, and the schema decides it is no envelope', () => {
  assert.deepEqual(readFrontmatter('---\n---\n'), { values: {} })
})

test('CRLF documents read like LF ones', () => {
  assert.deepEqual(readFrontmatter('---\r\nv: 1\r\n---\r\n\r\ncuerpo')?.values, { v: 1 })
})
