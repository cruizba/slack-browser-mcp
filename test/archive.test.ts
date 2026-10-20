import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm, stat } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { after, before, describe, it } from 'node:test'

import { ArchiveIndex } from '../src/archive/index.ts'
import { ArchiveStore, type ArchivedMessage } from '../src/archive/store.ts'

let root: string
let store: ArchiveStore
let index: ArchiveIndex

const TEAM = 'T0TEST'
const CHANNEL = 'C0CINOTIF'

function record(overrides: Partial<ArchivedMessage> = {}): ArchivedMessage {
  return {
    ts: '1792500000.000100',
    channel: CHANNEL,
    channel_name: 'ci-notifications',
    user: 'U0CARLOS',
    text: 'hola despliegue',
    synced_at: '2026-10-20T12:00:00.000Z',
    ...overrides,
  }
}

before(async () => {
  root = await mkdtemp(join(tmpdir(), 'slack-archive-'))
  store = new ArchiveStore(root)
  index = new ArchiveIndex(join(root, 'archive.sqlite'))
  await index.ensure()
})

after(async () => {
  index.close()
  await rm(root, { recursive: true, force: true })
})

describe('ArchiveStore', () => {
  it('writes one 0600 file per channel with 0700 directories', async () => {
    await store.append(TEAM, CHANNEL, [record()])
    const file = store.channelFile(TEAM, CHANNEL)
    assert.equal((await stat(file)).mode & 0o777, 0o600)
    assert.equal((await stat(join(root, TEAM))).mode & 0o777, 0o700)
    assert.equal((await stat(root)).mode & 0o777, 0o700)
  })

  it('folds the log by ts, keeping the last line', async () => {
    await store.append(TEAM, CHANNEL, [
      record({ ts: '1792500000.000200', text: 'primera version' }),
      record({ ts: '1792500000.000200', text: 'version editada', edited_ts: '1792500100.000000' }),
    ])
    const messages = await store.read(TEAM, CHANNEL)
    assert.equal(messages.length, 2)
    const edited = messages.find((message) => message.ts === '1792500000.000200')
    assert.equal(edited?.text, 'version editada')
    assert.equal(edited?.edited_ts, '1792500100.000000')
  })

  it('keeps a deleted message as history instead of dropping it', async () => {
    await store.append(TEAM, CHANNEL, [
      { ts: '1792500000.000100', channel: CHANNEL, deleted_at: '2026-10-20T12:05:00.000Z', synced_at: '2026-10-20T12:05:00.000Z' },
    ])
    const messages = await store.read(TEAM, CHANNEL)
    const deleted = messages.find((message) => message.ts === '1792500000.000100')
    assert.ok(deleted?.deleted_at)
    assert.equal(deleted?.text, 'hola despliegue', 'the original text is preserved')
  })

  it('clears deleted_at when the message reappears', async () => {
    await store.append(TEAM, CHANNEL, [record({ text: 'vuelve a estar' })])
    const messages = await store.read(TEAM, CHANNEL)
    assert.equal(messages.find((message) => message.ts === '1792500000.000100')?.deleted_at, undefined)
  })

  it('round-trips the sync state atomically', async () => {
    await store.saveState({
      version: 1,
      teams: { [TEAM]: { channels: { [CHANNEL]: { newest_ts: '1792500000.000200' } } } },
    })
    const state = await store.loadState()
    assert.equal(state.teams[TEAM]?.channels[CHANNEL]?.newest_ts, '1792500000.000200')
    assert.equal((await stat(join(root, 'state.json'))).mode & 0o777, 0o600)
  })
})

describe('ArchiveIndex', () => {
  it('indexes messages and finds them by word', () => {
    index.upsert(TEAM, [
      record({ ts: '1792500000.000300', text: 'el despliegue de produccion fallo' }),
      record({ ts: '1792500000.000400', text: 'otra cosa distinta' }),
    ])
    const hits = index.search({ query: 'despliegue' })
    assert.equal(hits.length, 1)
    assert.equal(hits[0]?.ts, '1792500000.000300')
    assert.equal(hits[0]?.channel_name, 'ci-notifications')
  })

  it('requires every word of a multi-word query', () => {
    assert.equal(index.search({ query: 'despliegue produccion' }).length, 1)
    assert.equal(index.search({ query: 'despliegue inexistente' }).length, 0)
  })

  it('survives FTS operators typed by a user', () => {
    assert.equal(index.search({ query: 'despliegue OR *' }).length, 1)
    assert.deepEqual(index.search({ query: '   ' }), [])
  })

  it('hides deleted messages unless asked', async () => {
    const tombstone = record({
      ts: '1792500000.000500',
      text: 'mensaje efimero',
      deleted_at: '2026-10-20T12:10:00.000Z',
    })
    await store.append(TEAM, CHANNEL, [tombstone])
    index.upsert(TEAM, [tombstone])
    assert.equal(index.search({ query: 'efimero' }).length, 0)
    const withDeleted = index.search({ query: 'efimero', includeDeleted: true })
    assert.equal(withDeleted.length, 1)
    assert.equal(withDeleted[0]?.deleted, true)
  })

  it('filters by channel name', () => {
    index.upsert(TEAM, [record({ ts: '1792500000.000600', channel: 'C0OTHER', channel_name: 'general', text: 'despliegue general' })])
    assert.equal(index.search({ query: 'despliegue', channelName: 'ci-notifications' }).length, 1)
    assert.equal(index.search({ query: 'despliegue', channelName: 'general' }).length, 1)
    assert.equal(index.search({ query: 'despliegue' }).length, 2)
  })

  it('rebuilds from the JSONL files after a reset', async () => {
    index.reset()
    assert.equal(index.search({ query: 'vuelve' }).length, 0)

    for (const team of await store.teams()) {
      for (const channel of await store.channelIds(team)) {
        index.upsert(team, await store.read(team, channel))
      }
    }

    const hits = index.search({ query: 'vuelve' })
    assert.equal(hits.length, 1, 'the index must be rebuildable from plain text')
    assert.equal(hits[0]?.channel_name, 'ci-notifications')
    const stats = index.stats(TEAM)
    assert.ok(stats.messages >= 2)
  })

  it('counts deleted messages in stats', () => {
    const stats = index.stats(TEAM)
    assert.equal(stats.deleted, 1)
    assert.ok(stats.oldest && stats.newest)
  })
})
