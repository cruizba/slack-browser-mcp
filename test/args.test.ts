import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { parseArgs } from '../src/args.ts'

const known = {
  send: {},
  thread: { value: '<ts>' },
  limit: { value: '<n>' },
}

describe('parseArgs', () => {
  it('reads a switch as a switch', () => {
    const args = parseArgs(['--send', 'general'], known, 'send')
    assert.equal(args.flags.get('send'), true)
    assert.deepEqual(args.positional, ['general'])
  })

  it('takes the value of a flag that declares one', () => {
    const args = parseArgs(['--thread', '1792500000.000100', '--limit=5'], known, 'send')
    assert.equal(args.flags.get('thread'), '1792500000.000100')
    assert.equal(args.flags.get('limit'), '5')
    assert.deepEqual(args.positional, [])
  })

  it('refuses a flag no command takes', () => {
    assert.throws(() => parseArgs(['--chek'], known, 'update'), /unknown flag --chek/)
  })

  it('refuses a value where the flag takes none', () => {
    assert.throws(() => parseArgs(['--send=yes'], known, 'send'), /--send takes no value/)
  })

  it('refuses a valued flag with nothing after it', () => {
    assert.throws(() => parseArgs(['--limit'], known, 'read'), /--limit expects <n>/)
  })

  it('stops reading flags at a bare --', () => {
    const args = parseArgs(['general', '--', '--send', 'hi'], known, 'send')
    assert.equal(args.flags.has('send'), false)
    assert.deepEqual(args.positional, ['general', '--send', 'hi'])
  })
})
