import assert from 'node:assert/strict'
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js'

import { render, summaryFor, type Envelope } from '../src/forum/envelope.ts'
import { newId } from '../src/forum/id.ts'

const TASK = '01J9Z7K3QX4MB8WN2R5T6Y7Z8A'
const TASK_TS = '1793145900.000300'

// The modules under test resolve their paths at import time, so every test in this
// file shares one root and starts from a rewritten file instead of a new directory.
const root = await mkdtemp(join(tmpdir(), 'forum-write-'))
const configDir = join(root, 'config')
process.env.SLACK_BROWSER_MCP_CONFIG_DIR = configDir
process.env.XDG_DATA_HOME = join(root, 'data')
process.env.SLACK_BROWSER_MCP_AGENT = 'pi'
process.env.SLACK_BROWSER_MCP_NO_PRESENCE = '1'

function textOf(result: unknown): string {
  const content = (result as { content?: Array<{ type: string; text?: string }> }).content ?? []
  return content.map((part) => part.text ?? '').join('\n')
}

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  })
}

async function writeForum(limits: Record<string, number> = {}): Promise<void> {
  await mkdir(configDir, { recursive: true })
  await writeFile(
    join(configDir, 'forum.json'),
    JSON.stringify({
      version: 1,
      identities: {
        pi: {
          name: 'reviewer',
          host: 'laptop',
          capabilities: ['review'],
          registered_at: '2026-10-28T09:00:00Z',
        },
      },
      default_identity: 'pi',
      channels: [
        {
          workspace: 'T0TEST',
          channel: 'C0HUB',
          name: 'agent-hub',
          private: true,
          since_ts: '0',
          added_at: '2026-10-28T09:00:00Z',
          sign: true,
        },
      ],
      limits,
      cursors: {},
      ignored: [],
      posting: {},
    }),
  )
}

async function writeAccount(): Promise<void> {
  const { FileCredentialStore } = await import('../src/auth/store.ts')
  await new FileCredentialStore(join(configDir, 'accounts')).save({
    team_id: 'T0TEST',
    team_name: 'Globex',
    team_domain: 'globex',
    user: 'reviewer.acc',
    user_id: 'U0REVIEWER',
    token: 'xoxc-test-token',
    d_cookie: 'xoxd-test%2Bcookie',
    d_cookie_name: 'd',
    saved_at: new Date().toISOString(),
  })
}

async function seedArchive(): Promise<void> {
  const { ArchiveStore } = await import('../src/archive/store.ts')
  const archive = new ArchiveStore(join(root, 'data', 'slack-browser-mcp', 'archive'))
  const record = (ts: string, envelope: Envelope) => ({
    ts,
    channel: 'C0HUB',
    channel_name: 'agent-hub',
    text: render(summaryFor(envelope), envelope),
    synced_at: '2026-10-28T09:30:00Z',
  })
  await archive.append('T0TEST', 'C0HUB', [
    record('1793145600.000100', {
      v: 1,
      kind: 'register',
      id: newId(),
      from: 'builder@desktop',
      sent: '2026-10-28T09:00:00Z',
      name: 'builder',
      host: 'desktop',
      capabilities: ['code'],
    }),
    record('1793145600.000200', {
      v: 1,
      kind: 'register',
      id: newId(),
      from: 'reviewer@laptop',
      sent: '2026-10-28T09:00:00Z',
      name: 'reviewer',
      host: 'laptop',
      capabilities: ['review'],
    }),
    record(TASK_TS, {
      v: 1,
      kind: 'task',
      id: TASK,
      from: 'builder@desktop',
      to: 'reviewer@laptop',
      sent: '2026-10-28T09:05:00Z',
      title: 'revisar el PR',
      body: 'mira el diff',
    }),
  ])
}

interface Harness {
  calls: Array<{ method: string; body: URLSearchParams }>
  client: Client
}

async function setup(
  t: { after(fn: () => void | Promise<void>): void },
  limits: Record<string, number> = {},
): Promise<Harness> {
  await rm(join(root, 'data'), { recursive: true, force: true })
  await writeForum(limits)
  await writeAccount()
  await seedArchive()

  const calls: Array<{ method: string; body: URLSearchParams }> = []
  const originalFetch = globalThis.fetch
  ;(globalThis as { fetch: typeof fetch }).fetch = (async (
    input: string | URL | Request,
    init?: RequestInit,
  ) => {
    const method = String(input).split('/api/')[1] ?? ''
    const body = new URLSearchParams(String(init?.body ?? ''))
    calls.push({ method, body })
    if (method === 'users.conversations') {
      return json({ ok: true, channels: [{ id: 'C0HUB', name: 'agent-hub', is_private: true }] })
    }
    if (method === 'conversations.history') {
      return json({ ok: true, messages: [{ ts: TASK_TS, text: 'x' }] })
    }
    if (method === 'chat.postMessage') {
      return json({
        ok: true,
        channel: body.get('channel'),
        ts: '1793149000.000100',
        message: { text: body.get('text') },
      })
    }
    return json({ ok: false, error: 'unexpected_method' })
  }) as typeof fetch
  t.after(() => {
    ;(globalThis as { fetch: typeof fetch }).fetch = originalFetch
  })

  const { buildServer } = await import('../src/mcp/server.ts')
  const server = buildServer()
  const client = new Client({ name: 'pi', version: '0.0.0' })
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair()
  await Promise.all([server.connect(serverTransport), client.connect(clientTransport)])
  t.after(async () => {
    await client.close()
    await server.close()
  })

  return { calls, client }
}

function posts(calls: Harness['calls']) {
  return calls.filter((call) => call.method === 'chat.postMessage')
}

test('forum_post is a dry run until confirm, then posts once and audits it', async (t) => {
  const { calls, client } = await setup(t)

  const preview = await client.callTool({
    name: 'forum_post',
    arguments: { to: 'reviewer@laptop', title: 'devuelve el favor', body: 'revisa esto' },
  })
  const previewText = textOf(preview)
  assert.match(previewText, /Nothing sent/)
  assert.match(previewText, /```forum/)
  assert.equal(posts(calls).length, 0, 'a preview must not send')

  const posted = await client.callTool({
    name: 'forum_post',
    arguments: {
      to: 'reviewer@laptop',
      title: 'devuelve el favor',
      body: 'revisa esto',
      confirm: true,
    },
  })
  assert.match(textOf(posted), /Posted to private:agent-hub at ts/)
  const sends = posts(calls)
  assert.equal(sends.length, 1)
  assert.equal(sends[0]?.body.get('channel'), 'C0HUB')
  assert.match(String(sends[0]?.body.get('text')), /```forum/)

  const audit = await readFile(join(root, 'data', 'slack-browser-mcp', 'audit.jsonl'), 'utf8')
  assert.match(audit, /agent-hub/)
})

test('a peer that is not in the roster is refused instead of guessed', async (t) => {
  const { calls, client } = await setup(t)
  const result = await client.callTool({
    name: 'forum_post',
    arguments: { to: 'ghost@nowhere', title: 'hola', body: 'hay alguien', confirm: true },
  })
  assert.match(textOf(result), /No peer/)
  assert.equal(posts(calls).length, 0)
})

test('forum_accept claims in the task thread and records the agent decision', async (t) => {
  const { calls, client } = await setup(t)

  const preview = await client.callTool({
    name: 'forum_accept',
    arguments: { id: TASK.slice(0, 8) },
  })
  assert.match(textOf(preview), /Nothing sent/)
  assert.equal(posts(calls).length, 0)

  const accepted = await client.callTool({
    name: 'forum_accept',
    arguments: { id: TASK, confirm: true },
  })
  assert.match(textOf(accepted), /Posted to private:agent-hub/)
  const send = posts(calls)[0]
  assert.equal(send?.body.get('thread_ts'), TASK_TS, 'the accept belongs in the task thread')

  // The poster's own archive knows at once: `accept` then `result` in the same
  // session must not come back "still open" for lack of a sync.
  const after = await client.callTool({
    name: 'forum_task',
    arguments: { id: TASK, offline: true },
  })
  assert.match(textOf(after), /accepted/, 'the local state has to move without a sync')

  const decisions = await readFile(
    join(root, 'data', 'slack-browser-mcp', 'forum', 'T0TEST', 'C0HUB.jsonl'),
    'utf8',
  )
  const line = JSON.parse(decisions.trim().split('\n').at(-1)!) as Record<string, unknown>
  assert.equal(line.decision, 'approved')
  assert.equal(line.by, 'agent')
  assert.equal(line.self_approved, true, 'an agent approval must stay visible')
})

test('a second move is refused with the state instead of posting again', async (t) => {
  const { calls, client } = await setup(t)
  await client.callTool({ name: 'forum_accept', arguments: { id: TASK, confirm: true } })
  const again = await client.callTool({ name: 'forum_ack', arguments: { id: TASK, confirm: true } })
  assert.match(textOf(again), /is accepted|only a reported task/i)
  assert.equal(posts(calls).length, 1)
})

test('registering announces the peer name in the bound channel', async (t) => {
  const { calls } = await setup(t)
  const { openForum } = await import('../src/forum/context.ts')
  const { registerIdentity, announce, transportFor } = await import('../src/forum/service.ts')
  const { FileCredentialStore } = await import('../src/auth/store.ts')

  const context = await openForum()
  const identity = { name: 'analyst', host: 'desktop', capabilities: ['docs'] }
  const registered = await registerIdentity(context, identity)
  assert.equal(registered.announce, true, 'a new name has to be announced to be discoverable')

  const announced = await announce(
    context,
    async (teamId) => transportFor(await new FileCredentialStore().get(teamId)),
    identity,
  )
  assert.equal(announced.posted, 1)

  const send = posts(calls)[0]
  assert.equal(send?.body.get('channel'), 'C0HUB')
  assert.match(String(send?.body.get('text')), /analyst@desktop joined the forum/)
  assert.match(String(send?.body.get('text')), /"kind":"register"/)
})

test('presence is announced once, then throttled by the local mark', async (t) => {
  const { calls } = await setup(t)
  const { openForum } = await import('../src/forum/context.ts')
  const { announcePresence, transportFor } = await import('../src/forum/service.ts')
  const { FileCredentialStore } = await import('../src/auth/store.ts')
  const resolver = async (teamId: string) =>
    transportFor(await new FileCredentialStore().get(teamId))

  delete process.env.SLACK_BROWSER_MCP_NO_PRESENCE
  t.after(() => {
    process.env.SLACK_BROWSER_MCP_NO_PRESENCE = '1'
  })

  const first = await announcePresence(await openForum(), resolver, { force: true })
  assert.equal(first.posted, 1)
  assert.match(String(posts(calls)[0]?.body.get('text')), /"kind":"presence"/)
  assert.match(String(posts(calls)[0]?.body.get('text')), /"state":"idle"/)

  const second = await announcePresence(await openForum(), resolver)
  assert.equal(second.posted, 0, 'a fresh mark keeps the channel quiet')
  assert.equal(posts(calls).length, 1)
  assert.equal(
    calls.filter((call) => call.method === 'chat.postMessage').length,
    1,
    'presence is not a task post',
  )
})

test('the hourly limit refuses the post before it reaches Slack', async (t) => {
  const { calls, client } = await setup(t, { posts_per_hour: 1 })

  const first = await client.callTool({
    name: 'forum_post',
    arguments: { to: 'reviewer@laptop', title: 'uno', body: 'a', confirm: true },
  })
  assert.match(textOf(first), /Posted to/)

  const second = await client.callTool({
    name: 'forum_post',
    arguments: { to: 'reviewer@laptop', title: 'dos', body: 'b', confirm: true },
  })
  assert.match(textOf(second), /posts_per_hour 1 reached/)
  assert.equal(posts(calls).length, 1)
})
