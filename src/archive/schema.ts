/**
 * The archive outlives any single build of this tool: a systemd timer, an MCP
 * server and a shell can all be running different versions against the same
 * files. So the layout carries a version, every release that changes it ships a
 * migration, and an upgrade rewrites what it must instead of asking the user to
 * delete history.
 *
 * Two rules keep that safe: a migration is idempotent (a crash halfway converges
 * on the next run) and it only rewrites a channel file when a line actually
 * changes, so an untouched archive is never rewritten.
 */

import { appendFile, chmod, mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises'
import { join } from 'node:path'

import { archiveDir, archiveIndexPath } from '../config/paths.ts'
import { SchemaError } from '../errors.ts'
import { rebuildIndex } from './index.ts'
import { SyncLock } from './lock.ts'
import { ArchiveStore } from './store.ts'

/** Bump when a reader that predates the change would misread stored data. */
export const ARCHIVE_SCHEMA_VERSION = 2

export interface SchemaMarker {
  version: number
  updated_at: string
}

export interface SchemaStatus {
  from: number
  to: number
  applied: number[]
  /** Another process held the sync lock; reads still work. */
  locked?: boolean
}

export interface MigrationContext {
  root: string
  log: (line: string) => void
}

export interface Migration {
  /** The version this migration produces. */
  version: number
  reason: string
  run(context: MigrationContext): Promise<void>
}

function markerPath(root: string): string {
  return join(root, 'schema.json')
}

/**
 * A record with `files: ["name"]` predates file ids, and Slack sends `null` for
 * absent optional fields where this code expects the key to be missing.
 */
function normalizeRecord(record: Record<string, unknown>): Record<string, unknown> {
  const next: Record<string, unknown> = { ...record }
  if (Array.isArray(next.files)) {
    const files = next.files.map((entry) =>
      typeof entry === 'string' ? { name: entry } : entry,
    )
    if (files.length > 0) next.files = files
    else delete next.files
  }
  for (const [key, value] of Object.entries(next)) {
    if (value === null) delete next[key]
  }
  return next
}

async function migrateRecords(context: MigrationContext): Promise<void> {
  const store = new ArchiveStore(context.root)
  for (const team of await store.teams()) {
    for (const channel of await store.channelIds(team)) {
      const file = store.channelFile(team, channel)
      const raw = await readFile(file, 'utf8')
      const lines = raw.split('\n').filter((line) => line.trim().length > 0)
      const corrupt: string[] = []
      let changed = false
      const rewritten: string[] = []

      for (const line of lines) {
        let parsed: Record<string, unknown>
        try {
          parsed = JSON.parse(line) as Record<string, unknown>
        } catch {
          corrupt.push(line)
          changed = true
          continue
        }
        const normalized = normalizeRecord(parsed)
        if (JSON.stringify(parsed) !== JSON.stringify(normalized)) changed = true
        rewritten.push(JSON.stringify(normalized))
      }

      if (!changed) continue
      if (corrupt.length > 0) {
        await appendFile(`${file}.corrupt`, `${corrupt.join('\n')}\n`, {
          encoding: 'utf8',
          mode: 0o600,
        })
        context.log(`${file}: ${corrupt.length} unreadable line(s) moved to ${file}.corrupt`)
      }

      const temporary = `${file}.${process.pid}.tmp`
      await writeFile(temporary, `${rewritten.join('\n')}\n`, { encoding: 'utf8', mode: 0o600 })
      await rename(temporary, file)
      await chmod(file, 0o600)
      context.log(`${file}: ${rewritten.length} records normalised`)
    }
  }

  // The FTS index is derived data, and `sync` only adds new rows to it, so a
  // migration has to rebuild it: dropping it would leave search blind until
  // someone remembered `archive reindex`.
  await rm(archiveIndexPath(), { force: true })
  const indexed = await rebuildIndex({ store })
  context.log(`rebuilt the FTS index: ${indexed} records`)
}

export const migrations: Migration[] = [
  {
    version: 2,
    reason: 'attachments carry their id and absent fields are omitted',
    run: migrateRecords,
  },
]

async function readMarker(root: string): Promise<SchemaMarker | undefined> {
  try {
    const parsed = JSON.parse(await readFile(markerPath(root), 'utf8')) as SchemaMarker
    return typeof parsed.version === 'number' ? parsed : undefined
  } catch {
    return undefined
  }
}

async function writeMarker(root: string, version: number): Promise<void> {
  await mkdir(root, { recursive: true, mode: 0o700 })
  const file = markerPath(root)
  const temporary = `${file}.${process.pid}.tmp`
  await writeFile(
    temporary,
    `${JSON.stringify({ version, updated_at: new Date().toISOString() })}\n`,
    { encoding: 'utf8', mode: 0o600 },
  )
  await rename(temporary, file)
  await chmod(file, 0o600)
}

/**
 * Data written before the marker existed is version 1: the layout has not
 * changed since, it just had no way to say so.
 */
async function currentVersion(root: string): Promise<number> {
  const marker = await readMarker(root)
  if (marker) return marker.version
  const store = new ArchiveStore(root)
  for (const team of await store.teams()) {
    if ((await store.channelIds(team)).length > 0) return 1
  }
  return ARCHIVE_SCHEMA_VERSION
}

export async function ensureArchiveSchema(args: {
  root?: string
  log?: (line: string) => void
} = {}): Promise<SchemaStatus> {
  const root = args.root ?? archiveDir()
  const log = args.log ?? (() => {})
  const from = await currentVersion(root)

  if (from > ARCHIVE_SCHEMA_VERSION) {
    throw new SchemaError(
      `The archive at ${root} was written with schema ${from}, newer than this build understands (${ARCHIVE_SCHEMA_VERSION}).`,
      'Update slack-browser before reading or syncing: a newer version wrote that data.',
    )
  }

  if (from === ARCHIVE_SCHEMA_VERSION) {
    if (!(await readMarker(root))) await writeMarker(root, from)
    return { from, to: from, applied: [] }
  }

  // The same lock the sync holds: a migration rewrites channel files, so it must
  // not run next to an append.
  const lock = new SyncLock(root)
  if (!(await lock.acquire())) return { from, to: from, applied: [], locked: true }

  const applied: number[] = []
  try {
    for (const migration of migrations.filter((item) => item.version > from)) {
      if (migration.version > ARCHIVE_SCHEMA_VERSION) break
      log(`migrating archive schema ${migration.version - 1} -> ${migration.version}: ${migration.reason}`)
      await migration.run({ root, log })
      applied.push(migration.version)
    }
    await writeMarker(root, ARCHIVE_SCHEMA_VERSION)
  } finally {
    await lock.release()
  }

  return { from, to: ARCHIVE_SCHEMA_VERSION, applied }
}
