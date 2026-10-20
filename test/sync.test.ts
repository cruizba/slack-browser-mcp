import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import { ArchiveIndex } from '../src/archive/index.ts'
import { ArchiveStore } from '../src/archive/store.ts'
import { syncAccount, type SyncClient } from '../src/archive/sync.ts'
import type { SlackAccount } from '../src/auth/store.ts'
import type { HistoryPage, SlackChannel, SlackMessage } from '../src/slack/client.ts'

const account: SlackAccount = {
  team_id: 'T0TEST',
  team_name: 'CodeURJC',
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
  user: 'U0CARLOS',
  text: 'despliegue terminado',
  reply_count: 2,
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
  client.messages = [{ ...parent, reply_count: 3 }]
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
    user: 'U0CARLOS',
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
