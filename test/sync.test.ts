import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import { ArchiveFileStore } from '../src/archive/files.ts'
import { localThread } from '../src/archive/local.ts'
import { ArchiveIndex } from '../src/archive/index.ts'
import { ArchiveStore } from '../src/archive/store.ts'
import { DEFAULT_BACKFILL_DAYS, syncAccount, type SyncClient } from '../src/archive/sync.ts'
import type { SlackAccount } from '../src/auth/store.ts'
import { envelopeOf, forumFileName } from '../src/forum/carrier.ts'
import { encode, type Envelope } from '../src/forum/envelope.ts'
import type { HistoryPage, SlackChannel, SlackFile, SlackMessage } from '../src/slack/client.ts'

const account: SlackAccount = {
  team_id: 'T0TEST',
  team_name: 'Globex',
  token: 'xoxc-test',
  d_cookie: 'xoxd-test',
  saved_at: '2026-10-20T00:00:00.000Z',
}

const channel: SlackChannel = { id: 'C0CINOTIF', name: 'ci-notifications' }

class FakeClient implements SyncClient {
  readonly historyCalls: Array<{ channel: string; oldest?: string }> = []
  readonly repliesCalls: string[] = []
  messages: SlackMessage[]
  threads: Record<string, SlackMessage[]>
  /** Set by the tests that carry an envelope as a file. */
  downloadFile?: (file: SlackFile) => Promise<Uint8Array>

  constructor(messages: SlackMessage[], threads: Record<string, SlackMessage[]> = {}) {
    this.messages = messages
    this.threads = threads
  }

  async listMyChannels(): Promise<SlackChannel[]> {
    return [channel]
  }

  async history(params: { channel: string; oldest?: string }): Promise<HistoryPage> {
    this.historyCalls.push({ channel: params.channel, ...(params.oldest ? { oldest: params.oldest } : {}) })
    const oldest = params.oldest ? Number(params.oldest) : 0
    return {
      messages: this.messages.filter((message) => Number(message.ts) > oldest),
      has_more: false,
    }
  }

  async repliesPage(params: { channel: string; thread_ts: string }): Promise<HistoryPage> {
    this.repliesCalls.push(params.thread_ts)
    return { messages: this.threads[params.thread_ts] ?? [], has_more: false }
  }
}

/** A client that pages like Slack, so the tests can run out of budget mid-window. */
class PagedClient implements SyncClient {
  readonly historyCalls: Array<{ oldest?: string; cursor?: string }> = []
  messages: SlackMessage[]
  readonly pageSize: number
  readonly delayMs: number

  constructor(messages: SlackMessage[], pageSize: number, delayMs = 0) {
    this.messages = messages
    this.pageSize = pageSize
    this.delayMs = delayMs
  }

  async listMyChannels(): Promise<SlackChannel[]> {
    return [channel]
  }

  async history(params: {
    channel: string
    oldest?: string
    cursor?: string
  }): Promise<HistoryPage> {
    this.historyCalls.push({
      ...(params.oldest ? { oldest: params.oldest } : {}),
      ...(params.cursor ? { cursor: params.cursor } : {}),
    })
    if (this.delayMs > 0) await new Promise((done) => setTimeout(done, this.delayMs))
    const newer = this.messages
      .filter((message) => Number(message.ts) > Number(params.oldest ?? 0))
      .sort((left, right) => Number(right.ts) - Number(left.ts))
    const start = Number(params.cursor ?? 0)
    const page = newer.slice(start, start + this.pageSize)
    const next = start + this.pageSize < newer.length ? String(start + this.pageSize) : undefined
    return {
      messages: page,
      has_more: next !== undefined,
      ...(next ? { next_cursor: next } : {}),
    }
  }

  async repliesPage(): Promise<HistoryPage> {
    return { messages: [], has_more: false }
  }
}

/** Serves one thread in pages, the way `conversations.replies` does. */
class ThreadClient implements SyncClient {
  readonly pages: Array<string | undefined> = []
  private readonly parentMessage: SlackMessage
  private readonly threads: SlackMessage[]
  private readonly pageSize: number

  constructor(parentMessage: SlackMessage, threads: SlackMessage[], pageSize: number) {
    this.parentMessage = parentMessage
    this.threads = threads
    this.pageSize = pageSize
  }

  async listMyChannels(): Promise<SlackChannel[]> {
    return [channel]
  }

  async history(): Promise<HistoryPage> {
    return { messages: [this.parentMessage], has_more: false }
  }

  async repliesPage(params: { cursor?: string }): Promise<HistoryPage> {
    this.pages.push(params.cursor)
    const all = [this.parentMessage, ...this.threads]
    const start = Number(params.cursor ?? 0)
    const page = all.slice(start, start + this.pageSize)
    const next = start + this.pageSize < all.length ? String(start + this.pageSize) : undefined
    return {
      messages: page,
      has_more: next !== undefined,
      ...(next ? { next_cursor: next } : {}),
    }
  }
}

async function scratch(t: { after: (fn: () => Promise<void>) => void }) {
  const root = await mkdtemp(join(tmpdir(), 'slack-sync-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  return {
    store: new ArchiveStore(join(root, 'archive')),
    index: new ArchiveIndex(join(root, 'archive.sqlite')),
  }
}

function at(minutes: number): Date {
  return new Date(Date.UTC(2026, 9, 20, 12, minutes, 0))
}

function ts(minutesAgo: number, suffix = '000100'): string {
  return `${(at(0).getTime() / 1000 - minutesAgo * 60).toFixed(6).slice(0, -6)}${suffix}`
}

const parent: SlackMessage = {
  ts: '1792500000.000100',
  user: 'U0AUTHOR',
  text: 'despliegue terminado',
  reply_count: 1,
}

const reply: SlackMessage = {
  ts: '1792500060.000200',
  user: 'U0ANA',
  text: 'enterado',
  thread_ts: parent.ts,
}

test('first run backfills messages and their thread, and remembers the cursor', async (t) => {
  const { store, index } = await scratch(t)
  const client = new FakeClient([parent], { [parent.ts]: [parent, reply] })

  const report = await syncAccount({
    account,
    client,
    store,
    index,
    options: { now: at(0), backfillDays: 30 },
  })

  assert.equal(report.channels_synced, 1)
  assert.equal(report.messages_added, 1)
  assert.equal(report.replies_added, 1)
  assert.deepEqual(client.repliesCalls, [parent.ts])

  const archived = await store.read(account.team_id, channel.id)
  assert.deepEqual(archived.map((message) => message.ts).sort(), [parent.ts, reply.ts].sort())
  assert.equal(archived.find((message) => message.ts === reply.ts)?.thread_ts, parent.ts)

  const state = await store.loadState()
  const entry = state.teams[account.team_id]?.channels[channel.id]
  assert.equal(entry?.newest_ts, reply.ts)
  assert.equal(entry?.last_error, undefined)
})

test('second run asks only for messages newer than the cursor', async (t) => {
  const { store, index } = await scratch(t)
  const client = new FakeClient([parent], { [parent.ts]: [parent, reply] })

  await syncAccount({ account, client, store, index, options: { now: at(0) } })
  client.messages = [parent, { ts: '1792500300.000300', user: 'U0ANA', text: 'nuevo aviso' }]
  client.historyCalls.length = 0
  client.repliesCalls.length = 0

  const report = await syncAccount({ account, client, store, index, options: { now: at(5) } })

  assert.equal(report.messages_added, 1)
  assert.equal(client.historyCalls.length, 1, 'no reconcile on the same day')
  assert.equal(client.historyCalls[0]?.oldest, reply.ts)
  assert.deepEqual(client.repliesCalls, [], 'an unchanged reply_count is not refetched')
})

test('a reply_count change refetches the thread on the next reconcile', async (t) => {
  const { store, index } = await scratch(t)
  const client = new FakeClient([parent], { [parent.ts]: [parent, reply] })
  await syncAccount({ account, client, store, index, options: { now: at(0) } })

  const state = await store.loadState()
  const entry = state.teams[account.team_id]?.channels[channel.id]
  assert.ok(entry)
  entry.last_reconcile = new Date(Date.UTC(2026, 9, 18, 12, 0, 0)).toISOString()
  await store.saveState(state)

  const extra: SlackMessage = { ts: '1792500120.000400', user: 'U0ANA', text: 'y otra', thread_ts: parent.ts }
  client.messages = [{ ...parent, reply_count: 2 }]
  client.threads[parent.ts] = [parent, reply, extra]
  client.repliesCalls.length = 0

  const report = await syncAccount({ account, client, store, index, options: { now: at(5) } })
  assert.deepEqual(client.repliesCalls, [parent.ts])
  assert.equal(report.replies_added, 1, 'only the new reply is written, not the one already archived')
})

test('a daily reconcile marks vanished messages as deleted', async (t) => {
  const { store, index } = await scratch(t)
  const second: SlackMessage = { ts: '1792500180.000500', user: 'U0ANA', text: 'borrame' }
  const client = new FakeClient([parent, second], { [parent.ts]: [parent, reply] })
  await syncAccount({ account, client, store, index, options: { now: at(0) } })

  client.messages = [parent]
  const report = await syncAccount({
    account,
    client,
    store,
    index,
    options: { now: at(60 * 25) },
  })

  assert.equal(report.deletions_marked, 1)
  const archived = await store.read(account.team_id, channel.id)
  const deleted = archived.find((message) => message.ts === second.ts)
  assert.ok(deleted?.deleted_at, 'the message is kept, marked as deleted')
  assert.equal(index.search({ query: 'borrame' }).length, 0)
})

test('thread backlog drains a few threads per run instead of all at once', async (t) => {
  const { store, index } = await scratch(t)
  const parents: SlackMessage[] = [1, 2, 3].map((index) => ({
    ts: `1792500${index}00.000100`,
    user: 'U0AUTHOR',
    text: `hilo ${index}`,
    reply_count: 1,
  }))
  const client = new FakeClient(parents)
  for (const threadParent of parents) {
    client.threads[threadParent.ts] = [
      threadParent,
      { ts: `${threadParent.ts}9`, user: 'U0ANA', text: 'respuesta', thread_ts: threadParent.ts },
    ]
  }

  const first = await syncAccount({
    account,
    client,
    store,
    index,
    options: { now: at(0), maxThreadsPerChannel: 1 },
  })
  assert.equal(first.threads_fetched, 1)

  const stateAfterFirst = await store.loadState()
  const pending = stateAfterFirst.teams[account.team_id]?.channels[channel.id]?.pending_threads ?? []
  assert.equal(pending.length, 2, 'the rest waits in the backlog')

  const second = await syncAccount({
    account,
    client,
    store,
    index,
    options: { now: at(5), maxThreadsPerChannel: 5 },
  })
  assert.equal(second.threads_fetched, 2)

  const stateAfterSecond = await store.loadState()
  assert.deepEqual(
    stateAfterSecond.teams[account.team_id]?.channels[channel.id]?.pending_threads ?? [],
    [],
  )
})

test('the channel file stays one line per state change, not per run', async (t) => {
  const { store, index } = await scratch(t)
  const client = new FakeClient([parent], { [parent.ts]: [parent, reply] })
  await syncAccount({ account, client, store, index, options: { now: at(0) } })
  await syncAccount({ account, client, store, index, options: { now: at(5) } })

  const raw = await readFile(store.channelFile(account.team_id, channel.id), 'utf8')
  assert.equal(raw.trim().split('\n').length, 2, 'no rewrite when nothing changed')
})

test('a refresh window notices a reply on a parent older than the cursor', async (t) => {
  const { store, index } = await scratch(t)
  const client = new FakeClient([parent], { [parent.ts]: [parent, reply] })
  await syncAccount({ account, client, store, index, options: { now: at(0) } })

  const extra: SlackMessage = { ts: '1792500120.000400', user: 'U0ANA', text: 'y otra', thread_ts: parent.ts }
  client.messages = [{ ...parent, reply_count: 2 }]
  client.threads[parent.ts] = [parent, reply, extra]
  client.historyCalls.length = 0
  client.repliesCalls.length = 0

  const report = await syncAccount({
    account,
    client,
    store,
    index,
    options: { now: at(5), refreshDays: 1 },
  })

  const oldest = Number(client.historyCalls[0]?.oldest)
  assert.ok(
    Math.abs(at(5).getTime() / 1000 - oldest - 86_400) < 1,
    'the window has to reach the parent, not only messages after the cursor',
  )
  assert.deepEqual(client.repliesCalls, [parent.ts], 'the changed reply_count re-reads the thread')
  assert.equal(report.replies_added, 1, 'the re-read reply is not rewritten')
})

test('a thread that came back short stays queued until Slack shows the reply', async (t) => {
  const { store, index } = await scratch(t)
  const client = new FakeClient([{ ...parent, reply_count: 2 }], { [parent.ts]: [parent, reply] })

  await syncAccount({ account, client, store, index, options: { now: at(0) } })
  let entry = (await store.loadState()).teams[account.team_id]?.channels[channel.id]
  assert.deepEqual(entry?.pending_threads, [parent.ts], 'one reply short is not a whole thread')

  const extra: SlackMessage = { ts: '1792500120.000400', user: 'U0ANA', text: 'y otra', thread_ts: parent.ts }
  client.threads[parent.ts] = [parent, reply, extra]
  await syncAccount({ account, client, store, index, options: { now: at(5) } })

  entry = (await store.loadState()).teams[account.team_id]?.channels[channel.id]
  assert.deepEqual(entry?.pending_threads ?? [], [])
})

test('a thread with more replies than one page is filed whole', async (t) => {
  const { store, index } = await scratch(t)
  const replies: SlackMessage[] = Array.from({ length: 250 }, (_, index) => ({
    ts: `1792500060.${String(index).padStart(6, '0')}`,
    user: 'U0ANA',
    text: `respuesta ${index}`,
    thread_ts: parent.ts,
  }))
  const client = new ThreadClient({ ...parent, reply_count: 250 }, replies, 100)

  await syncAccount({ account, client, store, index, options: { now: at(0) } })

  const archived = await store.read(account.team_id, channel.id)
  assert.equal(
    archived.filter((record) => record.thread_ts === parent.ts).length,
    250,
    'every page of the thread is filed, not only the first',
  )
  assert.equal(client.pages.length, 3, 'a page of 100 plus one of 100 plus the tail')
  const entry = (await store.loadState()).teams[account.team_id]?.channels[channel.id]
  assert.deepEqual(entry?.pending_threads ?? [], [], 'a thread read whole leaves the backlog')

  const thread = await localThread({
    store,
    teamId: account.team_id,
    channelId: channel.id,
    threadTs: parent.ts,
    limit: 300,
  })
  assert.equal(thread?.length, 251, 'the archive serves it without asking Slack again')
})

test('a channel fetch turns a search hit into real history', async (t) => {
  const { store, index } = await scratch(t)
  await store.append(account.team_id, channel.id, [
    {
      ts: parent.ts,
      channel: channel.id,
      channel_name: 'ci-notifications',
      user: 'U0AUTHOR',
      text: 'despliegue terminado',
      reply_count: 2,
      source: 'search',
      synced_at: '2026-10-20T12:00:00.000Z',
    },
  ])
  const client = new FakeClient([parent], { [parent.ts]: [] })
  await syncAccount({ account, client, store, index, options: { now: at(0) } })

  const [stored] = await store.read(account.team_id, channel.id)
  assert.equal(stored?.source, undefined, 'history must replace the search marker')
})

test('the index covers everything the run saw, even records a read tool already wrote', async (t) => {
  const { store, index } = await scratch(t)
  // A read tool already cached the message: it is in the JSONL but not in the index.
  await store.append(account.team_id, channel.id, [
    {
      ts: parent.ts,
      channel: channel.id,
      channel_name: 'ci-notifications',
      user: 'U0AUTHOR',
      text: 'despliegue terminado',
      reply_count: 2,
      synced_at: '2026-10-20T12:00:00.000Z',
    },
  ])
  // Sync fetches the same message again — unchanged compared to the cached copy.
  const client = new FakeClient([parent], { [parent.ts]: [] })
  await syncAccount({ account, client, store, index, options: { now: at(0) } })

  const hits = index.search({ query: 'despliegue' })
  assert.equal(hits.length, 1, 'the index must cover it even though sync did not rewrite the file')
})

test('a workspace seen for the first time is read back 90 days by default', async (t) => {
  const { store, index } = await scratch(t)
  const client = new FakeClient([parent])

  await syncAccount({
    account,
    client,
    store,
    index,
    options: { now: at(0), refreshDays: 1 },
  })

  const oldest = Number(client.historyCalls[0]?.oldest)
  assert.ok(Number.isFinite(oldest), 'the first run must page from a window, not from nothing')
  const days = (at(0).getTime() / 1000 - oldest) / 86_400
  assert.ok(
    Math.abs(days - DEFAULT_BACKFILL_DAYS) < 0.001,
    `expected ${DEFAULT_BACKFILL_DAYS} days, got ${days}`,
  )
})

const TASK_ID = '01J9Z7K3QX4MB8WN2R5T6Y7Z8A'

function envelope(overrides: Partial<Envelope> = {}): Envelope {
  return {
    v: 1,
    kind: 'task',
    id: TASK_ID,
    from: 'builder@laptop',
    to: 'reviewer@desktop',
    sent: '2026-10-20T11:00:00Z',
    title: 'revisar el PR',
    body: 'mira el diff\n\n```ts\nconst x = 1\n```',
    ...overrides,
  } as Envelope
}

test('an envelope that travels as a file is downloaded once and stays offline', async (t) => {
  const { store, index } = await scratch(t)
  const document = encode(envelope())
  const message: SlackMessage = {
    ts: '1792500300.000300',
    user: 'U0BUILDER',
    text: 'builder@laptop asks reviewer@desktop: revisar el PR',
    files: [
      {
        id: 'F0FORUM',
        name: forumFileName(TASK_ID),
        mimetype: 'text/markdown',
        size: Buffer.byteLength(document),
        url_private: 'https://files.slack.com/files/F0FORUM',
      },
    ],
  }
  const downloads: string[] = []
  const client = new FakeClient([message])
  client.downloadFile = async (file: SlackFile) => {
    downloads.push(file.id ?? '')
    return new TextEncoder().encode(document)
  }

  await syncAccount({ account, client, store, index, options: { now: at(0) } })
  assert.deepEqual(downloads, ['F0FORUM'], 'the envelope is fetched once')

  const [record] = await store.read('T0TEST', channel.id)
  assert.equal(record?.forum, document, 'the record carries the envelope, not a path to it')
  assert.deepEqual(envelopeOf(record!), envelope())
  const cached = await new ArchiveFileStore(store.root).find('T0TEST', 'F0FORUM')
  assert.ok(cached, 'the bytes are kept next to the archive, so read_file finds them')

  // A second run reads the file instead of asking Slack again.
  await syncAccount({ account, client, store, index, options: { now: at(5) } })
  assert.deepEqual(downloads, ['F0FORUM'])
})

test('a file that is not an envelope is left alone', async (t) => {
  const { store, index } = await scratch(t)
  const message: SlackMessage = {
    ts: '1792500300.000400',
    user: 'U0BUILDER',
    text: 'mira el informe',
    files: [{ id: 'F0REPORT', name: 'informe.json', mimetype: 'application/json' }],
  }
  let asked = 0
  const client = new FakeClient([message])
  client.downloadFile = async () => {
    asked += 1
    return new Uint8Array()
  }

  await syncAccount({ account, client, store, index, options: { now: at(0) } })
  assert.equal(asked, 0, 'a plain attachment is not an envelope')
  const [record] = await store.read('T0TEST', channel.id)
  assert.equal(record?.forum, undefined)
})

test('a reply carries its own envelope, so a state change is not invisible', async (t) => {
  const { store, index } = await scratch(t)
  const task = envelope()
  const accept: Envelope = {
    v: 1,
    kind: 'accept',
    id: '01J9Z7K3QX4MB8WN2R5T6Y7Z9C',
    from: 'reviewer@desktop',
    sent: '2026-10-20T11:05:00Z',
    task: TASK_ID,
  }
  const parent: SlackMessage = {
    ts: '1792500400.000500',
    user: 'U0BUILDER',
    text: 'builder@laptop asks reviewer@desktop: revisar el PR',
    reply_count: 1,
    files: [{ id: 'F0TASK', name: forumFileName(TASK_ID), size: 120 }],
  }
  const reply: SlackMessage = {
    ts: '1792500430.000600',
    user: 'U0REVIEWER',
    text: 'reviewer@desktop accepted ' + TASK_ID,
    thread_ts: parent.ts,
    files: [{ id: 'F0ACCEPT', name: forumFileName(String(accept.id)), size: 120 }],
  }
  const bodies: Record<string, string> = { F0TASK: encode(task), F0ACCEPT: encode(accept) }
  const asked: string[] = []
  const client = new FakeClient([parent], { [parent.ts]: [parent, reply] })
  client.downloadFile = async (file: SlackFile) => {
    asked.push(file.id ?? '')
    return new TextEncoder().encode(bodies[file.id ?? ''] ?? '')
  }

  await syncAccount({ account, client, store, index, options: { now: at(0) } })
  const records = await store.read('T0TEST', channel.id)
  const replyRecord = records.find((record) => record.ts === reply.ts)
  assert.ok(replyRecord, 'the reply was archived')
  assert.deepEqual(asked.sort(), ['F0ACCEPT', 'F0TASK'], 'both the task and its reply')
  assert.equal(replyRecord?.forum, encode(accept), 'a reply carries an envelope like a message')
  assert.equal(envelopeOf(replyRecord!)?.kind, 'accept')
})

test('a record filed without its envelope is completed on the next run', async (t) => {
  const { store, index } = await scratch(t)
  const json = encode(envelope())
  // What a v0.2.1 process left behind: the file name is there, the envelope is not.
  await store.append('T0TEST', channel.id, [
    {
      ts: '1792500100.000200',
      channel: channel.id,
      ...(channel.name ? { channel_name: channel.name } : {}),
      user: 'U0BUILDER',
      text: 'builder@laptop asks reviewer@desktop: revisar el PR',
      files: [{ id: 'F0OLD', name: forumFileName(TASK_ID) }],
      synced_at: '2026-10-20T10:00:00.000Z',
    },
  ])

  const asked: string[] = []
  const client = new FakeClient([])
  client.downloadFile = async (file: SlackFile) => {
    asked.push(file.id ?? '')
    return new TextEncoder().encode(json)
  }

  await syncAccount({ account, client, store, index, options: { now: at(0) } })
  assert.deepEqual(asked, ['F0OLD'], 'the file the record named is fetched')
  const [record] = await store.read('T0TEST', channel.id)
  assert.equal(record?.forum, json, 'the envelope is filed without a new sync of the message')
  assert.deepEqual(envelopeOf(record!), envelope())

  // And once it has one, another run does not download it again.
  await syncAccount({ account, client, store, index, options: { now: at(5) } })
  assert.deepEqual(asked, ['F0OLD'])
})

test('a read cut short tombstones nothing and keeps the cursor where it was', async (t) => {
  const { store, index } = await scratch(t)
  const first: SlackMessage = { ts: '1792500000.000100', user: 'U0ANA', text: 'uno' }
  const second: SlackMessage = { ts: '1792500060.000200', user: 'U0ANA', text: 'dos' }
  const third: SlackMessage = { ts: '1792500120.000300', user: 'U0ANA', text: 'tres' }

  await syncAccount({
    account,
    client: new PagedClient([first, second, third], 2),
    store,
    index,
    options: { now: at(0) },
  })
  const before = (await store.loadState()).teams[account.team_id]?.channels[channel.id]
  assert.equal(before?.newest_ts, third.ts)

  // The reconcile is due, and the catch-up read only gets one page of the four
  // messages it was asked for before the budget runs out: what it did not see is
  // unknown, not deleted, and the cursor must not step over it.
  const report = await syncAccount({
    account,
    client: new PagedClient(
      [
        first,
        second,
        third,
        { ts: '1792500180.000400', user: 'U0ANA', text: 'cuatro' },
        { ts: '1792500240.000500', user: 'U0ANA', text: 'cinco' },
        { ts: '1792500300.000600', user: 'U0ANA', text: 'seis' },
      ],
      2,
      400,
    ),
    store,
    index,
    options: { now: at(60 * 25), deadlineMs: 200 },
  })

  assert.equal(report.truncated, true)
  assert.equal(report.deletions_marked, 0, 'a partial window cannot prove a message is gone')
  const after = (await store.loadState()).teams[account.team_id]?.channels[channel.id]
  assert.equal(after?.newest_ts, third.ts, 'the cursor must not step over what was never read')
  assert.equal(after?.last_sync, at(0).toISOString(), 'a truncated channel is not fresh')
  assert.equal(after?.last_reconcile, at(0).toISOString())
  const live = (await store.read(account.team_id, channel.id)).filter((record) => !record.deleted_at)
  assert.equal(live.length, 5, "the page it did read is kept; the rest waits for the next run")
})

test('a run that is not a reconcile keeps the reconcile stamp', async (t) => {
  const { store, index } = await scratch(t)
  const client = new FakeClient([parent], { [parent.ts]: [parent, reply] })
  await syncAccount({ account, client, store, index, options: { now: at(0) } })
  const first = (await store.loadState()).teams[account.team_id]?.channels[channel.id]
  assert.equal(first?.last_reconcile, at(0).toISOString())

  client.historyCalls.length = 0
  await syncAccount({ account, client, store, index, options: { now: at(5) } })

  assert.equal(client.historyCalls.length, 1, 'no second walk on the same day')
  const second = (await store.loadState()).teams[account.team_id]?.channels[channel.id]
  assert.equal(second?.last_reconcile, at(0).toISOString(), 'the stamp outlives a plain run')
  assert.equal(second?.last_sync, at(5).toISOString())
})
