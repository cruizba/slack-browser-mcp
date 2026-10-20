import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import {
  DEFAULT_RECENT_AFTER_MINUTES,
  DEFAULT_STALE_AFTER_MINUTES,
  RECENT_AFTER_ENV,
  STALE_AFTER_ENV,
  describePolicy,
  freshnessPolicy,
  wantsRefresh,
} from '../src/config/freshness.ts'
import { StoreError } from '../src/errors.ts'

const MINUTE = 60_000
const HOUR = 60 * MINUTE

function withoutEnv(): NodeJS.ProcessEnv {
  return {}
}

describe('freshnessPolicy', () => {
  it('defaults to a day for any read and 15 minutes for the newest ones', () => {
    const policy = freshnessPolicy({}, withoutEnv())
    assert.equal(policy.staleAfterMs, DEFAULT_STALE_AFTER_MINUTES * MINUTE)
    assert.equal(policy.recentAfterMs, DEFAULT_RECENT_AFTER_MINUTES * MINUTE)
    assert.equal(policy.staleAfterMs, 24 * HOUR)
  })

  it('takes the thresholds from config.json', () => {
    const policy = freshnessPolicy(
      { stale_after_minutes: 90, recent_after_minutes: 2 },
      withoutEnv(),
    )
    assert.equal(policy.staleAfterMs, 90 * MINUTE)
    assert.equal(policy.recentAfterMs, 2 * MINUTE)
  })

  it('lets the environment win over config.json', () => {
    const policy = freshnessPolicy(
      { stale_after_minutes: 90, recent_after_minutes: 2 },
      { [STALE_AFTER_ENV]: '2880', [RECENT_AFTER_ENV]: ' 30 ' },
    )
    assert.equal(policy.staleAfterMs, 48 * HOUR)
    // Spreadsheet paste and hand-edited env files leave whitespace behind.
    assert.equal(policy.recentAfterMs, 30 * MINUTE)
  })

  it('names the variable when the value is not a positive number of minutes', () => {
    for (const value of ['soon', '0', '-5']) {
      assert.throws(
        () => freshnessPolicy({}, { [STALE_AFTER_ENV]: value }),
        (error: unknown) =>
          error instanceof StoreError &&
          error.message.includes(STALE_AFTER_ENV) &&
          error.message.includes(JSON.stringify(value)),
      )
    }
  })

  it('blames config.json when the file made the value unusable', () => {
    assert.throws(
      () => freshnessPolicy({ recent_after_minutes: 0 }, withoutEnv()),
      /config\.json.*positive number of minutes/,
    )
  })
})

describe('wantsRefresh', () => {
  const policy = freshnessPolicy({}, withoutEnv())

  it('asks for a sync when nothing is stored yet, whatever the read', () => {
    assert.equal(wantsRefresh(null, policy, false), true)
    assert.equal(wantsRefresh(null, policy, true), true)
  })

  it('leaves a history read alone until the archive is a day old', () => {
    assert.equal(wantsRefresh(1 * HOUR, policy, false), false)
    assert.equal(wantsRefresh(23 * HOUR + 59 * MINUTE, policy, false), false)
    assert.equal(wantsRefresh(24 * HOUR, policy, false), true)
    assert.equal(wantsRefresh(3 * 24 * HOUR, policy, false), true)
  })

  it('keeps a read that reaches the newest messages within minutes', () => {
    assert.equal(wantsRefresh(5 * MINUTE, policy, true), false)
    assert.equal(wantsRefresh(14 * MINUTE, policy, true), false)
    assert.equal(wantsRefresh(15 * MINUTE, policy, true), true)
    assert.equal(wantsRefresh(2 * HOUR, policy, true), true)
  })

  it('follows the configured values instead of the defaults', () => {
    const relaxed = freshnessPolicy({ stale_after_minutes: 2880, recent_after_minutes: 120 }, withoutEnv())
    assert.equal(wantsRefresh(25 * HOUR, relaxed, false), false)
    assert.equal(wantsRefresh(90 * MINUTE, relaxed, true), false)
  })
})

describe('describePolicy', () => {
  it('reads as a sentence the agent can forward', () => {
    const text = describePolicy(freshnessPolicy({}, withoutEnv()))
    assert.match(text, /older than 24 h/)
    assert.match(text, /15 min for a read that reaches the newest messages/)
  })
})
