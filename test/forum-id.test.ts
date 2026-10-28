import assert from 'node:assert/strict'
import { test } from 'node:test'

import { ID_LENGTH, isId, matchesId, newId, normalizeId } from '../src/forum/id.ts'

test('ids are 26 Crockford characters and grow with time', () => {
  const first = newId(1_700_000_000_000)
  const second = newId(1_700_000_001_000)
  assert.equal(first.length, ID_LENGTH)
  assert.ok(isId(first), first)
  assert.ok(first < second, `${first} should sort before ${second}`)
})

test('two ids in the same millisecond still sort, and never repeat', () => {
  const ids = Array.from({ length: 50 }, () => newId(1_700_000_000_000))
  assert.equal(new Set(ids).size, ids.length)
  assert.deepEqual([...ids].sort(), ids)
})

test('a clock that steps backwards does not produce an earlier id', () => {
  const late = newId(1_700_000_010_000)
  const early = newId(1_700_000_000_000)
  assert.ok(early > late, 'the stamp must not regress')
})

test('prefixes match case-insensitively and tolerate the Crockford letters', () => {
  const id = '01J9Z7K3QX4MB8WN2R5T6Y7Z8A'
  assert.ok(matchesId(id, '01j9z7'))
  assert.ok(matchesId(id, '01J9Z7K3QX'))
  assert.ok(matchesId('01J0Z7K3QX4MB8WN2R5T6Y7Z8A', '01jo'))
  assert.equal(matchesId(id, '01j9'), true)
  // Too short to be safe: anything shorter than four characters matches nothing.
  assert.equal(matchesId(id, '01j'), false)
  assert.equal(normalizeId(' 01j9z7 '), '01J9Z7')
})
