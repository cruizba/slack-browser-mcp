import assert from 'node:assert/strict'
import { test } from 'node:test'

import {
  ASSISTED_NICKNAME_ENV,
  checkedNickname,
  defaultNickname,
  resolveNickname,
  signingFor,
} from '../src/config/assist.ts'
import { StoreError } from '../src/errors.ts'
import { ASSISTED_RULE, describeSigning, withAssistHeader } from '../src/slack/assist.ts'

test('the text sits between two rules, below one bold line naming the bot', () => {
  assert.equal(
    withAssistHeader('latest deploy is green', 'Qwen3.8 27B'),
    [
      ASSISTED_RULE,
      '',
      '*Message Sent by AI Qwen3.8 27B*',
      '',
      'latest deploy is green',
      '',
      ASSISTED_RULE,
    ].join('\n'),
  )
})

test('refuses a message that the header would push past Slack\'s limit', () => {
  assert.throws(() => withAssistHeader('x'.repeat(4000), 'Qwen3.8'), /Slack accepts 4000/)
  assert.doesNotThrow(() => withAssistHeader('x'.repeat(3000), 'Qwen3.8'))
})

test('nothing configured signs with the generated default', () => {
  assert.deepEqual(signingFor({}, {}), { kind: 'default' })
  assert.equal(
    resolveNickname(signingFor({}, {}), { agent: 'pi', user: 'carlos.ruiz' }),
    "Agent 'pi' of carlos.ruiz",
  )
})

test('the default names the client and the account, and copes without either', () => {
  assert.equal(defaultNickname('claude-code', 'carlos.ruiz'), "Agent 'claude-code' of carlos.ruiz")
  assert.equal(defaultNickname('pi'), "Agent 'pi'")
  assert.equal(defaultNickname(undefined, 'carlos.ruiz'), "Agent 'unknown' of carlos.ruiz")
})

test('the default cleans what mrkdwn cannot take and fits the nickname budget', () => {
  assert.equal(defaultNickname('a*b', 'c\nd'), "Agent 'a b' of c d")
  const long = defaultNickname('x'.repeat(80), 'y'.repeat(80))
  assert.ok(long.length <= 60, `expected at most 60 characters, got ${long.length}`)
  assert.doesNotMatch(long, /[*\n]/)
})

test('the settings file supplies the nickname', () => {
  assert.deepEqual(signingFor({ assisted_nickname: '  Qwen3.8  ' }, {}), {
    kind: 'nickname',
    nickname: 'Qwen3.8',
  })
})

test('the environment wins over the settings file', () => {
  const env = { [ASSISTED_NICKNAME_ENV]: 'Claude' }
  assert.deepEqual(resolveNickname(signingFor({ assisted_nickname: 'Qwen3.8' }, env), {}), 'Claude')
})

test('an empty environment variable turns signing off instead of falling back', () => {
  const env = { [ASSISTED_NICKNAME_ENV]: ' ' }
  assert.deepEqual(signingFor({ assisted_nickname: 'Qwen3.8' }, env), { kind: 'off' })
  assert.equal(resolveNickname(signingFor({}, env), { agent: 'pi', user: 'carlos.ruiz' }), null)
})

test('refuses an environment nickname that would break the header line', () => {
  for (const value of ['a*b', 'two\nlines', 'x'.repeat(61)]) {
    assert.throws(
      () => signingFor({}, { [ASSISTED_NICKNAME_ENV]: value }),
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

test('the three signing states are told apart in the report', () => {
  assert.equal(
    describeSigning({ kind: 'nickname', nickname: 'Qwen3.8' }),
    'assisted nickname: Qwen3.8',
  )
  assert.match(describeSigning({ kind: 'off' }), /carry no header/)
  assert.match(
    describeSigning({ kind: 'default' }, 'pi'),
    /\*Message Sent by AI Agent 'pi' of <the sending account>\*/,
  )
})
