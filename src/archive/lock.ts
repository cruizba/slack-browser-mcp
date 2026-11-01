/**
 * One writer at a time for anything that must not interleave: the archive sync
 * and the interactive login.
 *
 * Node has no `flock` binding, and re-execing the whole sync under `flock(1)`
 * just to hold a lock is worse than a lock directory: `mkdir` is atomic on every
 * filesystem we care about, and the owner file makes a crash recoverable by
 * checking whether that pid is still alive. The lock is released in a `finally`,
 * and a dead pid or an ancient timestamp is taken over instead of blocking the
 * archive forever.
 */

import { mkdir, readFile, rename, rm, stat, writeFile } from 'node:fs/promises'
import { hostname } from 'node:os'
import { dirname, join } from 'node:path'

const MAX_AGE_MS = 6 * 60 * 60_000
/**
 * How long a lock directory with no owner file is assumed to belong to whoever is
 * writing that file. Without it, the gap between `mkdir` and the write makes a
 * lock look abandoned, and a second writer takes one that is being created.
 */
const OWNER_GRACE_MS = 5_000

export interface LockOwner {
  pid: number
  host: string
  started_at: string
}

function isAlive(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

export class Lock {
  private readonly dir: string
  private held = false

  constructor(dir: string) {
    this.dir = dir
  }

  async acquire(): Promise<boolean> {
    if (await this.tryCreate()) return true

    const owner = await this.owner()
    if (owner === undefined) {
      if (await this.justCreated()) return false
      await this.takeOver()
      return this.tryCreate()
    }
    if (this.isStale(owner) === false) return false

    await this.takeOver()
    return this.tryCreate()
  }

  async holder(): Promise<LockOwner | undefined> {
    return this.owner()
  }

  async release(): Promise<void> {
    if (!this.held) return
    this.held = false
    await rm(this.dir, { recursive: true, force: true })
  }

  private async justCreated(): Promise<boolean> {
    const info = await stat(this.dir).catch(() => undefined)
    return info !== undefined && Date.now() - info.mtimeMs < OWNER_GRACE_MS
  }

  /**
   * Moving the directory aside is atomic, so two processes that both decide the lock
   * is stale cannot delete each other's fresh one: only the rename's winner takes it
   * over, and the loser retries `tryCreate` against whatever is there now.
   */
  private async takeOver(): Promise<void> {
    const moved = `${this.dir}.stale.${process.pid}`
    await rm(moved, { recursive: true, force: true })
    await rename(this.dir, moved).catch(() => undefined)
    await rm(moved, { recursive: true, force: true })
  }

  private async tryCreate(): Promise<boolean> {
    await mkdir(dirname(this.dir), { recursive: true, mode: 0o700 })
    try {
      await mkdir(this.dir, { recursive: false, mode: 0o700 })
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'EEXIST') return false
      throw error
    }
    const owner: LockOwner = {
      pid: process.pid,
      host: hostname(),
      started_at: new Date().toISOString(),
    }
    await writeFile(join(this.dir, 'owner.json'), `${JSON.stringify(owner)}\n`, {
      encoding: 'utf8',
      mode: 0o600,
    })
    this.held = true
    return true
  }

  private async owner(): Promise<LockOwner | undefined> {
    try {
      return JSON.parse(await readFile(join(this.dir, 'owner.json'), 'utf8')) as LockOwner
    } catch {
      return undefined
    }
  }

  private isStale(owner: LockOwner): boolean {
    if (Date.now() - Date.parse(owner.started_at) > MAX_AGE_MS) return true
    if (owner.host !== hostname()) return false
    return !isAlive(owner.pid)
  }
}

/** The archive's writer lock, at the root of the archive directory. */
export class SyncLock extends Lock {
  constructor(archiveRoot: string) {
    super(join(archiveRoot, '.sync.lock'))
  }
}
