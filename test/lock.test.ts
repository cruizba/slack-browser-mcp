import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { hostname } from 'node:os'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { after, before, describe, it } from 'node:test'

import { SyncLock } from '../src/archive/lock.ts'
import { formatAge, oldestAge, teamStatus } from '../src/archive/status.ts'
import type { SyncState } from '../src/archive/store.ts'

let root: string

before(async () => {
  root = await mkdtemp(join(tmpdir(), 'slack-lock-'))
})

after(async () => {
  await rm(root, { recursive: true, force: true })
})

describe('SyncLock', () => {
  it('lets one holder in and refuses the second', async () => {
    const first = new SyncLock(root)
    const second = new SyncLock(root)
    assert.equal(await first.acquire(), true)
    assert.equal(await second.acquire(), false)

    const holder = await second.holder()
    assert.equal(holder?.pid, process.pid)

    await first.release()
    assert.equal(await second.acquire(), true)
    await second.release()
  })

  it('takes over a lock whose process is gone', async () => {
    const lock = new SyncLock(root)
    await lock.acquire()
    const ownerFile = join(root, '.sync.lock', 'owner.json')
    const owner = JSON.parse(await readFile(ownerFile, 'utf8')) as Record<string, unknown>
    await writeFile(
      ownerFile,
      JSON.stringify({ ...owner, pid: 2 ** 22 + 12345, started_at: new Date().toISOString() }),
    )

    const challenger = new SyncLock(root)
    assert.equal(await challenger.acquire(), true)
    assert.equal((await challenger.holder())?.pid, process.pid)
    await challenger.release()
  })

  it('never takes over a live lock held on another host', async () => {
    const lock = new SyncLock(root)
    await lock.acquire()
    const ownerFile = join(root, '.sync.lock', 'owner.json')
    await writeFile(
      ownerFile,
      JSON.stringify({ pid: 1, host: `${hostname()}-elsewhere`, started_at: new Date().toISOString() }),
    )
    assert.equal(await new SyncLock(root).acquire(), false)
    await lock.release()
  })

  it('takes over a lock older than the maximum age', async () => {
    const lock = new SyncLock(root)
    await lock.acquire()
    const ownerFile = join(root, '.sync.lock', 'owner.json')
    await writeFile(
      ownerFile,
      JSON.stringify({
        pid: process.pid,
        host: hostname(),
        started_at: new Date(Date.now() - 7 * 60 * 60_000).toISOString(),
      }),
    )
    assert.equal(await new SyncLock(root).acquire(), true)
    await lock.release()
  })
})

describe('archive status', () => {
  const now = new Date('2026-10-20T16:00:00.000Z')
  const state: SyncState = {
    version: 1,
    teams: {
      T0TEST: {
        last_run: '2026-10-20T15:55:00.000Z',
        channels: {
          C01: { last_sync: '2026-10-20T15:55:00.000Z', pending_threads: ['1', '2'] },
          C02: { last_sync: '2026-10-20T15:50:00.000Z', last_error: 'not_in_channel' },
        },
      },
    },
  }

  it('reports age, backlog and errors per team', () => {
    const status = teamStatus(state, 'T0TEST', now)
    assert.equal(status.age_ms, 5 * 60_000)
    assert.equal(status.channels, 2)
    assert.equal(status.pending_threads, 2)
    assert.deepEqual(status.errors, [{ channel: 'C02', error: 'not_in_channel' }])
  })

  it('falls back to the newest channel sync when last_run is missing', () => {
    const legacy: SyncState = { version: 1, teams: { T0TEST: { channels: state.teams.T0TEST!.channels } } }
    assert.equal(teamStatus(legacy, 'T0TEST', now).age_ms, 5 * 60_000)
  })

  it('uses the oldest team for the global freshness note', () => {
    const twoTeams: SyncState = {
      version: 1,
      teams: {
        ...state.teams,
        T0OTHER: { last_run: '2026-10-20T12:00:00.000Z', channels: {} },
      },
    }
    assert.equal(oldestAge(twoTeams, now), 4 * 60 * 60_000)
    assert.equal(oldestAge({ version: 1, teams: {} }, now), null)
  })

  it('formats ages for humans', () => {
    assert.equal(formatAge(null), 'never synced')
    assert.equal(formatAge(30_000), 'just now')
    assert.equal(formatAge(5 * 60_000), '5 min ago')
    assert.equal(formatAge(3 * 3_600_000), '3 h ago')
    assert.equal(formatAge(3 * 86_400_000), '3 d ago')
  })
})
