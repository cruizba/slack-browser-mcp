/**
 * A local registry of who is who: user ids, channel ids and user-group ids, so a
 * render can say `@ana` and `#random` instead of `U02ANA` and `C01RANDOM`.
 *
 * It is cached on disk because resolution must also work offline (the archive is
 * searchable with no network) and because `users.list` is far cheaper once a day
 * than one `users.info` per author per read.
 */

import { chmod, mkdir, readFile, rename, writeFile } from 'node:fs/promises'
import { join } from 'node:path'

import { archiveDir } from '../config/paths.ts'
import type { SlackChannel } from './client.ts'
import type { ReferenceScope } from './references.ts'

export interface DirectoryUser {
  id: string
  name?: string
  real_name?: string
  display_name?: string
  deleted?: boolean
  is_bot?: boolean
}

export interface DirectoryGroup {
  id: string
  handle?: string
  name?: string
}

export interface Directory {
  version: 1
  team_id: string
  saved_at: string
  users: DirectoryUser[]
  channels: Array<{ id: string; name: string }>
  usergroups: DirectoryGroup[]
}

/** The slice of the Slack client the directory needs; keeps tests off the network. */
export interface DirectoryClient {
  listUsers(): Promise<DirectoryUser[]>
  listAllChannels(): Promise<SlackChannel[]>
  listUsergroups(): Promise<DirectoryGroup[]>
}

export const DIRECTORY_MAX_AGE_MS = 24 * 60 * 60_000

export function userLabel(user: DirectoryUser | undefined): string | undefined {
  if (!user) return undefined
  return user.display_name || user.real_name || user.name || user.id
}

function groupLabel(group: DirectoryGroup): string {
  return group.handle || group.name || group.id
}

export function directoryScope(directory: Directory | undefined): ReferenceScope | undefined {
  if (!directory) return undefined
  const users = new Map(directory.users.map((user) => [user.id, userLabel(user) ?? user.id]))
  const channels = new Map(directory.channels.map((channel) => [channel.id, channel.name]))
  const groups = new Map(directory.usergroups.map((group) => [group.id, groupLabel(group)]))
  return {
    user: (id) => users.get(id),
    channel: (id) => channels.get(id),
    usergroup: (id) => groups.get(id),
  }
}

export function directoryUser(directory: Directory | undefined, userId: string): string | undefined {
  if (!directory) return undefined
  return userLabel(directory.users.find((user) => user.id === userId))
}

/**
 * A directory is a cache of slowly changing data, so a failed lookup degrades to
 * whatever it already had: a missing user group list must not cost the user's
 * names too.
 */
export async function fetchDirectory(
  client: DirectoryClient,
  teamId: string,
  now: Date = new Date(),
): Promise<Directory> {
  const results = await Promise.allSettled([
    client.listUsers(),
    client.listAllChannels(),
    client.listUsergroups(),
  ])
  const users = results[0].status === 'fulfilled' ? results[0].value : []
  const channels = results[1].status === 'fulfilled' ? results[1].value : []
  const groups = results[2].status === 'fulfilled' ? results[2].value : []
  if (users.length === 0 && channels.length === 0) {
    const reason = results[0].status === 'rejected' ? results[0].reason : results[1]
    throw reason instanceof Error ? reason : new Error('Slack returned an empty directory')
  }
  return {
    version: 1,
    team_id: teamId,
    saved_at: now.toISOString(),
    users,
    channels: channels
      .filter((channel): channel is SlackChannel & { name: string } => Boolean(channel.name))
      .map((channel) => ({ id: channel.id, name: channel.name })),
    usergroups: groups,
  }
}

export class DirectoryStore {
  readonly root: string

  constructor(root: string = archiveDir()) {
    this.root = root
  }

  private path(teamId: string): string {
    if (!/^[A-Za-z0-9_-]{1,64}$/.test(teamId)) throw new Error(`Invalid team id: ${teamId}`)
    return join(this.root, teamId, 'directory.json')
  }

  async read(teamId: string): Promise<Directory | undefined> {
    let raw: string
    try {
      raw = await readFile(this.path(teamId), 'utf8')
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return undefined
      throw error
    }
    try {
      const parsed = JSON.parse(raw) as Directory
      if (parsed.version !== 1 || !Array.isArray(parsed.users)) return undefined
      return parsed
    } catch {
      return undefined
    }
  }

  async write(directory: Directory): Promise<string> {
    const file = this.path(directory.team_id)
    await mkdir(join(this.root, directory.team_id), { recursive: true, mode: 0o700 })
    const temporary = `${file}.${process.pid}.tmp`
    await writeFile(temporary, `${JSON.stringify(directory)}\n`, {
      encoding: 'utf8',
      mode: 0o600,
    })
    await rename(temporary, file)
    await chmod(file, 0o600)
    return file
  }
}

/**
 * Returns the cached directory, refreshing it when it is missing or older than
 * `maxAgeMs`. A refresh that fails falls back to the stale copy, because an old
 * name beats a raw id.
 */
export async function loadDirectory(args: {
  client: DirectoryClient
  teamId: string
  store?: DirectoryStore
  maxAgeMs?: number
  force?: boolean
  now?: Date
}): Promise<Directory | undefined> {
  const store = args.store ?? new DirectoryStore()
  const cached = await store.read(args.teamId)
  const maxAgeMs = args.maxAgeMs ?? DIRECTORY_MAX_AGE_MS
  const now = args.now ?? new Date()

  if (!args.force) {
    if (cached) {
      const savedAt = Date.parse(cached.saved_at)
      if (Number.isFinite(savedAt) && now.getTime() - savedAt <= maxAgeMs) return cached
    }
  }

  try {
    const directory = await fetchDirectory(args.client, args.teamId, now)
    await store.write(directory)
    return directory
  } catch {
    return cached
  }
}
