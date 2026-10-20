/**
 * One writer at a time for the archive.
 *
 * Node has no `flock` binding, and re-execing the whole sync under `flock(1)`
 * just to hold a lock is worse than a lock directory: `mkdir` is atomic on every
 * filesystem we care about, and the owner file makes a crash recoverable by
 * checking whether that pid is still alive. The lock is released in a `finally`,
 * and a dead pid or an ancient timestamp is taken over instead of blocking the
 * archive forever.
 */

import { mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import { hostname } from 'node:os'
import { dirname, join } from 'node:path'

const MAX_AGE_MS = 6 * 60 * 60_000

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

export class SyncLock {
  private readonly dir: string
  private held = false

  constructor(archiveRoot: string) {
    this.dir = join(archiveRoot, '.sync.lock')
  }

  async acquire(): Promise<boolean> {
    if (await this.tryCreate()) return true

    const owner = await this.owner()
    if (owner && this.isStale(owner) === false) return false

    await rm(this.dir, { recursive: true, force: true })
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
