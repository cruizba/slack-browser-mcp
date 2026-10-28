import assert from 'node:assert/strict'
import { test } from 'node:test'

import { candidates, parseTarget, resolvePeer, targetLabel } from '../src/forum/addressing.ts'
import { ForumError } from '../src/errors.ts'
import type { Peer } from '../src/forum/roster.ts'

const roster: Peer[] = [
  { from: 'builder@laptop', name: 'builder', host: 'laptop', capabilities: ['code'], sent: '', ts: '1' },
  { from: 'builder@desktop', name: 'builder', host: 'desktop', capabilities: ['code'], sent: '', ts: '2' },
  { from: 'reviewer@laptop', name: 'reviewer', host: 'laptop', capabilities: ['review'], sent: '', ts: '3' },
]

test('a destination is a peer, a name or a broadcast', () => {
  assert.deepEqual(parseTarget('builder@laptop'), { kind: 'peer', name: 'builder', host: 'laptop' })
  assert.deepEqual(parseTarget('Reviewer'), { kind: 'peer', name: 'reviewer' })
  assert.deepEqual(parseTarget('*'), { kind: 'anyone' })
  assert.equal(targetLabel(parseTarget('Builder@Laptop')), 'builder@laptop')
  assert.throws(() => parseTarget(''), /needs a destination/)
  assert.throws(() => parseTarget('a@b@c'), /not a destination/)
  assert.throws(() => parseTarget('a@'), /empty host/)
})

test('name@host is exact, a loose name has to be unique', () => {
  assert.equal(resolvePeer(roster, parseTarget('builder@laptop')).from, 'builder@laptop')
  assert.equal(resolvePeer(roster, parseTarget('reviewer')).from, 'reviewer@laptop')

  assert.throws(
    () => resolvePeer(roster, parseTarget('builder')),
    (error: unknown) =>
      error instanceof ForumError &&
      /matches 2 agents/.test(error.message) &&
      /builder@desktop/.test(error.message),
  )
  assert.throws(
    () => resolvePeer(roster, parseTarget('ghost')),
    (error: unknown) => error instanceof ForumError && /No peer/.test(error.message),
  )
})

test('a capability tag narrows a broadcast', () => {
  assert.equal(candidates(roster, { kind: 'anyone' }).length, 3)
  assert.deepEqual(
    candidates(roster, { kind: 'anyone', tag: 'review' }).map((peer) => peer.from),
    ['reviewer@laptop'],
  )
  assert.equal(candidates(roster, { kind: 'anyone', tag: 'deploy' }).length, 0)
})
