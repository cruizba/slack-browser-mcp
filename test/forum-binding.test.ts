import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import { ForumStore, type Binding, type StoredIdentity } from '../src/forum/binding.ts'

function identity(name: string): StoredIdentity {
  return {
    name,
    host: 'laptop',
    capabilities: [],
    registered_at: '2026-11-01T00:00:00.000Z',
  }
}

function binding(channel: string): Binding {
  return {
    workspace: 'T0TEST',
    channel,
    name: channel.toLowerCase(),
    private: false,
    since_ts: '0',
    added_at: '2026-11-01T00:00:00.000Z',
    sign: false,
    announce: true,
  }
}

async function scratch(t: { after: (fn: () => Promise<void>) => void }): Promise<ForumStore> {
  const root = await mkdtemp(join(tmpdir(), 'forum-binding-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  return new ForumStore(join(root, 'forum.json'))
}

test('two changes started together both land instead of one being dropped', async (t) => {
  const store = await scratch(t)

  await Promise.all([store.register('pi', identity('reviewer')), store.bind(binding('C0HUB'))])
  await Promise.all([store.register('claude', identity('builder')), store.ignore('gone@elsewhere')])

  const file = await store.read()
  assert.deepEqual(Object.keys(file.identities).sort(), ['claude', 'pi'])
  assert.deepEqual(
    file.channels.map((entry) => entry.channel),
    ['C0HUB'],
  )
  assert.deepEqual(file.ignored, ['gone@elsewhere'])
  assert.equal(
    existsSync(`${store.path}.lock`),
    false,
    'the lock must not outlive the write it guards',
  )
})

test('binding the same channel twice reports no second addition', async (t) => {
  const store = await scratch(t)
  const first = await store.bind(binding('C0HUB'))
  const second = await store.bind(binding('C0HUB'))

  assert.equal(first.added, true)
  assert.equal(second.added, false)
  assert.equal((await store.read()).channels.length, 1)
})
