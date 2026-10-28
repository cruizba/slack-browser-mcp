import assert from 'node:assert/strict'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import type { ArchivedMessage } from '../src/archive/store.ts'
import { render, summaryFor, type Envelope } from '../src/forum/envelope.ts'
import { newId } from '../src/forum/id.ts'
import { DEFAULT_LIMITS } from '../src/forum/limits.ts'
import {
  beaconThrottleMs,
  lastBeacon,
  lastClaim,
  markBeacon,
  markClaim,
} from '../src/forum/presence.ts'
import { humanStatus, machineStatus, rosterFrom } from '../src/forum/roster.ts'

const NOW = new Date('2026-10-28T10:00:00Z')

function message(ts: string, envelope: Envelope, user = 'U0PEER'): ArchivedMessage {
  return {
    ts,
    channel: 'C0HUB',
    text: render(summaryFor(envelope), envelope),
    synced_at: NOW.toISOString(),
    user,
  }
}

function split(from: string): [string, string] {
  const [name, host] = from.split('@') as [string, string]
  return [name, host]
}

function register(ts: string, from: string): ArchivedMessage {
  const [name, host] = split(from)
  return message(ts, {
    v: 1,
    kind: 'register',
    id: newId(),
    from,
    sent: '2026-10-28T09:00:00Z',
    name,
    host,
    capabilities: ['code'],
  })
}

function beacon(ts: string, from: string): ArchivedMessage {
  const [name, host] = split(from)
  return message(ts, {
    v: 1,
    kind: 'beacon',
    id: newId(),
    from,
    sent: '2026-10-28T09:00:00Z',
    name,
    host,
  })
}

function presence(
  ts: string,
  from: string,
  state: 'here' | 'away' | 'gone' | 'listening' | 'idle',
  note?: string,
): ArchivedMessage {
  const [name, host] = split(from)
  return message(ts, {
    v: 1,
    kind: 'presence',
    id: newId(),
    from,
    sent: '2026-10-28T09:00:00Z',
    name,
    host,
    state,
    ...(note ? { note } : {}),
  })
}

function task(ts: string, from: string, to: string): ArchivedMessage {
  return message(ts, {
    v: 1,
    kind: 'task',
    id: newId(),
    from,
    to,
    sent: '2026-10-28T09:00:00Z',
    title: 'una cosa',
    body: 'hazla',
  })
}

function statuses(peer: ReturnType<typeof rosterFrom>[number], at: Date) {
  return {
    machine: machineStatus(peer, { ttlHours: 72, now: at }),
    human: humanStatus(peer, { hereSeconds: 3600, ttlHours: 72, now: at }),
  }
}

test('a beacon keeps the machine up and claims nothing about the person', () => {
  const roster = rosterFrom([
    register('1793145600.000100', 'builder@laptop'),
    beacon('1793149100.000200', 'builder@laptop'),
  ])
  assert.equal(roster.length, 1)
  const peer = roster[0]!
  assert.equal(peer.state, undefined, 'a beacon is not a presence claim')
  assert.equal(peer.ts, '1793149100.000200')
  assert.deepEqual(statuses(peer, new Date(NOW)), { machine: 'up', human: 'unknown' })
  assert.equal(peer.capabilities.length, 1, 'a beacon does not erase what the register said')
  assert.equal(peer.owner, 'U0PEER')
})

test('a here claim expires on its own, and activity keeps it fresh', () => {
  const roster = rosterFrom([
    register('1793145600.000100', 'builder@laptop'),
    presence('1793149000.000200', 'builder@laptop', 'here'),
  ])
  const peer = roster[0]!
  const claimed = new Date(Number(peer.ts) * 1000 + 60_000)
  assert.equal(humanStatus(peer, { hereSeconds: 3600, ttlHours: 72, now: claimed }), 'here')

  const later = new Date(Number(peer.ts) * 1000 + 2 * 3_600_000)
  assert.equal(humanStatus(peer, { hereSeconds: 3600, ttlHours: 72, now: later }), 'unknown')
  assert.equal(machineStatus(peer, { ttlHours: 72, now: later }), 'up', 'the machine has not gone')

  const busy = rosterFrom([
    register('1793145600.000100', 'builder@laptop'),
    presence('1793149000.000200', 'builder@laptop', 'here'),
    task('1793154400.000300', 'builder@laptop', 'reviewer@laptop'),
  ])
  const refreshed = new Date(1793154400 * 1000 + 10 * 60_000)
  assert.equal(
    humanStatus(busy[0]!, { hereSeconds: 3600, ttlHours: 72, now: refreshed }),
    'here',
    'a message the session writes is proof of life, no second presence needed',
  )
})

test('away stands until replaced and carries the note', () => {
  const roster = rosterFrom([
    register('1793145600.000100', 'builder@laptop'),
    presence('1793149000.000200', 'builder@laptop', 'away', 'back Monday'),
  ])
  const peer = roster[0]!
  assert.equal(peer.state, 'away')
  assert.equal(peer.note, 'back Monday')
  const muchLater = new Date(Number(peer.ts) * 1000 + 5 * 3_600_000)
  assert.equal(humanStatus(peer, { hereSeconds: 3600, ttlHours: 72, now: muchLater }), 'away')
})

test('the old listening heartbeat keeps the peer alive but claims no person', () => {
  const roster = rosterFrom([
    register('1793145600.000100', 'builder@laptop'),
    presence('1793149000.000200', 'builder@laptop', 'listening'),
  ])
  const peer = roster[0]!
  assert.equal(peer.state, undefined)
  assert.deepEqual(statuses(peer, new Date(NOW)), { machine: 'up', human: 'unknown' })
})

test('a message from a peer nobody registered does not invent a peer', () => {
  const roster = rosterFrom([task('1793149000.000200', 'stranger@elsewhere', 'builder@laptop')])
  assert.deepEqual(roster, [])
})

test('a peer that leaves stops being a peer, and can come back', () => {
  const retired = rosterFrom([
    register('1793145600.000100', 'builder@laptop'),
    presence('1793149000.000200', 'builder@laptop', 'here'),
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
})

test('the beacon mark throttles the poller and the claim mark the session', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'forum-liveness-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  process.env.SLACK_BROWSER_MCP_CONFIG_DIR = join(root, 'config')
  process.env.XDG_DATA_HOME = join(root, 'data')

  assert.equal(await lastBeacon(), undefined)
  await markBeacon(NOW)
  assert.equal(await lastBeacon(), NOW.getTime())
  assert.equal(
    beaconThrottleMs(DEFAULT_LIMITS.presence_seconds),
    24 * 3_600_000,
    'a day between beacons by default',
  )

  assert.equal(await lastClaim(), undefined)
  await markClaim('away', NOW)
  assert.deepEqual(await lastClaim(), { state: 'away', at: NOW.getTime() })
})

test('the roster prints the machine and the person as two columns', async () => {
  const { renderPeers } = await import('../src/forum/render.ts')
  const roster = rosterFrom([
    register('1793145600.000100', 'builder@laptop'),
    task(String(NOW.getTime() / 1000), 'builder@laptop', 'reviewer@laptop'),
    register('1793145600.000200', 'planner@alice-pc'),
    presence('1793149000.000300', 'planner@alice-pc', 'away', 'back Monday'),
    register('1792699200.000400', 'old@lab-pc'),
    beacon('1792699200.000500', 'old@lab-pc'),
  ])
  const view = {
    binding: { workspace: 'T0TEST', channel: 'C0HUB', name: 'agent-hub', private: true },
    key: 'T0TEST:C0HUB',
    roster,
    people: new Map([['U0PEER', 'bob']]),
    ledger: { tasks: [], unreadable: 0, orphans: 0 },
    decisions: new Map(),
  }
  const text = renderPeers(view as never, NOW, DEFAULT_LIMITS).join('\n')
  assert.match(text, /builder@laptop · bob \[code\] — up · here, active now/)
  assert.match(text, /planner@alice-pc · bob \[code\] — up · away — back Monday/)
  assert.match(text, /old@lab-pc · bob \[code\] — out, last seen \d+d ago/)
})
