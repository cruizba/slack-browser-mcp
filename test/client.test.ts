import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import type { SlackAccount } from '../src/auth/store.ts'
import { AuthError, SlackApiError } from '../src/errors.ts'
import { SlackClient } from '../src/slack/client.ts'

const account: SlackAccount = {
  team_id: 'T01ACME',
  team_name: 'Acme',
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
      () => respond({ ok: true, team: 'Acme', team_id: 'T01', user: 'ana', user_id: 'U01' }),
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
      () => respond({ ok: true, team: 'Acme', team_id: 'T01', user: 'ana', user_id: 'U01' }),
    ])
    const client = new SlackClient(account, { fetch: fetchImpl })
    assert.deepEqual(await client.authTest(), {
      team: 'Acme',
      team_id: 'T01',
      user: 'ana',
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
      () => respond({ ok: true, team: 'Acme', team_id: 'T01', user: 'ana', user_id: 'U01' }),
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

  it('flattens the profile when listing users for the directory', async () => {
    const { fetchImpl } = recordingFetch([
      () =>
        respond({
          ok: true,
          members: [
            {
              id: 'U02ANA',
              name: 'ana',
              real_name: 'Mica Test',
              is_bot: false,
              profile: { display_name: 'ana' },
            },
          ],
        }),
    ])
    const client = new SlackClient(account, { fetch: fetchImpl })
    const [user] = await client.listUsers()
    assert.deepEqual(user, {
      id: 'U02ANA',
      name: 'ana',
      real_name: 'Mica Test',
      display_name: 'ana',
      is_bot: false,
    })
  })

  it('downloads an attachment with the same credentials as the API', async () => {
    const calls: Array<{ url: string; init: RequestInit }> = []
    const fetchImpl = (async (input: unknown, init: RequestInit) => {
      calls.push({ url: String(input), init })
      return new Response(new Uint8Array([1, 2, 3]), {
        status: 200,
        headers: { 'content-type': 'image/png' },
      })
    }) as unknown as typeof fetch

    const client = new SlackClient(account, { fetch: fetchImpl })
    const { bytes, contentType } = await client.downloadFile(
      'https://files.slack.com/files-pri/T01-F0ABC/foto.png',
    )
    assert.deepEqual([...bytes], [1, 2, 3])
    assert.equal(contentType, 'image/png')
    const headers = calls[0]?.init.headers as Record<string, string>
    assert.equal(headers.Authorization, 'Bearer xoxc-1234567890-abcdef')
    assert.equal(headers.Cookie, 'd=xoxd-abcdef%2Bghij')
  })

  it('refuses a download that Slack rejects', async () => {
    const fetchImpl = (async () =>
      new Response('nope', { status: 403 })) as unknown as typeof fetch
    const client = new SlackClient(account, { fetch: fetchImpl })
    await assert.rejects(
      () => client.downloadFile('https://files.slack.com/files-pri/T01-F0ABC/foto.png'),
      (error: unknown) => error instanceof SlackApiError && error.code === 'http_403',
    )
  })
})
