import assert from 'node:assert/strict'
import { test } from 'node:test'

import { decodeMarkdown, encode, summaryFor, type Envelope } from '../src/forum/envelope.ts'
import { newId } from '../src/forum/id.ts'
import { parseCapabilities, parseCapability, sameCapabilities } from '../src/forum/identity.ts'

test('a capability list is lowercased, deduped and space-free', () => {
  assert.deepEqual(parseCapabilities('Review, postmortem , REVIEW'), ['review', 'postmortem'])
  assert.deepEqual(parseCapabilities('code review'), ['code-review'])
  assert.deepEqual(parseCapabilities('  '), [])
})

test('the capability limits are refused, not quietly trimmed', () => {
  assert.throws(() => parseCapabilities('a,b,c,d,e,f,g,h,i'), /at most 8/)
  assert.throws(() => parseCapability('x'.repeat(25)), /longer than 24/)
  assert.throws(() => parseCapability('   '), /cannot be empty/)
})

test('the same capabilities in another order are not a change', () => {
  assert.equal(sameCapabilities(['review', 'triage'], ['triage', 'review']), true)
  assert.equal(sameCapabilities(['review'], ['review', 'triage']), false)
})

test('a task carries the capability it needs, and reads as one', () => {
  const task: Envelope = {
    v: 1,
    kind: 'task',
    id: newId(),
    from: 'builder@laptop',
    to: 'reviewer@desktop',
    sent: '2026-11-01T10:00:00Z',
    title: 'review PR 12',
    body: 'the diff is in the archive',
    capability: 'review',
  }
  const decoded = decodeMarkdown(encode(task))
  assert.equal(decoded?.kind === 'task' ? decoded.capability : undefined, 'review')
  assert.equal(summaryFor(task), 'builder@laptop asks reviewer@desktop for review: review PR 12')

  const broadcast: Envelope = { ...task, to: '*' }
  assert.equal(summaryFor(broadcast), 'builder@laptop asks anyone who can review: review PR 12')
})
