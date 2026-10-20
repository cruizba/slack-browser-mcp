/**
 * Attachment bytes are the one part of Slack history the JSONL archive does not
 * carry, so they are stored next to it: one directory per team, one file per
 * Slack file id. Downloads happen on demand (`read_file`), never during sync, so
 * a backfill of 91 channels does not pull every screenshot ever posted.
 */

import { chmod, mkdir, readdir, rename, stat, writeFile } from 'node:fs/promises'
import { basename, join } from 'node:path'

import { archiveDir } from '../config/paths.ts'
import type { SlackFile } from '../slack/client.ts'

const MAX_NAME_CHARS = 80

export interface StoredFile {
  path: string
  name: string
  bytes: number
}

/**
 * A name taken from the message is untrusted input that becomes a path, so it is
 * reduced to a flat, printable basename: `../../etc/passwd` must not escape the
 * team directory.
 */
export function safeFileName(file: SlackFile, fileId: string): string {
  const raw = (file.name ?? file.title ?? file.mimetype ?? 'file').normalize('NFKD')
  const cleaned = raw
    .replace(/[^\w.\- ]+/g, '_')
    .replace(/\s+/g, ' ')
    .replace(/^[.\s]+/, '')
    .trim()
    .slice(0, MAX_NAME_CHARS)
  return `${fileId}-${cleaned || 'file'}`
}

export class ArchiveFileStore {
  readonly root: string

  constructor(root: string = archiveDir()) {
    this.root = root
  }

  private dir(teamId: string): string {
    if (!/^[A-Za-z0-9_-]{1,64}$/.test(teamId)) throw new Error(`Invalid team id: ${teamId}`)
    return join(this.root, teamId, 'files')
  }

  async find(teamId: string, fileId: string): Promise<StoredFile | undefined> {
    const dir = this.dir(teamId)
    let entries: string[]
    try {
      entries = await readdir(dir)
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return undefined
      throw error
    }
    const match = entries.find((entry) => entry.startsWith(`${fileId}-`))
    if (!match) return undefined
    const path = join(dir, match)
    const bytes = (await stat(path)).size
    return { path, name: match.slice(fileId.length + 1), bytes }
  }

  async save(teamId: string, file: SlackFile, bytes: Uint8Array): Promise<StoredFile> {
    const fileId = file.id ?? (file.url_private ?? '').split('/').pop()?.split('?')[0]
    if (!fileId) throw new Error('Cannot store a file without an id')
    const dir = this.dir(teamId)
    await mkdir(dir, { recursive: true, mode: 0o700 })
    const name = safeFileName(file, fileId)
    const path = join(dir, basename(name))
    const temporary = `${path}.${process.pid}.tmp`
    await writeFile(temporary, bytes, { mode: 0o600 })
    await rename(temporary, path)
    await chmod(path, 0o600)
    return { path, name, bytes: bytes.byteLength }
  }
}
