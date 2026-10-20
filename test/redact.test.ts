import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { redact } from '../src/log/redact.ts'

describe('redact', () => {
  it('masks every token shape Slack issues', () => {
    const text =
      'browser xoxc-1234-5678 lookback xoxp-99-88 xoxd-abcdef%2B and xoxb-777'
    const result = redact(text)
    assert.ok(!result.includes('xoxc-1234'), result)
    assert.ok(!result.includes('xoxp-99'), result)
    assert.ok(!result.includes('xoxb-777'), result)
    assert.ok(!result.includes('abcdef'), result)
  })

  it('masks a token passed as a query or form parameter', () => {
    assert.equal(redact('POST /api/x?token=xoxc-secret&channel=C1'), 'POST /api/x?token=<redacted>&channel=C1')
    assert.equal(redact('token=xoxc-secret&channel=C1'), 'token=<redacted>&channel=C1')
  })

  it('masks bearer headers', () => {
    assert.equal(redact('Authorization: Bearer abcdef123456'), 'Authorization: Bearer <redacted>')
  })
})
