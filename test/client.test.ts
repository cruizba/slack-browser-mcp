import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import type { SlackAccount } from '../src/auth/store.ts'
import { AuthError, SlackApiError } from '../src/errors.ts'
import { SlackClient } from '../src/slack/client.ts'

const account: SlackAccount = {
  team_id: 'T01OPENVIDU',
  team_name: 'OpenVidu',
  token: 'xoxc-1234567890-abcdef',
  d_cookie: 'xoxd-abcdef%2Bghij',
  d_cookie_name: 'd',
  saved_at: '2026-10-20T15:00:00.000Z',
}

function respond(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'content-type': 'application/json' },
    ...init,
  })
}

function recordingFetch(responses: Array<() => Response>) {
  const calls: Array<{ url: string; init: RequestInit }> = []
  const fetchImpl = (async (input: unknown, init: RequestInit) => {
    calls.push({ url: String(input), init })
    const next = responses.shift()
    if (!next) throw new Error('unexpected fetch call')
    return next()
  }) as unknown as typeof fetch
  return { fetchImpl, calls }
}

describe('SlackClient', () => {
  it('sends the xoxc token as a form field and the d cookie as a header', async () => {
    const { fetchImpl, calls } = recordingFetch([
      () => respond({ ok: true, team: 'OpenVidu', team_id: 'T01', user: 'carlos', user_id: 'U01' }),
    ])
    const client = new SlackClient(account, { fetch: fetchImpl })
    await client.authTest()

    const [call] = calls
    assert.equal(call?.url, 'https://slack.com/api/auth.test')
    const body = new URLSearchParams(String(call?.init.body))
    assert.equal(body.get('token'), 'xoxc-1234567890-abcdef')
    const headers = call?.init.headers as Record<string, string>
    assert.equal(headers.Cookie, 'd=xoxd-abcdef%2Bghij')
    assert.equal(headers['Content-Type'], 'application/x-www-form-urlencoded')
  })

  it('parses auth.test into the workspace identity', async () => {
    const { fetchImpl } = recordingFetch([
      () => respond({ ok: true, team: 'OpenVidu', team_id: 'T01', user: 'carlos', user_id: 'U01' }),
    ])
    const client = new SlackClient(account, { fetch: fetchImpl })
    assert.deepEqual(await client.authTest(), {
      team: 'OpenVidu',
      team_id: 'T01',
      user: 'carlos',
      user_id: 'U01',
    })
  })

  it('turns invalid_auth into an AuthError that asks for a new login', async () => {
    const { fetchImpl } = recordingFetch([() => respond({ ok: false, error: 'invalid_auth' })])
    const client = new SlackClient(account, { fetch: fetchImpl })
    await assert.rejects(
      () => client.authTest(),
      (error: unknown) => error instanceof AuthError && /invalid_auth/.test(error.hint + error.message),
    )
  })

  it('reports other Slack errors with their code', async () => {
    const { fetchImpl } = recordingFetch([() => respond({ ok: false, error: 'channel_not_found' })])
    const client = new SlackClient(account, { fetch: fetchImpl })
    await assert.rejects(
      () => client.history({ channel: 'C404' }),
      (error: unknown) => error instanceof SlackApiError && error.code === 'channel_not_found',
    )
  })

  it('honours retry-after on 429 and succeeds on the next attempt', async () => {
    const waits: number[] = []
    const { fetchImpl, calls } = recordingFetch([
      () => respond({ ok: false, error: 'ratelimited' }, { status: 429, headers: { 'retry-after': '1' } }),
      () => respond({ ok: true, team: 'OpenVidu', team_id: 'T01', user: 'carlos', user_id: 'U01' }),
    ])
    const client = new SlackClient(account, {
      fetch: fetchImpl,
      sleep: async (ms) => {
        waits.push(ms)
      },
    })
    await client.authTest()
    assert.deepEqual(waits, [1000])
    assert.equal(calls.length, 2)
  })

  it('resolves a channel name against the user conversations', async () => {
    const { fetchImpl, calls } = recordingFetch([
      () => respond({ ok: true, channels: [{ id: 'C01', name: 'general', num_members: 12, is_private: false }] }),
    ])
    const client = new SlackClient(account, { fetch: fetchImpl })
    const channel = await client.resolveChannel('#general')
    assert.equal(channel.id, 'C01')
    const body = new URLSearchParams(String(calls[0]?.init.body))
    assert.equal(body.get('types'), 'public_channel,private_channel,mpim,im')
  })

  it('passes a channel id through without listing channels', async () => {
    const { fetchImpl, calls } = recordingFetch([])
    const client = new SlackClient(account, { fetch: fetchImpl })
    const channel = await client.resolveChannel('C0ABCDEF123')
    assert.equal(channel.id, 'C0ABCDEF123')
    assert.equal(calls.length, 0)
  })
})
