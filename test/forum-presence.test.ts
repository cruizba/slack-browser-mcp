import assert from 'node:assert/strict'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import type { ArchivedMessage } from '../src/archive/store.ts'
import { render, summaryFor, type Envelope } from '../src/forum/envelope.ts'
import { newId } from '../src/forum/id.ts'
import {
  lastPresence,
  markListening,
  markPresence,
  presenceState,
  presenceThrottleMs,
} from '../src/forum/presence.ts'
import { peerStatus, rosterFrom } from '../src/forum/roster.ts'

const NOW = new Date('2026-10-28T10:00:00Z')

function statusAt(peer: { ts: string }, now: Date) {
  return peerStatus(peer as never, { presenceSeconds: 900, ttlHours: 24, now })
}

function message(ts: string, envelope: Envelope): ArchivedMessage {
  return {
    ts,
    channel: 'C0HUB',
    text: render(summaryFor(envelope), envelope),
    synced_at: NOW.toISOString(),
  }
}

function register(ts: string, from: string): ArchivedMessage {
  const [name, host] = from.split('@') as [string, string]
  return {
    ...message(ts, {
      v: 1,
      kind: 'register',
      id: newId(),
      from,
      sent: '2026-10-28T09:00:00Z',
      name,
      host,
      capabilities: ['code'],
    }),
    user: 'U0PEER',
  }
}

function presence(ts: string, from: string, state: 'listening' | 'idle' | 'gone'): ArchivedMessage {
  const [name, host] = from.split('@') as [string, string]
  return {
    ...message(ts, {
      v: 1,
      kind: 'presence',
      id: newId(),
      from,
      sent: '2026-10-28T09:59:00Z',
      name,
      host,
      state,
      capabilities: ['code'],
    }),
    user: 'U0PEER',
  }
}

test('a presence message keeps the peer in the roster and says how alive it is', () => {
  const roster = rosterFrom([
    register('1793145600.000100', 'builder@laptop'),
    presence('1793149000.000200', 'builder@laptop', 'listening'),
  ])
  assert.equal(roster.length, 1)
  const peer = roster[0]!
  assert.equal(peer.state, 'listening')
  assert.equal(peer.ts, '1793149000.000200', 'the newest message is what the age is measured from')
  assert.equal(peer.owner, 'U0PEER')
  assert.equal(peer.capabilities.length, 1)

  const now = new Date(Number(peer.ts) * 1000 + 60_000)
  assert.equal(statusAt(peer, now), 'listening')
  const later = new Date(Number(peer.ts) * 1000 + 20 * 60_000)
  assert.equal(statusAt(peer, later), 'idle', 'a listening claim expires into idle')
  const gone = new Date(Number(peer.ts) * 1000 + 25 * 3_600_000)
  assert.equal(statusAt(peer, gone), 'out', 'a day of silence is out')
})

test('a peer that went idle stops claiming attention', () => {
  const roster = rosterFrom([
    register('1793145600.000100', 'builder@laptop'),
    presence('1793149000.000200', 'builder@laptop', 'listening'),
    presence('1793152600.000300', 'builder@laptop', 'idle'),
  ])
  const peer = roster[0]!
  assert.equal(peer.state, 'idle')
  assert.equal(statusAt(peer, new Date(Number(peer.ts) * 1000 + 60_000)), 'idle')
})

test('a peer that leaves stops being a peer, and can come back', () => {
  const retired = rosterFrom([
    register('1793145600.000100', 'builder@laptop'),
    presence('1793149000.000200', 'builder@laptop', 'listening'),
    presence('1793152600.000300', 'builder@laptop', 'gone'),
  ])
  assert.deepEqual(retired, [], 'a retired peer is not in the roster')

  const back = rosterFrom([
    register('1793145600.000100', 'builder@laptop'),
    presence('1793149000.000200', 'builder@laptop', 'gone'),
    register('1793156200.000400', 'builder@laptop'),
  ])
  assert.equal(back.length, 1)
  assert.equal(back[0]?.state, undefined, 'a register after a departure brings the name back')
  assert.equal(statusAt(back[0]!, NOW), 'idle')
})

test('the listening mark is a claim with an expiry', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'forum-presence-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  process.env.SLACK_BROWSER_MCP_CONFIG_DIR = join(root, 'config')
  process.env.XDG_DATA_HOME = join(root, 'data')

  await markListening(NOW)
  assert.equal(await presenceState(900, new Date(NOW.getTime() + 60_000)), 'listening')
  assert.equal(await presenceState(900, new Date(NOW.getTime() + 16 * 60_000)), 'idle')
})

test('the local presence mark survives between tools of the same session', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'forum-presence-mark-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  process.env.SLACK_BROWSER_MCP_CONFIG_DIR = join(root, 'config')
  process.env.XDG_DATA_HOME = join(root, 'data')

  assert.equal(await lastPresence(), undefined)
  await markPresence(NOW)
  assert.equal(await lastPresence(), NOW.getTime())
  assert.equal(presenceThrottleMs(900), 300_000, 'a third of the window')
})
