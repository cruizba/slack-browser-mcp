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

export type ConversationKind = 'channel' | 'private' | 'dm' | 'mpim'

export interface DirectoryChannel {
  id: string
  kind: ConversationKind
  name?: string
  /** The other side of a DM: the only handle that says who it is with. */
  user?: string
  /** Kept so listing conversations stays local instead of re-asking Slack. */
  num_members?: number
}

export interface Directory {
  version: 1
  team_id: string
  saved_at: string
  users: DirectoryUser[]
  channels: DirectoryChannel[]
  usergroups: DirectoryGroup[]
  /**
   * Number of DMs and group DMs held. A directory written before DMs were
   * indexed lacks it, and `loadDirectory` treats that as stale so the upgrade
   * lands at once instead of at the next daily refresh.
   */
  dm_count?: number
}

/** The slice of the Slack client the directory needs; keeps tests off the network. */
export interface DirectoryClient {
  listUsers(): Promise<DirectoryUser[]>
  listAllChannels(): Promise<SlackChannel[]>
  /** IMs and group DMs, which `listAllChannels` never returns. */
  listDmChannels(): Promise<SlackChannel[]>
  listUsergroups(): Promise<DirectoryGroup[]>
}

export function conversationKind(channel: SlackChannel): ConversationKind {
  if (channel.is_im) return 'dm'
  if (channel.is_mpim) return 'mpim'
  return channel.is_private ? 'private' : 'channel'
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
  const channels = new Map(
    directory.channels
      .filter((channel): channel is DirectoryChannel & { name: string } => Boolean(channel.name))
      .map((channel) => [channel.id, channel.name]),
  )
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

export function directoryChannel(
  directory: Directory | undefined,
  channelId: string,
): DirectoryChannel | undefined {
  return directory?.channels.find((channel) => channel.id === channelId)
}

/** Resolves the handle Slack builds a group DM's name from (`mpdm-<handles>-<n>`). */
export function directoryUserByHandle(
  directory: Directory | undefined,
  handle: string,
): DirectoryUser | undefined {
  const needle = handle.toLowerCase()
  return directory?.users.find((user) => user.name?.toLowerCase() === needle)
}

/**
 * Matches what a person is called around here, so `dm:Ana Ruiz` works. A name two
 * people share resolves to nobody: picking one would address the wrong person.
 */
export function directoryUserByReference(
  directory: Directory | undefined,
  reference: string,
): DirectoryUser | undefined {
  const needle = reference.trim().replace(/^@/, '').toLowerCase()
  if (!needle || !directory) return undefined
  const matches = directory.users.filter((user) =>
    [user.display_name, user.name, user.real_name].some(
      (field) => field !== undefined && field.toLowerCase() === needle,
    ),
  )
  return matches.length === 1 ? matches[0] : undefined
}

function toDirectoryChannel(channel: SlackChannel): DirectoryChannel {
  return {
    id: channel.id,
    kind: conversationKind(channel),
    ...(channel.name ? { name: channel.name } : {}),
    ...(channel.user ? { user: channel.user } : {}),
    ...(channel.num_members !== undefined ? { num_members: channel.num_members } : {}),
  }
}

function settled<T>(result: PromiseSettledResult<T[]>): T[] {
  return result.status === 'fulfilled' ? result.value : []
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
    client.listDmChannels(),
    client.listUsergroups(),
  ])
  const users = settled(results[0]!)
  const channels = [...settled(results[1]!), ...settled(results[2]!)]
  const groups = settled(results[3]!)
  if (users.length === 0 && channels.length === 0) {
    const reason = results[0]!.status === 'rejected' ? results[0]!.reason : results[1]!
    throw reason instanceof Error ? reason : new Error('Slack returned an empty directory')
  }
  // A DM shows up in both lists once Slack adds it to the channel list too, so the
  // id, not the call, decides what the registry keeps.
  const byId = new Map<string, DirectoryChannel>()
  for (const channel of channels) byId.set(channel.id, toDirectoryChannel(channel))
  const entries = [...byId.values()]
  return {
    version: 1,
    team_id: teamId,
    saved_at: now.toISOString(),
    users,
    channels: entries,
    usergroups: groups,
    dm_count: entries.filter((channel) => channel.kind === 'dm' || channel.kind === 'mpim').length,
  }
}

/**
 * Directories written before DMs were indexed hold channels as `{id, name}`.
 * Reading folds them into the current shape instead of failing on them.
 */
function normalizeChannels(value: unknown): DirectoryChannel[] {
  if (!Array.isArray(value)) return []
  const channels: DirectoryChannel[] = []
  for (const entry of value) {
    if (!entry || typeof entry !== 'object') continue
    const raw = entry as {
      id?: unknown
      kind?: unknown
      name?: unknown
      user?: unknown
      num_members?: unknown
    }
    if (typeof raw.id !== 'string') continue
    channels.push({
      id: raw.id,
      kind:
        raw.kind === 'dm' || raw.kind === 'mpim' || raw.kind === 'private' ? raw.kind : 'channel',
      ...(typeof raw.name === 'string' ? { name: raw.name } : {}),
      ...(typeof raw.user === 'string' ? { user: raw.user } : {}),
      ...(typeof raw.num_members === 'number' ? { num_members: raw.num_members } : {}),
    })
  }
  return channels
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
      const parsed = JSON.parse(raw) as {
        version?: unknown
        users?: unknown
        channels?: unknown
        dm_count?: unknown
      } & Directory
      if (parsed.version !== 1 || !Array.isArray(parsed.users)) return undefined
      return {
        ...parsed,
        channels: normalizeChannels(parsed.channels),
        ...(typeof parsed.dm_count === 'number' ? { dm_count: parsed.dm_count } : {}),
      }
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
      const fresh = Number.isFinite(savedAt) && now.getTime() - savedAt <= maxAgeMs
      // `dm_count` says the directory knows DMs; without it, `dm:<name>` labels
      // would keep showing raw user ids for a day after an upgrade.
      if (fresh && cached.dm_count !== undefined) return cached
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

/**
 * Loads the directory at most once per call site, because naming a person is the
 * only thing that needs it and most calls name a channel. A failed load is not
 * retried: the caller gets `undefined` and falls back to raw ids.
 */
export function memoizedDirectory(load: () => Promise<Directory | undefined>): {
  get(): Promise<Directory | undefined>
  person(reference: string): Promise<string | undefined>
} {
  let pending: Promise<Directory | undefined> | undefined
  const get = () => (pending ??= load())
  return {
    get,
    person: async (reference) => directoryUserByReference(await get(), reference)?.id,
  }
}
