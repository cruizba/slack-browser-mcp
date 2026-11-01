import assert from 'node:assert/strict'
import { test } from 'node:test'

import { changed, toRecord } from '../src/archive/record.ts'

const at = new Date('2026-11-01T00:00:00Z')
const message = { ts: '1793000000.000100', user: 'U0ANA', text: 'hola' }

test('a renamed channel makes the archived line stale', () => {
  const before = toRecord({ id: 'C0X', name: 'antes' }, message, at)
  const after = toRecord({ id: 'C0X', name: 'despues' }, message, at)

  assert.equal(
    changed(before, after),
    true,
    'the archive filters by the current name, so the line has to be rewritten',
  )
})

test('a fetch that carries no name does not flap the record', () => {
  const before = toRecord({ id: 'C0X', name: 'antes' }, message, at)
  const nameless = toRecord({ id: 'C0X' }, message, at)

  assert.equal(changed(before, nameless), false, 'a DM has no name, and a missing one proves nothing')
})

test('the same copy is not a change', () => {
  const record = toRecord({ id: 'C0X', name: 'antes' }, message, at)
  assert.equal(changed(record, record), false)
})
