/**
 * Append-only archive of Slack messages: one JSONL file per channel.
 *
 * Messages are immutable in Slack apart from edits and deletions, so the file is
 * an event log where the last line for a `ts` wins. A deletion is a line with
 * `deleted_at`, never a removed line: losing the fact that a message existed
 * would make the archive lie about history.
 */

import { appendFile, chmod, mkdir, readFile, readdir, rename, writeFile } from 'node:fs/promises'
import { join } from 'node:path'

import { archiveDir } from '../config/paths.ts'

export interface ArchivedFile {
  id?: string
  name?: string
  title?: string
  mimetype?: string
  size?: number
}

export interface ArchivedMessage {
  ts: string
  channel: string
  channel_name?: string
  /**
   * `search` marks a message filed from Slack search rather than from a channel
   * fetch: it is searchable, but it is not a contiguous window, so channel
   * history must not be served from it.
   */
  source?: 'search'
  user?: string
  username?: string
  bot_id?: string
  text?: string
  thread_ts?: string
  subtype?: string
  edited_ts?: string
  reply_count?: number
  files?: ArchivedFile[]
  synced_at: string
  deleted_at?: string
}

export interface ChannelState {
  newest_ts?: string
  oldest_ts?: string
  last_sync?: string
  last_reconcile?: string
  last_error?: string
  pending_threads?: string[]
}

export interface SyncState {
  version: 1
  teams: Record<string, { last_run?: string; channels: Record<string, ChannelState> }>
}

function emptyState(): SyncState {
  return { version: 1, teams: {} }
}

/**
 * Archives written before files carried ids hold bare names, and reading them
 * must not need a migration step: both shapes fold into the object form.
 */
function normalizeFiles(value: unknown): ArchivedFile[] | undefined {
  if (!Array.isArray(value) || value.length === 0) return undefined
  return value.map((entry) =>
    typeof entry === 'string' ? { name: entry } : (entry as ArchivedFile),
  )
}

function fold(records: ArchivedMessage[]): ArchivedMessage[] {
  const byTs = new Map<string, ArchivedMessage>()
  for (const record of records) {
    const existing = byTs.get(record.ts)
    if (!existing) {
      byTs.set(record.ts, record)
      continue
    }
    const merged: ArchivedMessage = { ...existing, ...record }
    if (!record.deleted_at) delete merged.deleted_at
    // Same rule for the origin: a later line without `source` means a channel
    // fetch confirmed the message, and the marker must not outlive it.
    if (!record.source) delete merged.source
    byTs.set(record.ts, merged)
  }
  return [...byTs.values()].sort((left, right) => Number(left.ts) - Number(right.ts))
}

export class ArchiveStore {
  readonly root: string

  constructor(root: string = archiveDir()) {
    this.root = root
  }

  private statePath(): string {
    return join(this.root, 'state.json')
  }

  private teamDir(teamId: string): string {
    if (!/^[A-Za-z0-9_-]{1,64}$/.test(teamId)) throw new Error(`Invalid team id: ${teamId}`)
    return join(this.root, teamId)
  }

  channelFile(teamId: string, channelId: string): string {
    if (!/^[A-Za-z0-9_-]{1,64}$/.test(channelId)) throw new Error(`Invalid channel id: ${channelId}`)
    return join(this.teamDir(teamId), `${channelId}.jsonl`)
  }

  async append(teamId: string, channelId: string, records: ArchivedMessage[]): Promise<void> {
    if (records.length === 0) return
    const dir = this.teamDir(teamId)
    await mkdir(dir, { recursive: true, mode: 0o700 })
    await chmod(dir, 0o700)
    const file = this.channelFile(teamId, channelId)
    const payload = records.map((record) => `${JSON.stringify(record)}\n`).join('')
    await appendFile(file, payload, { encoding: 'utf8', mode: 0o600 })
    await chmod(file, 0o600)
  }

  async read(teamId: string, channelId: string): Promise<ArchivedMessage[]> {
    let raw: string
    try {
      raw = await readFile(this.channelFile(teamId, channelId), 'utf8')
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return []
      throw error
    }
    return fold(
      raw
        .split('\n')
        .filter((line) => line.trim().length > 0)
        .map((line) => {
          const parsed = JSON.parse(line) as ArchivedMessage & { files?: unknown }
          const files = normalizeFiles(parsed.files)
          return files ? { ...parsed, files } : (parsed as ArchivedMessage)
        }),
    )
  }

  async channelIds(teamId: string): Promise<string[]> {
    try {
      const entries = await readdir(this.teamDir(teamId))
      return entries
        .filter((entry) => entry.endsWith('.jsonl'))
        .map((entry) => entry.slice(0, -'.jsonl'.length))
        .sort()
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return []
      throw error
    }
  }

  async teams(): Promise<string[]> {
    try {
      const entries = await readdir(this.root, { withFileTypes: true })
      return entries
        .filter((entry) => entry.isDirectory() && /^[A-Za-z0-9_-]{1,64}$/.test(entry.name))
        .map((entry) => entry.name)
        .sort()
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return []
      throw error
    }
  }

  async loadState(): Promise<SyncState> {
    try {
      const parsed = JSON.parse(await readFile(this.statePath(), 'utf8')) as SyncState
      return parsed.version === 1 ? parsed : emptyState()
    } catch (error) {
      const code = (error as NodeJS.ErrnoException).code
      if (code === 'ENOENT') return emptyState()
      throw error
    }
  }

  async saveState(state: SyncState): Promise<void> {
    await mkdir(this.root, { recursive: true, mode: 0o700 })
    await chmod(this.root, 0o700)
    const temporary = `${this.statePath()}.${process.pid}.tmp`
    await writeFile(temporary, `${JSON.stringify(state, null, 2)}\n`, {
      encoding: 'utf8',
      mode: 0o600,
    })
    await rename(temporary, this.statePath())
  }
}
