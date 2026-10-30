import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import { ArchiveFileStore } from '../src/archive/files.ts'
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

  async replies(params: { channel: string; thread_ts: string }): Promise<SlackMessage[]> {
    this.repliesCalls.push(params.thread_ts)
    return this.threads[params.thread_ts] ?? []
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
  assert.equal(report.replies_added, 2, 'thread reads are idempotent by ts')
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
  assert.equal(report.replies_added, 2, 'thread reads are idempotent by ts')
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
  const json = encode(envelope())
  const message: SlackMessage = {
    ts: '1792500300.000300',
    user: 'U0BUILDER',
    text: 'builder@laptop asks reviewer@desktop: revisar el PR',
    files: [
      {
        id: 'F0FORUM',
        name: forumFileName(TASK_ID),
        mimetype: 'application/json',
        size: Buffer.byteLength(json),
        url_private: 'https://files.slack.com/files/F0FORUM',
      },
    ],
  }
  const downloads: string[] = []
  const client = new FakeClient([message])
  client.downloadFile = async (file: SlackFile) => {
    downloads.push(file.id ?? '')
    return new TextEncoder().encode(json)
  }

  await syncAccount({ account, client, store, index, options: { now: at(0) } })
  assert.deepEqual(downloads, ['F0FORUM'], 'the envelope is fetched once')

  const [record] = await store.read('T0TEST', channel.id)
  assert.equal(record?.forum, json, 'the record carries the envelope, not a path to it')
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
