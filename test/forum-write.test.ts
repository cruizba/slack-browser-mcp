import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js'

import { render, summaryFor, type Envelope } from '../src/forum/envelope.ts'
import { newId } from '../src/forum/id.ts'

const TASK = '01J9Z7K3QX4MB8WN2R5T6Y7Z8A'
// Anchored to the clock: the ledger derives a task's expiry from the envelope's
// `sent`, so a fixed date turns it expired once enough real time passes.
const NOW_SECONDS = Math.floor(Date.now() / 1000)
const TASK_TS = `${NOW_SECONDS - 3600}.000300`
const PEER_TS = `${NOW_SECONDS - 7200}`
const iso = (secondsAgo: number): string => new Date(Date.now() - secondsAgo * 1000).toISOString()
let NEXT_POST_TS = NOW_SECONDS + 10

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

function archived(): Array<{ ts: string; text?: string; user?: string }> {
  const file = join(root, 'data', 'slack-browser-mcp', 'archive', 'T0TEST', 'C0HUB.jsonl')
  if (!existsSync(file)) return []
  const byTs = new Map<string, { ts: string; text?: string; user?: string }>()
  for (const line of readFileSync(file, 'utf8').split('\n')) {
    if (line.trim() === '') continue
    const record = JSON.parse(line) as { ts: string; text?: string; user?: string; deleted_at?: string }
    if (record.deleted_at) continue
    byTs.set(record.ts, record)
  }
  return [...byTs.values()]
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
          sign: false,
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
    record(`${PEER_TS}.000100`, {
      v: 1,
      kind: 'register',
      id: newId(),
      from: 'builder@desktop',
      sent: iso(7200),
      name: 'builder',
      host: 'desktop',
      capabilities: ['code'],
    }),
    record(`${PEER_TS}.000200`, {
      v: 1,
      kind: 'register',
      id: newId(),
      from: 'reviewer@laptop',
      sent: iso(7200),
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
      sent: iso(3600),
      title: 'revisar el PR',
      body: 'mira el diff',
    }),
  ])
}

interface Harness {
  calls: Array<{ method: string; body: URLSearchParams }>
  /** Bytes of every `forum-*.json` that reached the pre-signed upload URL. */
  uploads: Array<{ name: string; json: string }>
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
  const uploads: Array<{ name: string; json: string }> = []
  const originalFetch = globalThis.fetch
  ;(globalThis as { fetch: typeof fetch }).fetch = (async (
    input: string | URL | Request,
    init?: RequestInit,
  ) => {
    const target = String(input)
    const method = target.split('/api/')[1] ?? ''
    // The pre-signed upload: bytes, no token, and the only place the envelope exists.
    if (target.startsWith('https://files.slack.com/')) {
      const form = init?.body as FormData
      const blob = form.get('file') as Blob
      uploads.push({ name: String(form.get('filename')), json: await blob.text() })
      return new Response('OK', { status: 200 })
    }
    const body = new URLSearchParams(String(init?.body ?? ''))
    calls.push({ method, body })
    if (method === 'users.conversations') {
      return json({ ok: true, channels: [{ id: 'C0HUB', name: 'agent-hub', is_private: true }] })
    }
    if (method === 'conversations.history') {
      // A write refreshes before deciding, and the reconcile pass marks as deleted
      // whatever the channel no longer returns. The archive *is* the channel here:
      // mirroring it keeps both passes honest without inventing messages.
      const oldest = Number(body.get('oldest') ?? 0)
      const messages = archived()
        .filter((record) => Number(record.ts) > oldest)
        .sort((left, right) => Number(right.ts) - Number(left.ts))
        .map((record) => ({
          ts: record.ts,
          ...(record.text !== undefined ? { text: record.text } : {}),
          ...(record.user ? { user: record.user } : {}),
        }))
      return json({ ok: true, messages })
    }
    if (method === 'chat.postMessage') {
      NEXT_POST_TS += 1
      return json({
        ok: true,
        channel: body.get('channel'),
        ts: `${NEXT_POST_TS}.000100`,
        message: { text: body.get('text') },
      })
    }
    if (method === 'files.getUploadURLExternal') {
      return json({
        ok: true,
        upload_url: 'https://files.slack.com/upload/v1/test',
        file_id: `F0TEST${uploads.length + 1}`,
      })
    }
    if (method === 'files.completeUploadExternal') {
      NEXT_POST_TS += 1
      const channel = String(body.get('channel_id'))
      const files = JSON.parse(String(body.get('files'))) as Array<{ id: string; title: string }>
      return json({
        ok: true,
        files: files.map((file) => ({
          id: file.id,
          shares: { private: { [channel]: [{ ts: `${NEXT_POST_TS}.000100` }] } },
        })),
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

  return { calls, uploads, client }
}

/** One entry per envelope that was posted: the upload that carried it. */
function posts(calls: Harness['calls']) {
  return calls.filter((call) => call.method === 'files.completeUploadExternal')
}

function uploaded(uploads: Harness['uploads']): Array<Record<string, unknown>> {
  return uploads.map((upload) => JSON.parse(upload.json) as Record<string, unknown>)
}

test('forum_post is a dry run until confirm, then posts once and audits it', async (t) => {
  const { calls, uploads, client } = await setup(t)

  const preview = await client.callTool({
    name: 'forum_post',
    arguments: { to: 'reviewer@laptop', title: 'devuelve el favor', body: 'revisa esto' },
  })
  const previewText = textOf(preview)
  assert.match(previewText, /Nothing sent/)
  assert.match(previewText, /It travels as forum-\w+\.json/)
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
  assert.equal(sends[0]?.body.get('channel_id'), 'C0HUB')
  assert.match(
    String(sends[0]?.body.get('initial_comment')),
    /asks reviewer@laptop: devuelve el favor/,
    'the channel gets the sentence a person reads, and nothing else',
  )
  const [envelope] = uploaded(uploads)
  assert.equal(envelope?.kind, 'task')
  assert.equal(envelope?.to, 'reviewer@laptop')
  assert.equal(uploads[0]?.name, `forum-${String(envelope?.id)}.json`)
  assert.equal(
    sends[0]?.body.get('blocks'),
    null,
    'an upload takes no blocks: Slack builds the message from the bytes',
  )
  assert.doesNotMatch(String(sends[0]?.body.get('initial_comment')), /Message Sent by AI/)

  const audit = await readFile(join(root, 'data', 'slack-browser-mcp', 'audit.jsonl'), 'utf8')
  assert.match(audit, /agent-hub/)
})

test('a bare name is resolved to name@host in the envelope', async (t) => {
  const { uploads, client } = await setup(t)
  const posted = await client.callTool({
    name: 'forum_post',
    arguments: { to: 'builder', title: 'sin host', body: 'pero con destino', confirm: true },
  })
  assert.match(textOf(posted), /Posted to/)
  const [envelope] = uploaded(uploads)
  assert.equal(envelope?.to, 'builder@desktop', 'the reader only knows the full label')
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
  const { calls, uploads } = await setup(t)
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
  assert.equal(send?.body.get('channel_id'), 'C0HUB')
  assert.match(String(send?.body.get('initial_comment')), /analyst@desktop joined the forum/)
  assert.equal(uploaded(uploads)[0]?.kind, 'register')
})

test('a session says it is here once, and the claim mark keeps it quiet', async (t) => {
  const { calls, uploads } = await setup(t)
  const { openForum } = await import('../src/forum/context.ts')
  const { announceLiveness, transportFor } = await import('../src/forum/service.ts')
  const { FileCredentialStore } = await import('../src/auth/store.ts')
  const resolver = async (teamId: string) =>
    transportFor(await new FileCredentialStore().get(teamId))

  delete process.env.SLACK_BROWSER_MCP_NO_PRESENCE
  t.after(() => {
    process.env.SLACK_BROWSER_MCP_NO_PRESENCE = '1'
  })

  const first = await announceLiveness(await openForum(), resolver, { path: 'session' })
  assert.equal(first.posted, 1)
  const text = String(posts(calls)[0]?.body.get('initial_comment'))
  assert.match(text, /reviewer@laptop is here/)
  const [envelope] = uploaded(uploads)
  assert.equal(envelope?.kind, 'presence')
  assert.equal(envelope?.state, 'here')

  const second = await announceLiveness(await openForum(), resolver, { path: 'session' })
  assert.equal(second.posted, 0, 'a fresh claim keeps the channel quiet')
  assert.equal(posts(calls).length, 1)
})

test('the poll path posts a beacon and can never answer', async (t) => {
  const { calls, uploads } = await setup(t)
  const { openForum } = await import('../src/forum/context.ts')
  const { announceLiveness, transportFor, locateTask } = await import('../src/forum/service.ts')
  const { loadViews } = await import('../src/forum/inbox.ts')
  const { FileCredentialStore } = await import('../src/auth/store.ts')
  const resolver = async (teamId: string) =>
    transportFor(await new FileCredentialStore().get(teamId))

  delete process.env.SLACK_BROWSER_MCP_NO_PRESENCE
  t.after(() => {
    process.env.SLACK_BROWSER_MCP_NO_PRESENCE = '1'
  })

  const context = await openForum()
  // There is work waiting for this agent, which is exactly when an over-eager daemon
  // would answer. It reads it and says nothing but "the machine is up".
  const waiting = locateTask(await loadViews(context), TASK)
  assert.equal(waiting.task.to, 'reviewer@laptop')

  const first = await announceLiveness(context, resolver, { path: 'poll' })
  assert.equal(first.posted, 1)
  const text = String(posts(calls)[0]?.body.get('initial_comment'))
  assert.match(text, /reviewer@laptop is up/)
  const envelopes = uploaded(uploads)
  assert.equal(envelopes[0]?.kind, 'beacon')
  assert.equal(envelopes[0]?.state, undefined, 'a beacon claims nothing about the person')

  const second = await announceLiveness(context, resolver, { path: 'poll' })
  assert.equal(second.posted, 0, 'the beacon window keeps the poller quiet')

  const sent = uploaded(uploads)
  assert.equal(sent.length, 1, 'a poll writes once and only once')
  assert.equal(sent[0]?.kind, 'beacon')
  assert.equal(
    sent.filter((body) => ['presence', 'accept', 'result', 'ack', 'progress'].includes(String(body.kind)))
      .length,
    0,
    'the listener never answers in the user’s name',
  )
})

test('a write retries once when the thread lags behind', async (t) => {
  const { calls } = await setup(t)
  const { openForum } = await import('../src/forum/context.ts')
  const { replyTo, transportFor } = await import('../src/forum/service.ts')
  const { FileCredentialStore } = await import('../src/auth/store.ts')
  const { ArchiveStore } = await import('../src/archive/store.ts')
  const { render, summaryFor } = await import('../src/forum/envelope.ts')
  const { newId } = await import('../src/forum/id.ts')

  const archive = new ArchiveStore(join(root, 'data', 'slack-browser-mcp', 'archive'))
  const accept = {
    v: 1 as const,
    kind: 'accept' as const,
    id: newId(),
    from: 'reviewer@laptop',
    sent: iso(60),
    task: TASK,
  }

  let refreshes = 0
  const hook = {
    retryDelayMs: 0,
    refresh: async () => {
      refreshes += 1
      // The first look sees the old thread; by the retry Slack has served the accept.
      if (refreshes === 2) {
        await archive.append('T0TEST', 'C0HUB', [
          {
            ts: `${NOW_SECONDS - 60}.000500`,
            channel: 'C0HUB',
            text: render(summaryFor(accept), accept),
            user: 'U0REVIEWER',
            synced_at: '2026-10-28T09:30:00Z',
          },
        ])
      }
    },
  }

  const result = await replyTo(
    await openForum(),
    async (teamId) => transportFor(await new FileCredentialStore().get(teamId)),
    { id: TASK, kind: 'progress', body: 'starting', confirm: true, by: 'agent' },
    hook,
  )
  assert.equal(result.kind, 'posted')
  assert.equal(refreshes, 2, 'one look, one retry')
  assert.equal(posts(calls).length, 1)
})

test('unregistering says goodbye, forgets the identity and retires the label', async (t) => {
  const { calls, uploads } = await setup(t)
  const { openForum } = await import('../src/forum/context.ts')
  const { unregisterIdentity, transportFor } = await import('../src/forum/service.ts')
  const { FileCredentialStore } = await import('../src/auth/store.ts')
  const { ForumStore } = await import('../src/forum/binding.ts')
  const resolver = async (teamId: string) =>
    transportFor(await new FileCredentialStore().get(teamId))

  delete process.env.SLACK_BROWSER_MCP_NO_PRESENCE
  t.after(() => {
    process.env.SLACK_BROWSER_MCP_NO_PRESENCE = '1'
  })

  const result = await unregisterIdentity(await openForum(), resolver)
  assert.equal(result.label, 'reviewer@laptop')
  assert.equal(result.posted, 1)
  assert.equal(uploaded(uploads)[0]?.state, 'gone')
  assert.match(String(posts(calls)[0]?.body.get('initial_comment')), /left the forum/)

  const file = await new ForumStore(join(configDir, 'forum.json')).read()
  assert.deepEqual(Object.keys(file.identities), [], 'the identity is gone')
  assert.equal(file.default_identity, undefined)
  assert.ok(file.ignored.includes('reviewer@laptop'), 'its own label is retired')
})

test('posting to a peer past its ttl warns but still works', async (t) => {
  const { calls, client } = await setup(t, { peer_ttl_hours: 24 })
  const { ArchiveStore } = await import('../src/archive/store.ts')
  const { render, summaryFor } = await import('../src/forum/envelope.ts')
  const { newId } = await import('../src/forum/id.ts')

  const archive = new ArchiveStore(join(root, 'data', 'slack-browser-mcp', 'archive'))
  const envelope = {
    v: 1 as const,
    kind: 'register' as const,
    id: newId(),
    from: 'ghost@desktop',
    sent: '2026-10-26T09:00:00Z',
    name: 'ghost',
    host: 'desktop',
    capabilities: ['code'],
  }
  await archive.append('T0TEST', 'C0HUB', [
    {
      ts: '1792972800.000100',
      channel: 'C0HUB',
      text: render(summaryFor(envelope), envelope),
      synced_at: '2026-10-26T09:00:00Z',
    },
  ])

  const preview = await client.callTool({
    name: 'forum_post',
    arguments: { to: 'ghost@desktop', title: 'are you there', body: 'ping' },
  })
  assert.match(textOf(preview), /Nothing sent/)
  assert.match(textOf(preview), /reads as out/)
  assert.equal(posts(calls).length, 0)
})

test('there is no hourly budget: back-to-back posts both go out', async (t) => {
  const { calls, client } = await setup(t)

  for (const [title, body] of [
    ['uno', 'a'],
    ['dos', 'b'],
  ] as const) {
    const result = await client.callTool({
      name: 'forum_post',
      arguments: { to: 'reviewer@laptop', title, body, confirm: true },
    })
    assert.match(textOf(result), /Posted to/)
  }
  assert.equal(posts(calls).length, 2)
})
