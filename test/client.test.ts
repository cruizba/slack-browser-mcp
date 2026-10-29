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

  it('takes the id from the label a listing printed', async () => {
    const { fetchImpl, calls } = recordingFetch([])
    const client = new SlackClient(account, { fetch: fetchImpl })
    const channel = await client.resolveChannel('dm:Ana Ruiz (D01ANARUIZ)')
    assert.equal(channel.id, 'D01ANARUIZ')
    assert.equal(calls.length, 0)
  })

  it('resolves a DM from the person it is with', async () => {
    const { fetchImpl } = recordingFetch([
      () =>
        respond({
          ok: true,
          channels: [
            { id: 'C01GENERAL', name: 'general' },
            { id: 'D01ANARUIZ', is_im: true, user: 'U02ANARUIZ' },
          ],
        }),
    ])
    const client = new SlackClient(account, { fetch: fetchImpl })
    const channel = await client.resolveChannel('dm:Ana Ruiz', {
      person: (reference) => (reference === 'Ana Ruiz' ? 'U02ANARUIZ' : undefined),
    })
    assert.equal(channel.id, 'D01ANARUIZ')
    assert.equal(channel.is_im, true)
  })

  it('says so when the person has no open DM', async () => {
    const { fetchImpl } = recordingFetch([
      () => respond({ ok: true, channels: [{ id: 'C01GENERAL', name: 'general' }] }),
    ])
    const client = new SlackClient(account, { fetch: fetchImpl })
    await assert.rejects(
      () => client.resolveChannel('@ana', { person: () => 'U02ANARUIZ' }),
      /No open direct message with ana/,
    )
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
    const { bytes, contentType } = await client.fetchAttachment(
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
      () => client.fetchAttachment('https://files.slack.com/files-pri/T01-F0ABC/foto.png'),
      (error: unknown) => error instanceof SlackApiError && error.code === 'http_403',
    )
  })

  it('uploads files to a pre-signed URL and posts them as one message', async () => {
    const calls: Array<{ url: string; init: RequestInit }> = []
    const fetchImpl = (async (input: unknown, init: RequestInit) => {
      const url = String(input)
      calls.push({ url, init })
      if (url.endsWith('files.getUploadURLExternal')) {
        const filename = new URLSearchParams(String(init.body)).get('filename')
        return respond({ ok: true, upload_url: `https://files.slack.com/upload/v1/${filename}`, file_id: `F0${filename === 'informe.md' ? 'A' : 'B'}` })
      }
      if (url.startsWith('https://files.slack.com/upload/')) return new Response('OK', { status: 200 })
      return respond({
        ok: true,
        files: [
          { id: 'F0A', shares: { private: { C0OPS: [{ ts: '1792500000.000200' }] } } },
          { id: 'F0B', shares: { private: { C0OPS: [{ ts: '1792500000.000200' }] } } },
        ],
      })
    }) as unknown as typeof fetch

    const client = new SlackClient(account, { fetch: fetchImpl })
    const uploaded = await client.uploadFiles({
      channel: 'C0OPS',
      text: 'el informe',
      thread_ts: '1792499999.000001',
      files: [
        { name: 'informe.md', bytes: new Uint8Array([35, 32, 104, 105]) },
        { name: 'captura.png', bytes: new Uint8Array([1, 2, 3]) },
      ],
    })

    assert.deepEqual(uploaded, { ts: '1792500000.000200', fileIds: ['F0A', 'F0B'] })
    const lengths = calls
      .filter((call) => call.url.endsWith('files.getUploadURLExternal'))
      .map((call) => new URLSearchParams(String(call.init.body)).get('length'))
    assert.deepEqual(lengths, ['4', '3'], 'Slack is told the exact size of each file')

    const puts = calls.filter((call) => call.url.startsWith('https://files.slack.com/upload/'))
    assert.equal(puts.length, 2)
    const put = puts[0]!
    assert.equal(
      put.init.headers,
      undefined,
      'the pre-signed URL needs no session: the token must not leave slack.com/api',
    )
    const form = put.init.body as FormData
    assert.equal(form.get('filename'), 'informe.md')
    assert.equal(await (form.get('file') as File).text(), '# hi')

    const completion = calls.at(-1)!
    assert.equal(completion.url, 'https://slack.com/api/files.completeUploadExternal')
    const body = new URLSearchParams(String(completion.init.body))
    assert.deepEqual(JSON.parse(String(body.get('files'))), [
      { id: 'F0A', title: 'informe.md' },
      { id: 'F0B', title: 'captura.png' },
    ])
    assert.equal(body.get('channel_id'), 'C0OPS')
    assert.equal(body.get('initial_comment'), 'el informe')
    assert.equal(body.get('thread_ts'), '1792499999.000001')
  })

  it('reports an upload that the file host refuses', async () => {
    const methods: string[] = []
    const fetchImpl = (async (input: unknown) => {
      const url = String(input)
      methods.push(url)
      if (url.endsWith('files.getUploadURLExternal')) {
        return respond({ ok: true, upload_url: 'https://files.slack.com/upload/v1/x', file_id: 'F0A' })
      }
      return new Response('nope', { status: 500 })
    }) as unknown as typeof fetch

    const client = new SlackClient(account, { fetch: fetchImpl })
    await assert.rejects(
      () => client.uploadFiles({ channel: 'C0OPS', files: [{ name: 'x.md', bytes: new Uint8Array([1]) }] }),
      (error: unknown) => error instanceof SlackApiError && error.code === 'http_500',
    )
    assert.equal(
      methods.some((url) => url.endsWith('files.completeUploadExternal')),
      false,
      'a failed upload must not post an empty message',
    )
  })

  it('spends the burst at once and then paces one request per interval', async () => {
    const waits: number[] = []
    const { fetchImpl, calls } = recordingFetch(
      Array.from({ length: 5 }, () => () =>
        respond({ ok: true, team: 'Acme', team_id: 'T01', user: 'ana', user_id: 'U01' }),
      ),
    )
    const client = new SlackClient(account, {
      fetch: fetchImpl,
      paceMs: 100,
      burst: 2,
      sleep: async (ms) => {
        waits.push(ms)
      },
    })
    for (let index = 0; index < 5; index += 1) await client.authTest()

    assert.equal(calls.length, 5)
    assert.equal(waits.length, 3)
    for (const wait of waits) assert.ok(wait > 0 && wait <= 120, `paced ${wait} ms`)
  })

  it('sends requests back to back when pacing is off', async () => {
    const waits: number[] = []
    const { fetchImpl, calls } = recordingFetch(
      Array.from({ length: 4 }, () => () =>
        respond({ ok: true, team: 'Acme', team_id: 'T01', user: 'ana', user_id: 'U01' }),
      ),
    )
    const client = new SlackClient(account, {
      fetch: fetchImpl,
      paceMs: 0,
      sleep: async (ms) => {
        waits.push(ms)
      },
    })
    for (let index = 0; index < 4; index += 1) await client.authTest()

    assert.equal(calls.length, 4)
    assert.deepEqual(waits, [])
  })

  it('stops asking users.info after a bounded number of authors', async () => {
    const { fetchImpl, calls } = recordingFetch(
      Array.from({ length: 40 }, (_, index) => () =>
        respond({ ok: true, user: { id: `U${index}`, name: `user${index}` } }),
      ),
    )
    const client = new SlackClient(account, { fetch: fetchImpl })
    const messages = Array.from({ length: 30 }, (_, index) => ({
      ts: `17925000${index}.000100`,
      user: `U${index}`,
    }))

    const names = await client.namesFor(messages)

    assert.equal(calls.length, 25, 'one page of messages must not cost one request per author')
    assert.equal(names.size, 25)
  })

  it('costs nothing when the directory already knows the authors', async () => {
    const { fetchImpl, calls } = recordingFetch([])
    const client = new SlackClient(account, { fetch: fetchImpl })
    const messages = Array.from({ length: 30 }, (_, index) => ({
      ts: `17925000${index}.000100`,
      user: `U${index}`,
    }))

    const names = await client.namesFor(messages, (id) => `name-${id}`)

    assert.equal(calls.length, 0)
    assert.equal(names.size, 30)
  })

  it('parses search.messages into a page of matches', async () => {
    const { fetchImpl, calls } = recordingFetch([
      () =>
        respond({
          ok: true,
          messages: {
            total: 42,
            matches: [
              {
                ts: '1792500000.000100',
                text: 'el mtu del vxlan',
                user: 'U0AUTHOR',
                username: 'Ana',
                channel: { id: 'C0OPS', name: 'operations' },
              },
            ],
          },
        }),
    ])
    const client = new SlackClient(account, { fetch: fetchImpl })
    const page = await client.searchMessages({ query: 'vxlan', count: 5 })

    assert.equal(page.total, 42)
    assert.equal(page.matches.length, 1)
    assert.equal(page.matches[0]!.text, 'el mtu del vxlan')
    assert.equal(page.matches[0]!.channel?.name, 'operations')
    const body = new URLSearchParams(String(calls[0]?.init.body))
    assert.equal(body.get('query'), 'vxlan')
    assert.equal(body.get('count'), '5')
  })
})
