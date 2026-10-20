import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { after, before, describe, it } from 'node:test'

import { ArchiveIndex } from '../src/archive/index.ts'
import { localHistory, localThread } from '../src/archive/local.ts'
import { rememberMessages } from '../src/archive/remember.ts'
import { ArchiveStore, type ArchivedMessage } from '../src/archive/store.ts'

let root: string
let store: ArchiveStore
let index: ArchiveIndex

const TEAM = 'T0TEST'

function record(channel: string, overrides: Partial<ArchivedMessage> = {}): ArchivedMessage {
  return {
    ts: '1792500000.000100',
    channel,
    channel_name: channel,
    user: 'U0AUTHOR',
    text: 'despliegue terminado',
    synced_at: '2026-10-20T12:00:00.000Z',
    ...overrides,
  }
}

before(async () => {
  root = await mkdtemp(join(tmpdir(), 'slack-local-'))
  store = new ArchiveStore(join(root, 'archive'))
  index = new ArchiveIndex(join(root, 'archive.sqlite'))
  await index.ensure()
})

after(async () => {
  index.close()
  await rm(root, { recursive: true, force: true })
})

describe('localHistory', () => {
  it('serves the newest messages of an archived channel', async () => {
    const channel = 'C0LOCAL1'
    await store.append(TEAM, channel, [
      record(channel, { ts: '1792500000.000100', text: 'uno' }),
      record(channel, { ts: '1792500060.000200', text: 'dos' }),
      record(channel, { ts: '1792500120.000300', text: 'tres' }),
    ])

    const messages = await localHistory({
      store,
      teamId: TEAM,
      channelId: channel,
      limit: 2,
    })

    assert.deepEqual(
      messages?.map((message) => message.text),
      ['dos', 'tres'],
    )
  })

  it('asks Slack when the channel was never archived', async () => {
    const messages = await localHistory({ store, teamId: TEAM, channelId: 'C0MISSING', limit: 20 })
    assert.equal(messages, undefined)
  })

  it('answers empty for a channel a sync walked and found quiet', async () => {
    const messages = await localHistory({
      store,
      teamId: TEAM,
      channelId: 'C0QUIET',
      limit: 20,
      oldest: '1792447200',
      synced: true,
    })
    assert.deepEqual(messages, [])
  })

  it('leaves thread replies and deleted messages out of channel history', async () => {
    const channel = 'C0LOCAL2'
    await store.append(TEAM, channel, [
      record(channel, { ts: '1792500000.000100' }),
      record(channel, { ts: '1792500060.000200', thread_ts: '1792500000.000100', text: 'una respuesta' }),
      record(channel, { ts: '1792500120.000300', deleted_at: '2026-10-20T12:30:00.000Z' }),
    ])

    const messages = await localHistory({ store, teamId: TEAM, channelId: channel, limit: 20 })
    assert.deepEqual(
      messages?.map((message) => message.ts),
      ['1792500000.000100'],
    )
  })

  it('returns empty when the window does not overlap the channel\'s data', async () => {
    const channel = 'C0LOCAL3'
    await store.append(TEAM, channel, [record(channel, { ts: '1792500000.000100' })])

    // The archive is current, but the requested window falls after it.
    const messages = await localHistory({
      store,
      teamId: TEAM,
      channelId: channel,
      limit: 20,
      oldest: '1792500001.000000',
    })
    assert.deepEqual(messages, [])
  })

  it('answers empty when the window is inside what the archive holds', async () => {
    const channel = 'C0LOCAL4'
    await store.append(TEAM, channel, [record(channel, { ts: '1792500000.000100' })])

    const messages = await localHistory({
      store,
      teamId: TEAM,
      channelId: channel,
      limit: 20,
      latest: '1792000000.000000',
    })
    assert.deepEqual(messages, [])
  })
})

describe('localThread', () => {
  const channel = 'C0THREAD'

  it('refuses a thread that is only half archived', async () => {
    await store.append(TEAM, channel, [
      record(channel, { ts: '1792500000.000100', reply_count: 2 }),
      record(channel, { ts: '1792500060.000200', thread_ts: '1792500000.000100', text: 'una' }),
    ])

    const messages = await localThread({
      store,
      teamId: TEAM,
      channelId: channel,
      threadTs: '1792500000.000100',
      limit: 100,
    })
    assert.equal(messages, undefined)
  })

  it('serves a complete thread with the parent first', async () => {
    await store.append(TEAM, channel, [
      record(channel, { ts: '1792500120.000300', thread_ts: '1792500000.000100', text: 'dos' }),
    ])

    const messages = await localThread({
      store,
      teamId: TEAM,
      channelId: channel,
      threadTs: '1792500000.000100',
      limit: 100,
    })
    assert.deepEqual(
      messages?.map((message) => message.text),
      ['despliegue terminado', 'una', 'dos'],
    )
  })

  it('asks Slack for a thread the archive never saw', async () => {
    const messages = await localThread({
      store,
      teamId: TEAM,
      channelId: channel,
      threadTs: '1799999999.000999',
      limit: 100,
    })
    assert.equal(messages, undefined)
  })
})

describe('rememberMessages', () => {
  it('appends what Slack answered and makes it searchable', async () => {
    const channel = 'C0REMEMBER'
    const result = await rememberMessages({
      store,
      index,
      teamId: TEAM,
      channel: { id: channel, name: 'operations' },
      messages: [{ ts: '1792500000.000100', text: 'el mtu del vxlan', user: 'U0AUTHOR' }],
    })

    assert.deepEqual(result, { written: 1, indexed: true })
    const hits = index.search({ query: 'vxlan' })
    assert.equal(hits.length, 1)
    assert.equal(hits[0]?.channel_name, 'operations')
  })

  it('writes nothing when Slack repeats what the archive already has', async () => {
    const channel = 'C0REMEMBER2'
    const args = {
      store,
      index,
      teamId: TEAM,
      channel: { id: channel, name: 'operations' },
      messages: [{ ts: '1792500000.000100', text: 'sin cambios', user: 'U0AUTHOR' }],
    }
    await rememberMessages(args)
    const second = await rememberMessages(args)

    assert.equal(second.written, 0)
    const lines = (await readFile(join(root, 'archive', TEAM, `${channel}.jsonl`), 'utf8'))
      .trim()
      .split('\n')
    assert.equal(lines.length, 1)
  })

  it('files a search hit as searchable but not as channel history', async () => {
    const channel = 'C0SEARCHHIT'
    await rememberMessages({
      store,
      index,
      teamId: TEAM,
      channel: { id: channel, name: 'operations' },
      messages: [{ ts: '1792500000.000100', text: 'vxlan en el archivo viejo' }],
      source: 'search',
    })

    assert.equal(index.search({ query: 'vxlan' }).length, 1)
    const history = await localHistory({ store, teamId: TEAM, channelId: channel, limit: 20 })
    assert.equal(history, undefined, 'a gap must not look like a quiet channel')
  })

  it('appends an edit as a new line', async () => {
    const channel = 'C0REMEMBER3'
    const base = {
      store,
      index,
      teamId: TEAM,
      channel: { id: channel, name: 'operations' },
    }
    await rememberMessages({ ...base, messages: [{ ts: '1792500000.000100', text: 'antes' }] })
    const edited = await rememberMessages({
      ...base,
      messages: [{ ts: '1792500000.000100', text: 'después', edited: { ts: '1792500200.000000' } }],
    })

    assert.equal(edited.written, 1)
    const cached = await store.read(TEAM, channel)
    assert.equal(cached.length, 1)
    assert.equal(cached[0]?.text, 'después')
  })
})
