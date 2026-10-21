import assert from 'node:assert/strict'
import { test } from 'node:test'

import {
  ASSISTED_NICKNAME_ENV,
  assistedNickname,
  checkedNickname,
} from '../src/config/assist.ts'
import { StoreError } from '../src/errors.ts'
import { withAssistHeader } from '../src/slack/assist.ts'

test('the header is one bold line, then a blank line and the text', () => {
  assert.equal(
    withAssistHeader('latest deploy is green', 'Qwen3.8 27B'),
    '*Message assisted by AI Bot Qwen3.8 27B*\n\nlatest deploy is green',
  )
})

test('refuses a message that the header would push past Slack\'s limit', () => {
  assert.throws(() => withAssistHeader('x'.repeat(4000), 'Qwen3.8'), /Slack accepts 4000/)
  assert.doesNotThrow(() => withAssistHeader('x'.repeat(3000), 'Qwen3.8'))
})

test('no nickname configured means no header', () => {
  assert.equal(assistedNickname({}, {}), null)
})

test('the settings file supplies the nickname', () => {
  assert.equal(assistedNickname({ assisted_nickname: '  Qwen3.8  ' }, {}), 'Qwen3.8')
})

test('the environment wins over the settings file', () => {
  const env = { [ASSISTED_NICKNAME_ENV]: 'Claude' }
  assert.equal(assistedNickname({ assisted_nickname: 'Qwen3.8' }, env), 'Claude')
})

test('an empty environment variable disables the header', () => {
  assert.equal(assistedNickname({ assisted_nickname: 'Qwen3.8' }, { [ASSISTED_NICKNAME_ENV]: ' ' }), null)
})

test('refuses an environment nickname that would break the header line', () => {
  for (const value of ['a*b', 'two\nlines', 'x'.repeat(61)]) {
    assert.throws(
      () => assistedNickname({}, { [ASSISTED_NICKNAME_ENV]: value }),
      (error: unknown) => {
        assert.ok(error instanceof StoreError)
        assert.match(error.message, new RegExp(ASSISTED_NICKNAME_ENV))
        return true
      },
    )
  }
})

test('checkedNickname names what is wrong with a value about to be stored', () => {
  assert.equal(checkedNickname('  Qwen3.8  '), 'Qwen3.8')
  assert.throws(() => checkedNickname('   '), /cannot be empty/)
  assert.throws(() => checkedNickname('a*b'), /no \* and no line break/)
  assert.throws(() => checkedNickname('x'.repeat(61)), /at most 60 characters/)
})
