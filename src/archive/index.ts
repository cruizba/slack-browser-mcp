/**
 * SQLite index over the JSONL archive. Derived data only: `archive reindex`
 * rebuilds it from scratch, so a damaged index costs time, never history.
 */

import { chmod, mkdir } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { dirname } from 'node:path'

import { archiveIndexPath } from '../config/paths.ts'
import { redact } from '../log/redact.ts'
import { ArchiveStore, type ArchivedMessage } from './store.ts'

interface Statement {
  run(...params: unknown[]): unknown
  get(...params: unknown[]): unknown
  all(...params: unknown[]): unknown[]
}

interface Database {
  exec(sql: string): void
  prepare(sql: string): Statement
  close(): void
}

const require = createRequire(import.meta.url)
const { DatabaseSync } = require('node:sqlite') as {
  DatabaseSync: new (path: string) => Database
}

export interface SearchHit {
  team: string
  channel: string
  channel_name?: string
  ts: string
  user?: string
  username?: string
  text?: string
  thread_ts?: string
  deleted: boolean
  /** Only `list()` reads it; search ranks messages without needing the count. */
  reply_count?: number
}

export interface SearchParams {
  query: string
  team?: string
  channel?: string
  channelName?: string
  limit?: number
  includeDeleted?: boolean
}

export interface ListParams {
  team?: string
  channel?: string
  channelName?: string
  /** User ids, the shape the directory resolves a name to. */
  authors?: string[]
  oldest?: string
  latest?: string
  order?: 'oldest' | 'newest'
  limit?: number
  includeDeleted?: boolean
  includeReplies?: boolean
}

function hitFrom(row: Record<string, unknown>): SearchHit {
  return {
    team: String(row.team),
    channel: String(row.channel),
    ...(row.channel_name ? { channel_name: String(row.channel_name) } : {}),
    ts: String(row.ts),
    ...(row.user ? { user: String(row.user) } : {}),
    ...(row.username ? { username: String(row.username) } : {}),
    ...(row.text ? { text: redact(String(row.text)) } : {}),
    ...(row.thread_ts ? { thread_ts: String(row.thread_ts) } : {}),
    deleted: Number(row.deleted) === 1,
    ...(row.reply_count !== null && row.reply_count !== undefined
      ? { reply_count: Number(row.reply_count) }
      : {}),
  }
}

export interface ArchiveStats {
  messages: number
  deleted: number
  channels: number
  oldest: string | null
  newest: string | null
}

/**
 * Turns free text into a safe FTS5 query: every token becomes a quoted term
 * joined with AND, so a stray `*`, `:` or `-` cannot raise a SQL error.
 */
export function toFtsQuery(query: string): string {
  const tokens = query
    .split(/\s+/)
    .map((token) => token.replace(/["*:^()-]/g, ''))
    .filter((token) => token.length > 0)
    .filter((token) => !/^(and|or|not|near)$/i.test(token))
  if (tokens.length === 0) return ''
  return tokens.map((token) => `"${token}"`).join(' AND ')
}

export async function rebuildIndex(args: {
  store?: ArchiveStore
  index?: ArchiveIndex
} = {}): Promise<number> {
  const store = args.store ?? new ArchiveStore()
  const index = args.index ?? new ArchiveIndex()
  let total = 0
  try {
    index.reset()
    for (const team of await store.teams()) {
      for (const channel of await store.channelIds(team)) {
        const records = await store.read(team, channel)
        index.upsert(team, records)
        total += records.length
      }
    }
  } finally {
    if (!args.index) index.close()
  }
  return total
}

export class ArchiveIndex {
  private db: Database | undefined
  private readonly file: string

  constructor(file: string = archiveIndexPath()) {
    this.file = file
  }

  private open(): Database {
    if (this.db) return this.db
    const { mkdirSync, chmodSync } = require('node:fs') as typeof import('node:fs')
    mkdirSync(dirname(this.file), { recursive: true, mode: 0o700 })
    const db = new DatabaseSync(this.file)
    db.exec('PRAGMA foreign_keys = ON')
    // A read tool now writes back what it had to fetch, and that happens while a
    // sync process may be indexing: WAL lets both write without the reader's
    // whole run failing on SQLITE_BUSY.
    db.exec('PRAGMA journal_mode = WAL')
    db.exec('PRAGMA busy_timeout = 5000')
    db.exec(`
      CREATE TABLE IF NOT EXISTS messages (
        team TEXT NOT NULL,
        ts TEXT NOT NULL,
        channel TEXT NOT NULL,
        channel_name TEXT,
        user TEXT,
        username TEXT,
        text TEXT,
        thread_ts TEXT,
        subtype TEXT,
        edited_ts TEXT,
        deleted INTEGER NOT NULL DEFAULT 0,
        reply_count INTEGER,
        PRIMARY KEY (team, ts)
      );
      CREATE INDEX IF NOT EXISTS messages_channel ON messages (team, channel, ts);
      CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
        text, ts UNINDEXED, team UNINDEXED, channel UNINDEXED, tokenize='unicode61'
      );
    `)
    chmodSync(this.file, 0o600)
    this.db = db
    return db
  }

  async ensure(): Promise<void> {
    await mkdir(dirname(this.file), { recursive: true, mode: 0o700 })
    this.open()
    await chmod(this.file, 0o600)
  }

  upsert(team: string, records: ArchivedMessage[]): void {
    if (records.length === 0) return
    const db = this.open()
    const upsertMessage = db.prepare(`
      INSERT INTO messages (team, ts, channel, channel_name, user, username, text, thread_ts, subtype, edited_ts, deleted, reply_count)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT (team, ts) DO UPDATE SET
        channel = excluded.channel,
        channel_name = coalesce(excluded.channel_name, messages.channel_name),
        user = coalesce(excluded.user, messages.user),
        username = coalesce(excluded.username, messages.username),
        text = coalesce(excluded.text, messages.text),
        thread_ts = coalesce(excluded.thread_ts, messages.thread_ts),
        subtype = coalesce(excluded.subtype, messages.subtype),
        edited_ts = coalesce(excluded.edited_ts, messages.edited_ts),
        deleted = excluded.deleted,
        reply_count = coalesce(excluded.reply_count, messages.reply_count)
    `)
    const dropFromFts = db.prepare('DELETE FROM messages_fts WHERE team = ? AND ts = ?')
    const addToFts = db.prepare('INSERT INTO messages_fts (text, ts, team, channel) VALUES (?, ?, ?, ?)')

    db.exec('BEGIN')
    try {
      for (const record of records) {
        const deleted = record.deleted_at ? 1 : 0
        upsertMessage.run(
          team,
          record.ts,
          record.channel,
          record.channel_name ?? null,
          record.user ?? null,
          record.username ?? null,
          record.text ?? null,
          record.thread_ts ?? null,
          record.subtype ?? null,
          record.edited_ts ?? null,
          deleted,
          record.reply_count ?? null,
        )
        dropFromFts.run(team, record.ts)
        if (record.text) addToFts.run(record.text, record.ts, team, record.channel)
      }
      db.exec('COMMIT')
    } catch (error) {
      db.exec('ROLLBACK')
      throw error
    }
  }

  search(params: SearchParams): SearchHit[] {
    const match = toFtsQuery(params.query)
    if (match === '') return []
    const db = this.open()

    const where = ['messages_fts MATCH ?']
    const args: unknown[] = [match]
    if (params.team) {
      where.push('messages_fts.team = ?')
      args.push(params.team)
    }
    if (params.channel) {
      where.push('messages_fts.channel = ?')
      args.push(params.channel)
    }
    if (params.channelName) {
      where.push('lower(m.channel_name) = lower(?)')
      args.push(params.channelName)
    }
    if (!params.includeDeleted) where.push('m.deleted = 0')
    args.push(params.limit ?? 20)

    const rows = db
      .prepare(
        `SELECT m.team, m.channel, m.channel_name, m.ts, m.user, m.username, m.text,
                m.thread_ts, m.deleted
         FROM messages_fts
         JOIN messages m ON m.team = messages_fts.team AND m.ts = messages_fts.ts
         WHERE ${where.join(' AND ')}
         ORDER BY bm25(messages_fts)
         LIMIT ?`,
      )
      .all(...args) as Array<Record<string, unknown>>

    return rows.map(hitFrom)
  }

  /**
   * Timeline across every conversation. Answers what FTS cannot ("the oldest
   * messages", "the first one in #x"): no words to match, just an order.
   */
  list(params: ListParams): SearchHit[] {
    const db = this.open()
    const where: string[] = []
    const args: unknown[] = []
    if (params.team) {
      where.push('team = ?')
      args.push(params.team)
    }
    if (params.channel) {
      where.push('channel = ?')
      args.push(params.channel)
    }
    if (params.channelName) {
      where.push('lower(channel_name) = lower(?)')
      args.push(params.channelName)
    }
    if (params.authors && params.authors.length > 0) {
      where.push(`user IN (${params.authors.map(() => '?').join(', ')})`)
      args.push(...params.authors)
    }
    if (!params.includeDeleted) where.push('deleted = 0')
    // A reply lands on its parent's day, so a timeline of what was said wants roots.
    if (!params.includeReplies) where.push('thread_ts IS NULL')
    if (params.oldest !== undefined) {
      where.push('CAST(ts AS REAL) > CAST(? AS REAL)')
      args.push(params.oldest)
    }
    if (params.latest !== undefined) {
      where.push('CAST(ts AS REAL) < CAST(? AS REAL)')
      args.push(params.latest)
    }
    args.push(params.limit ?? 20)

    const rows = db
      .prepare(
        `SELECT team, channel, channel_name, ts, user, username, text, thread_ts,
                deleted, reply_count
         FROM messages
         ${where.length > 0 ? `WHERE ${where.join(' AND ')}` : ''}
         ORDER BY CAST(ts AS REAL) ${params.order === 'newest' ? 'DESC' : 'ASC'}
         LIMIT ?`,
      )
      .all(...args) as Array<Record<string, unknown>>

    return rows.map(hitFrom)
  }

  stats(team?: string): ArchiveStats {
    const db = this.open()
    const filter = team ? 'WHERE team = ?' : ''
    const args = team ? [team] : []
    const row = db
      .prepare(
        `SELECT count(*) AS messages,
                sum(CASE WHEN deleted = 1 THEN 1 ELSE 0 END) AS deleted,
                count(DISTINCT channel) AS channels,
                min(ts) AS oldest,
                max(ts) AS newest
         FROM messages ${filter}`,
      )
      .get(...args) as Record<string, unknown>
    return {
      messages: Number(row.messages ?? 0),
      deleted: Number(row.deleted ?? 0),
      channels: Number(row.channels ?? 0),
      oldest: row.oldest ? String(row.oldest) : null,
      newest: row.newest ? String(row.newest) : null,
    }
  }

  reset(): void {
    const db = this.open()
    db.exec('DELETE FROM messages; DELETE FROM messages_fts;')
  }

  close(): void {
    this.db?.close()
    this.db = undefined
  }
}
