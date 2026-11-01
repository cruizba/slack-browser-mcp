/**
 * The ladder of `docs/agent-forum-spec.md` §7.5 end to end: two identities on one
 * machine, the tools over the real MCP transport, and one archive they share. Slack
 * is the only fake, because it is the only thing a test cannot reach.
 */

import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { after, test } from 'node:test'

import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js'

import { decodeMarkdown, encode, summaryFor, type Envelope } from '../src/forum/envelope.ts'
import { newId } from '../src/forum/id.ts'

const root = await mkdtemp(join(tmpdir(), 'forum-workflow-'))
const configDir = join(root, 'config')
const dataDir = join(root, 'data')
process.env.SLACK_BROWSER_MCP_CONFIG_DIR = configDir
process.env.XDG_DATA_HOME = dataDir
process.env.SLACK_BROWSER_MCP_NO_PRESENCE = '1'
// The identity is the MCP client name here: an environment variable would collapse
// both agents into one.
delete process.env.SLACK_BROWSER_MCP_AGENT
delete process.env.SLACK_BROWSER_MCP_TEAM

const TEAM = 'T0TEST'
const CHANNEL = 'C0HUB'
const BUILDER = 'builder@laptop'
const REVIEWER = 'reviewer@desktop'

const { ArchiveStore } = await import('../src/archive/store.ts')
const { buildServer } = await import('../src/mcp/server.ts')
const { FileCredentialStore } = await import('../src/auth/store.ts')
const { forumFileName } = await import('../src/forum/carrier.ts')

after(() => rm(root, { recursive: true, force: true }))

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  })
}

function textOf(result: unknown): string {
  const content = (result as { content?: Array<{ type: string; text?: string }> }).content ?? []
  return content.map((part) => part.text ?? '').join('\n')
}

/** The archive *is* the channel here, which keeps the refresh and reconcile passes honest. */
function archived(): Array<{ ts: string; thread_ts?: string; text?: string; user?: string }> {
  const file = join(dataDir, 'slack-browser-mcp', 'archive', TEAM, `${CHANNEL}.jsonl`)
  if (!existsSync(file)) return []
  const byTs = new Map<string, { ts: string; thread_ts?: string; text?: string; user?: string }>()
  for (const line of readFileSync(file, 'utf8').split('\n')) {
    if (line.trim() === '') continue
    const record = JSON.parse(line) as {
      ts: string
      thread_ts?: string
      text?: string
      user?: string
      deleted_at?: string
    }
    if (record.deleted_at) continue
    byTs.set(record.ts, record)
  }
  return [...byTs.values()]
}

interface Harness {
  builder: Client
  reviewer: Client
  uploads: Envelope[]
  calls: string[]
}

let nextTs = Math.floor(Date.now() / 1000)

async function setup(t: { after(fn: () => void | Promise<void>): void }): Promise<Harness> {
  await rm(join(dataDir), { recursive: true, force: true })
  await mkdir(configDir, { recursive: true })
  await writeFile(
    join(configDir, 'forum.json'),
    JSON.stringify({
      version: 1,
      identities: {
        builder: { name: 'builder', host: 'laptop', capabilities: [], registered_at: '2026-11-01T00:00:00Z' },
        reviewer: { name: 'reviewer', host: 'desktop', capabilities: [], registered_at: '2026-11-01T00:00:00Z' },
      },
      default_identity: 'builder',
      channels: [
        {
          workspace: TEAM,
          channel: CHANNEL,
          name: 'agent-hub',
          private: true,
          since_ts: '0',
          added_at: '2026-11-01T00:00:00Z',
          sign: false,
          announce: false,
        },
      ],
      limits: {},
      cursors: {},
      ignored: [],
      posting: {},
    }),
  )

  const store = new FileCredentialStore(join(configDir, 'accounts'))
  await store.save({
    team_id: TEAM,
    team_name: 'Globex',
    team_domain: 'globex',
    user: 'ana',
    user_id: 'U0ANA',
    token: 'xoxc-test-token',
    d_cookie: 'xoxd-test%2Bcookie',
    d_cookie_name: 'd',
    saved_at: '2026-11-01T00:00:00.000Z',
  })

  const archive = new ArchiveStore(join(dataDir, 'slack-browser-mcp', 'archive'))
  const register = (from: string, name: string, host: string): Envelope => ({
    v: 1,
    kind: 'register',
    id: newId(),
    from,
    sent: '2026-11-01T00:00:00Z',
    name,
    host,
    capabilities: [],
  })
  await archive.append(TEAM, CHANNEL, [
    register(BUILDER, 'builder', 'laptop'),
    register(REVIEWER, 'reviewer', 'desktop'),
  ].map((envelope) => ({
    ts: `${(nextTs += 1)}.000100`,
    channel: CHANNEL,
    channel_name: 'agent-hub',
    user: 'U0ANA',
    text: summaryFor(envelope),
    files: [{ name: forumFileName(envelope.id), mimetype: 'text/markdown' }],
    forum: encode(envelope),
    synced_at: '2026-11-01T00:00:00Z',
  })))

  const uploads: Envelope[] = []
  const calls: string[] = []
  const originalFetch = globalThis.fetch
  ;(globalThis as { fetch: typeof fetch }).fetch = (async (
    input: string | URL | Request,
    init?: RequestInit,
  ) => {
    const target = String(input)
    if (target.startsWith('https://files.slack.com/')) {
      const blob = (init?.body as FormData).get('file') as Blob
      const envelope = decodeMarkdown(await blob.text())
      if (envelope) uploads.push(envelope)
      return new Response('OK', { status: 200 })
    }
    const method = target.split('/api/')[1] ?? ''
    calls.push(method)
    const body = new URLSearchParams(String(init?.body ?? ''))

    if (method === 'users.conversations') {
      return json({ ok: true, channels: [{ id: CHANNEL, name: 'agent-hub', is_private: true }] })
    }
    if (method === 'conversations.history') {
      const oldest = Number(body.get('oldest') ?? 0)
      const messages = archived()
        .filter((record) => !record.thread_ts && Number(record.ts) > oldest)
        .sort((left, right) => Number(right.ts) - Number(left.ts))
        .map((record) => ({
          ts: record.ts,
          ...(record.text !== undefined ? { text: record.text } : {}),
          ...(record.user ? { user: record.user } : {}),
        }))
      return json({ ok: true, messages })
    }
    if (method === 'conversations.replies') {
      const thread = String(body.get('ts'))
      const messages = archived()
        .filter((record) => record.ts === thread || record.thread_ts === thread)
        .map((record) => ({
          ts: record.ts,
          ...(record.thread_ts ? { thread_ts: record.thread_ts } : {}),
          ...(record.text !== undefined ? { text: record.text } : {}),
          ...(record.user ? { user: record.user } : {}),
        }))
      return json({ ok: true, messages })
    }
    if (method === 'files.getUploadURLExternal') {
      return json({
        ok: true,
        upload_url: 'https://files.slack.com/upload/v1/test',
        file_id: `F0TEST${uploads.length + 1}`,
      })
    }
    if (method === 'files.completeUploadExternal') {
      nextTs += 1
      const channel = String(body.get('channel_id'))
      const files = JSON.parse(String(body.get('files'))) as Array<{ id: string }>
      return json({
        ok: true,
        files: files.map((file) => ({
          id: file.id,
          shares: { private: { [channel]: [{ ts: `${nextTs}.000100` }] } },
        })),
      })
    }
    if (method === 'files.info') {
      return json({ ok: true, file: { id: String(body.get('file')) } })
    }
    return json({ ok: false, error: 'unexpected_method' })
  }) as typeof fetch
  t.after(() => {
    ;(globalThis as { fetch: typeof fetch }).fetch = originalFetch
  })

  const connect = async (name: string): Promise<Client> => {
    const server = buildServer()
    const client = new Client({ name, version: '0.0.0' })
    const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair()
    await Promise.all([server.connect(serverTransport), client.connect(clientTransport)])
    t.after(async () => {
      await client.close()
      await server.close()
    })
    return client
  }

  return { builder: await connect('builder'), reviewer: await connect('reviewer'), uploads, calls }
}

function taskNamed(uploads: Envelope[], title: string): string {
  const found = uploads.filter((envelope) => envelope.kind === 'task' && envelope.title === title)
  const last = found.at(-1)
  assert.ok(last, `no task called "${title}" was posted`)
  return last.id
}

/** The state `forum_task` prints for a task, as either agent sees it. */
async function stateOf(client: Client, id: string): Promise<string> {
  const text = textOf(await client.callTool({ name: 'forum_task', arguments: { id, offline: true } }))
  // The header is `<id>  <state>  <local decision>  <workspace>/<channel>`, and a
  // stalled task prints the marker glued to the state.
  const match = /^[A-Z0-9]{26}\s+([a-z-]+)/.exec(text.split('\n')[0] ?? '')
  assert.ok(match, `no state in:\n${text}`)
  return match[1]!
}

test('ask, claim, progress, report and ack, with the thread as the record', async (t) => {
  const { builder, reviewer, uploads } = await setup(t)

  const posted = textOf(
    await builder.callTool({
      name: 'forum_post',
      arguments: { to: REVIEWER, title: 'revisar el PR', body: 'mira el diff de sync.ts', confirm: true },
    }),
  )
  assert.match(posted, /Posted to private:agent-hub/)
  const task = taskNamed(uploads, 'revisar el PR')
  assert.equal(await stateOf(builder, task), 'open')

  const inbox = textOf(await reviewer.callTool({ name: 'forum_inbox', arguments: { offline: true } }))
  assert.match(inbox, /revisar el PR/)

  await reviewer.callTool({ name: 'forum_accept', arguments: { id: task, confirm: true } })
  assert.equal(await stateOf(builder, task), 'accepted')

  await reviewer.callTool({
    name: 'forum_progress',
    arguments: { id: task, body: 'voy por la mitad', confirm: true },
  })
  assert.equal(await stateOf(builder, task), 'accepted', 'a note never moves the state')

  await reviewer.callTool({
    name: 'forum_result',
    arguments: { id: task, body: 'listo, dos fallos de borde', confirm: true },
  })
  assert.equal(await stateOf(reviewer, task), 'done')

  await builder.callTool({ name: 'forum_ack', arguments: { id: task, confirm: true } })
  const closed = textOf(await builder.callTool({ name: 'forum_task', arguments: { id: task, offline: true } }))
  assert.equal(await stateOf(builder, task), 'acknowledged')
  assert.match(closed, /local:acknowledged/)
  for (const [kind, from] of [
    ['accept', REVIEWER],
    ['progress', REVIEWER],
    ['result', REVIEWER],
    ['ack', BUILDER],
  ] as const) {
    assert.match(closed, new RegExp(`${kind} from ${from}`), `the thread keeps the ${kind}`)
  }

  // Every move is a file in the same thread, told apart by its envelope.
  const replies = uploads.filter((envelope) => envelope.kind !== 'task' && 'task' in envelope)
  assert.deepEqual(
    replies.map((envelope) => envelope.kind),
    ['accept', 'progress', 'result', 'ack'],
  )
})

test('decline, cancel, a failed result and a reopen', async (t) => {
  const { builder, reviewer, uploads } = await setup(t)

  const declined = textOf(
    await builder.callTool({
      name: 'forum_post',
      arguments: { to: REVIEWER, title: 'no lo voy a hacer', body: 'devuélvemelo', confirm: true },
    }),
  )
  assert.match(declined, /Posted to/)
  const sayNo = taskNamed(uploads, 'no lo voy a hacer')
  await reviewer.callTool({
    name: 'forum_decline',
    arguments: { id: sayNo, body: 'no me da tiempo', confirm: true },
  })
  assert.equal(await stateOf(builder, sayNo), 'declined')

  await builder.callTool({
    name: 'forum_post',
    arguments: { to: REVIEWER, title: 'esto se cancela', body: 'olvídalo', confirm: true },
  })
  const cancelled = taskNamed(uploads, 'esto se cancela')
  await reviewer.callTool({ name: 'forum_accept', arguments: { id: cancelled, confirm: true } })
  await builder.callTool({
    name: 'forum_cancel',
    arguments: { id: cancelled, body: 'al final no', confirm: true },
  })
  assert.equal(await stateOf(reviewer, cancelled), 'cancelled')

  await builder.callTool({
    name: 'forum_post',
    arguments: { to: REVIEWER, title: 'con dos pasadas', body: 'hazlo bien', confirm: true },
  })
  const twice = taskNamed(uploads, 'con dos pasadas')
  await reviewer.callTool({ name: 'forum_accept', arguments: { id: twice, confirm: true } })
  await reviewer.callTool({
    name: 'forum_result',
    arguments: { id: twice, body: 'me faltó la mitad', ok: false, confirm: true },
  })
  assert.equal(await stateOf(builder, twice), 'failed')

  const reopened = textOf(
    await builder.callTool({
      name: 'forum_reopen',
      arguments: { id: twice, body: 'falta el otro fichero', confirm: true },
    }),
  )
  assert.match(reopened, /Posted to/)
  assert.equal(await stateOf(reviewer, twice), 'accepted')

  await reviewer.callTool({
    name: 'forum_result',
    arguments: { id: twice, body: 'ahora sí', confirm: true },
  })
  assert.equal(await stateOf(builder, twice), 'done')
})

test('a task says what kind of work it is', async (t) => {
  const { builder, reviewer, uploads } = await setup(t)

  await builder.callTool({
    name: 'forum_post',
    arguments: {
      to: REVIEWER,
      title: 'revisar el PR 12',
      body: 'el diff está en el archivo',
      capability: 'Review',
      confirm: true,
    },
  })

  const envelope = uploads.at(-1)
  assert.equal(envelope?.kind === 'task' ? envelope.capability : undefined, 'review')
  const inbox = textOf(await reviewer.callTool({ name: 'forum_inbox', arguments: { offline: true } }))
  assert.match(inbox, /\[review\] revisar el PR 12/)

  const task = textOf(
    await reviewer.callTool({
      name: 'forum_task',
      arguments: { id: taskNamed(uploads, 'revisar el PR 12'), offline: true },
    }),
  )
  assert.match(task, /needs review/)
})

test('registering with capabilities announces them', async (t) => {
  const { uploads } = await setup(t)
  const file = join(configDir, 'forum.json')
  const forum = JSON.parse(readFileSync(file, 'utf8')) as { channels: Array<{ announce: boolean }> }
  forum.channels[0]!.announce = true
  await writeFile(file, JSON.stringify(forum))

  const { openForum } = await import('../src/forum/context.ts')
  const { announce, registerIdentity, transportFor } = await import('../src/forum/service.ts')
  const { parseCapabilities } = await import('../src/forum/identity.ts')

  const context = await openForum()
  const identity = { name: 'builder', host: 'laptop', capabilities: parseCapabilities('review,triage') }

  // The roster in the archive says builder accepts nothing, so this is a change.
  assert.equal((await registerIdentity(context, identity)).announce, true)
  const posted = await announce(
    context,
    async (teamId) => transportFor(await new FileCredentialStore(join(configDir, 'accounts')).get(teamId)),
    identity,
  )
  assert.equal(posted.posted, 1)
  const envelope = uploads.at(-1)
  assert.deepEqual(envelope?.kind === 'register' ? envelope.capabilities : undefined, [
    'review',
    'triage',
  ])
  assert.equal(context.identity?.capabilities.length, 0, 'the old context is a snapshot')
})
