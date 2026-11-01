/**
 * Archive sync: two passes per channel plus a thread backlog.
 *
 * Incremental (`oldest=<newest cached>`) is cheap but blind to edits and
 * deletions, so a second pass re-reads a recent window once a day and marks
 * whatever disappeared as deleted instead of erasing it.
 *
 * Replies never appear in channel history, only as a parent with `reply_count`,
 * so each thread costs one `conversations.replies`. Parents whose replies are
 * missing are kept in a persisted backlog and drained across runs; that is what
 * makes a first backfill possible without one enormous burst.
 */

import { readFile } from 'node:fs/promises'

import type { SlackAccount } from '../auth/store.ts'
import { forumFileOf } from '../forum/carrier.ts'
import type { HistoryPage, SlackChannel, SlackFile, SlackMessage } from '../slack/client.ts'
import { ArchiveFileStore } from './files.ts'
import { ArchiveIndex } from './index.ts'
import { SyncLock } from './lock.ts'
import { changed, channelName, toRecord } from './record.ts'
import { ArchiveStore, type ArchivedMessage, type ChannelState } from './store.ts'

const PAGE_SIZE = 200
/**
 * A loop guard, not a budget: the deadline is what bounds a run. Paging stops
 * early only when Slack repeats a cursor, and that is reported as an incomplete
 * window, because a walk that stopped halfway is not history.
 */
const MAX_PAGES = 1000
const PENDING_THREAD_CAP = 500
const RECONCILE_INTERVAL_MS = 86_400_000

/**
 * How far back a workspace is read the first time it is seen. Deeper costs pages
 * Slack ends early when a plan keeps less history, and shallower leaves a gap that
 * only `--full --since` closes; 90 days is where the workspaces handled so far stop
 * serving channel history.
 */
export const DEFAULT_BACKFILL_DAYS = 90

export interface SyncClient {
  listMyChannels(): Promise<SlackChannel[]>
  history(params: {
    channel: string
    limit?: number
    oldest?: string
    latest?: string
    cursor?: string
  }): Promise<HistoryPage>
  repliesPage(params: {
    channel: string
    thread_ts: string
    limit?: number
    cursor?: string
  }): Promise<HistoryPage>
  /** Bytes of an attachment, for the envelopes that travel as `forum-*` files. */
  downloadFile?(file: SlackFile): Promise<Uint8Array>
}

export interface SyncOptions {
  full?: boolean
  backfillDays?: number
  reconcileDays?: number
  deadlineMs?: number
  maxThreadsPerChannel?: number
  /**
   * Always re-read this window, even when the cursor is newer. Replies never show
   * up in channel history, so a parent older than the cursor is only seen again
   * when a read reaches back to it; the forum uses this to keep live tasks current.
   */
  refreshDays?: number
  /** Restrict the run to these channel ids; the forum polls one or two at a time. */
  channels?: readonly string[]
  now?: Date
  onProgress?: (line: string) => void
}

export interface SyncReport {
  team_id: string
  channels_seen: number
  channels_synced: number
  messages_added: number
  replies_added: number
  threads_fetched: number
  deletions_marked: number
  errors: Array<{ channel: string; error: string }>
  truncated: boolean
  skipped?: 'locked'
  duration_ms: number
}

interface ResolvedOptions {
  full: boolean
  backfillDays: number
  reconcileDays: number
  maxThreadsPerChannel: number
  refreshDays?: number
  now: Date
}

function slackTs(date: Date): string {
  return (date.getTime() / 1000).toFixed(6)
}

function daysAgo(now: Date, days: number): string {
  return slackTs(new Date(now.getTime() - days * 86_400_000))
}

/** The older of two Slack ts values, so one history call answers both questions. */
function olderTs(left: string, right: string | undefined): string {
  if (right === undefined) return left
  return Number(right) < Number(left) ? right : left
}

export async function syncAccount(args: {
  account: SlackAccount
  client: SyncClient
  store?: ArchiveStore
  index?: ArchiveIndex
  options?: SyncOptions
}): Promise<SyncReport> {
  const options = args.options ?? {}
  const resolved: ResolvedOptions = {
    full: options.full ?? false,
    backfillDays: options.backfillDays ?? DEFAULT_BACKFILL_DAYS,
    reconcileDays: options.reconcileDays ?? 7,
    maxThreadsPerChannel: options.maxThreadsPerChannel ?? 10,
    ...(options.refreshDays !== undefined ? { refreshDays: options.refreshDays } : {}),
    now: options.now ?? new Date(),
  }
  // Real clock for the budget; `now` only stamps records, and tests inject a fixed one.
  const deadline = Date.now() + (options.deadlineMs ?? 10 * 60_000)
  const progress = options.onProgress ?? (() => {})
  const store = args.store ?? new ArchiveStore()
  const index = args.index ?? new ArchiveIndex()
  const teamId = args.account.team_id
  const startedAt = Date.now()

  const report: SyncReport = {
    team_id: teamId,
    channels_seen: 0,
    channels_synced: 0,
    messages_added: 0,
    replies_added: 0,
    threads_fetched: 0,
    deletions_marked: 0,
    errors: [],
    truncated: false,
    duration_ms: 0,
  }

  const state = await store.loadState()
  const teamState = (state.teams[teamId] ??= { channels: {} })
  const lock = new SyncLock(store.root)

  if (!(await lock.acquire())) {
    return { ...report, skipped: 'locked', duration_ms: Date.now() - startedAt }
  }

  try {
    const channels = await args.client.listMyChannels()
    const wanted = options.channels ? new Set(options.channels) : undefined
    const scoped = wanted ? channels.filter((channel) => wanted.has(channel.id)) : channels
    report.channels_seen = scoped.length
    const ordered = [...scoped].sort((left, right) => {
      const leftSync = teamState.channels[left.id]?.last_sync ?? ''
      const rightSync = teamState.channels[right.id]?.last_sync ?? ''
      return leftSync.localeCompare(rightSync)
    })

    for (const channel of ordered) {
      if (Date.now() > deadline) {
        report.truncated = true
        break
      }

      try {
        const outcome = await syncChannel({
          client: args.client,
          store,
          index,
          teamId,
          channel,
          entry: teamState.channels[channel.id] ?? {},
          options: resolved,
          deadline,
        })
        teamState.channels[channel.id] = outcome.entry
        report.channels_synced += 1
        report.messages_added += outcome.added
        report.replies_added += outcome.replies
        report.threads_fetched += outcome.threads
        report.deletions_marked += outcome.deletions
        if (!outcome.complete) report.truncated = true
        if (outcome.added + outcome.replies > 0) {
          progress(
            `${channelName(channel) ?? channel.id}: +${outcome.added} messages, +${outcome.replies} replies`,
          )
        }
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error)
        teamState.channels[channel.id] = {
          ...(teamState.channels[channel.id] ?? {}),
          last_error: message,
        }
        report.errors.push({ channel: channelName(channel) ?? channel.id, error: message })
      }

      await store.saveState(state)
    }

    teamState.last_run = resolved.now.toISOString()
    await store.saveState(state)
  } finally {
    await lock.release()
    index.close()
    report.duration_ms = Date.now() - startedAt
  }

  return report
}

/**
 * The bit of a message an envelope lookup needs, whichever side it came from: a
 * fresh one from Slack or a record the archive already holds.
 */
interface EnvelopeCarrier {
  ts: string
  files?: readonly SlackFile[] | undefined
}

/**
 * The envelope a message carries travels as a file, so the channel file alone is not
 * enough to read the thread — and in a thread is where every state change lives. The
 * bytes land next to the archive (the same store `read_file` uses) and the envelope is
 * copied into the record, which is what keeps `inbox --offline`, the ledger rebuild
 * and the inbox cursor honest without a download on every read.
 */
async function collectEnvelopes(args: {
  client: SyncClient
  store: ArchiveStore
  teamId: string
  carriers: readonly EnvelopeCarrier[]
  deadline: number
}): Promise<Map<string, string>> {
  const found = new Map<string, string>()
  const files = new ArchiveFileStore(args.store.root)
  for (const message of args.carriers) {
    if (Date.now() > args.deadline) break
    const file = forumFileOf(message.files)
    if (!file?.id || !args.client.downloadFile) continue
    try {
      const cached = await files.find(args.teamId, file.id)
      if (cached) {
        found.set(message.ts, await readFile(cached.path, 'utf8'))
        continue
      }
      const bytes = await args.client.downloadFile(file)
      await files.save(args.teamId, file, bytes)
      found.set(message.ts, new TextDecoder().decode(bytes))
    } catch {
      // An envelope that will not download is a record the next run can still
      // complete; failing the channel over one attachment is worse than retrying.
    }
  }
  return found
}

async function syncChannel(args: {
  client: SyncClient
  store: ArchiveStore
  index: ArchiveIndex
  teamId: string
  channel: SlackChannel
  entry: ChannelState
  options: ResolvedOptions
  deadline: number
}): Promise<{
  entry: ChannelState
  added: number
  replies: number
  threads: number
  deletions: number
  /** False when a read stopped before Slack ran out of pages. */
  complete: boolean
}> {
  const { channel, client, store, index, teamId, options } = args
  const name = channelName(channel)
  const cached = await store.read(teamId, channel.id)
  const cachedByTs = new Map(cached.map((record) => [record.ts, record]))
  const pending = new Set<string>(args.entry.pending_threads ?? [])
  const records: ArchivedMessage[] = []
  const fetched = new Map<string, SlackMessage>()

  /** True only when the whole window was read, so nothing downstream guesses from a partial one. */
  const collect = async (params: { oldest?: string }): Promise<boolean> => {
    let cursor: string | undefined
    for (let page = 0; page < MAX_PAGES; page += 1) {
      if (Date.now() > args.deadline) return false
      const result = await client.history({
        channel: channel.id,
        limit: PAGE_SIZE,
        ...(params.oldest ? { oldest: params.oldest } : {}),
        ...(cursor ? { cursor } : {}),
      })
      for (const message of result.messages) fetched.set(message.ts, message)
      if (!result.has_more) return true
      // Nothing to continue from, or the same page twice: stopping here is the only
      // honest answer, and it must not pass for the end of the channel.
      if (!result.next_cursor || result.next_cursor === cursor) return false
      cursor = result.next_cursor
    }
    return false
  }

  const incrementalFrom = options.full ? undefined : args.entry.newest_ts
  const refreshFrom =
    !options.full && options.refreshDays !== undefined && options.refreshDays > 0
      ? daysAgo(options.now, options.refreshDays)
      : undefined
  // One walk covers both questions: the window that catches edits and new replies
  // is the older of the cursor and the refresh start, so a poll stays one call. A
  // channel with no cursor still gets the full backfill, refresh window or not.
  const cursor = incrementalFrom ?? daysAgo(options.now, options.backfillDays)
  const incrementalComplete = await collect({ oldest: olderTs(cursor, refreshFrom) })

  const reconcileDue =
    options.reconcileDays > 0 &&
    (!args.entry.last_reconcile ||
      options.now.getTime() - Date.parse(args.entry.last_reconcile) > RECONCILE_INTERVAL_MS)

  // The deletion pass calls whatever the reconcile window did not show gone. A
  // window that was cut short proves nothing, so it is skipped instead of trusted.
  const reconcileComplete = reconcileDue
    ? await collect({ oldest: daysAgo(options.now, options.reconcileDays) })
    : true

  for (const message of fetched.values()) {
    if ((message.reply_count ?? 0) <= 0) continue
    const storedCount = cachedByTs.get(message.ts)?.reply_count ?? -1
    if (storedCount !== message.reply_count) pending.add(message.ts)
  }

  /**
   * Every reply of one thread. `conversations.replies` serves 200 at most, so a busy
   * thread is walked page by page; a walk that stopped early says so.
   */
  const collectThread = async (
    parentTs: string,
  ): Promise<{ replies: SlackMessage[]; complete: boolean }> => {
    const collected = new Map<string, SlackMessage>()
    let threadCursor: string | undefined
    for (let page = 0; page < MAX_PAGES; page += 1) {
      if (Date.now() > args.deadline) {
        return { replies: [...collected.values()], complete: false }
      }
      const result = await client.repliesPage({
        channel: channel.id,
        thread_ts: parentTs,
        limit: PAGE_SIZE,
        ...(threadCursor ? { cursor: threadCursor } : {}),
      })
      for (const message of result.messages) collected.set(message.ts, message)
      if (!result.has_more) return { replies: [...collected.values()], complete: true }
      if (!result.next_cursor || result.next_cursor === threadCursor) {
        return { replies: [...collected.values()], complete: false }
      }
      threadCursor = result.next_cursor
    }
    return { replies: [...collected.values()], complete: false }
  }

  // The threads come first: `accept`, `progress`, `result` and `ack` are replies, so
  // a reply carries an envelope exactly like a channel message does, and its file has
  // to be fetched before the record is built. Reading only the history left every
  // state change of a v0.3.0 task invisible while the task sat in `open`.
  const threadParents = [...pending].slice(0, options.maxThreadsPerChannel)
  const replyMessages: SlackMessage[] = []
  for (const parentTs of threadParents) {
    if (Date.now() > args.deadline) break
    const thread = await collectThread(parentTs)
    const replies = thread.replies.filter((message) => message.ts !== parentTs)
    replyMessages.push(...replies)
    // Slack can serve a thread a moment before its newest reply is visible. Keeping
    // the parent queued retries it on the next run instead of trusting a half
    // thread forever, which would leave a task stuck one state behind.
    const expected = fetched.get(parentTs)?.reply_count ?? cachedByTs.get(parentTs)?.reply_count ?? 0
    pending.delete(parentTs)
    if (!thread.complete || replies.length < expected) pending.add(parentTs)
  }

  // A record written before this build read envelopes from files still names the file
  // it carried, and that is enough to go back for it: without this pass an upgrade
  // leaves a channel half readable, with records that will never be looked at again.
  const replyTs = new Set(replyMessages.map((message) => message.ts))
  const stale = cached.filter(
    (record) =>
      record.forum === undefined &&
      !record.deleted_at &&
      forumFileOf(record.files) !== undefined &&
      !fetched.has(record.ts) &&
      !replyTs.has(record.ts),
  )

  const envelopes = await collectEnvelopes({
    client,
    store,
    teamId,
    carriers: [...fetched.values(), ...replyMessages, ...stale],
    deadline: args.deadline,
  })

  const withEnvelope = (record: ArchivedMessage, ts: string): ArchivedMessage => {
    const forum = envelopes.get(ts)
    if (forum !== undefined) record.forum = forum
    return record
  }

  for (const message of fetched.values()) {
    const record = withEnvelope(toRecord(channel, message, options.now), message.ts)
    if (changed(cachedByTs.get(message.ts), record)) records.push(record)
  }

  for (const record of stale) {
    const completed = withEnvelope(
      { ...record, synced_at: options.now.toISOString() },
      record.ts,
    )
    if (completed.forum !== undefined) records.push(completed)
  }

  let deletions = 0
  if (reconcileDue && reconcileComplete) {
    const windowStart = daysAgo(options.now, options.reconcileDays)
    for (const record of cached) {
      if (record.deleted_at) continue
      // Replies come from conversations.replies, so they are never in the history
      // window; their absence here says nothing about deletion.
      if (record.thread_ts) continue
      if (Number(record.ts) < Number(windowStart)) continue
      if (fetched.has(record.ts)) continue
      records.push({
        ts: record.ts,
        channel: channel.id,
        ...(name ? { channel_name: name } : {}),
        deleted_at: options.now.toISOString(),
        synced_at: options.now.toISOString(),
      })
      deletions += 1
    }
  }

  let replies = 0
  for (const message of replyMessages) {
    const record = withEnvelope(toRecord(channel, message, options.now), message.ts)
    if (!changed(cachedByTs.get(message.ts), record)) continue
    records.push(record)
    replies += 1
  }

  if (records.length > 0) {
    await store.append(teamId, channel.id, records)
  }
  // Everything the run saw goes to the index, not only what changed: a read tool
  // can have appended a record whose text already matched, and comparing against
  // the JSONL alone would leave that record unsearchable forever.
  const indexable = new Map(records.map((record) => [record.ts, record]))
  for (const message of fetched.values()) {
    if (!indexable.has(message.ts)) indexable.set(message.ts, toRecord(channel, message, options.now))
  }
  if (indexable.size > 0) index.upsert(teamId, [...indexable.values()])

  const timestamps = [
    ...cached.map((record) => record.ts),
    ...records.filter((record) => !record.deleted_at).map((record) => record.ts),
  ]
    .filter((ts) => Number(ts) > 0)
    .sort((left, right) => Number(left) - Number(right))

  const newest = timestamps.length > 0 ? timestamps[timestamps.length - 1] : undefined
  const entry: ChannelState = {
    // A read that stopped early leaves the cursor where it was. Stepping over the
    // part it never fetched is how a gap becomes permanent, so the run keeps the
    // previous numbers and the next one walks the same window again.
    ...(incrementalComplete
      ? {
          last_sync: options.now.toISOString(),
          ...(newest !== undefined ? { newest_ts: newest } : {}),
        }
      : {
          ...(args.entry.last_sync ? { last_sync: args.entry.last_sync } : {}),
          ...(args.entry.newest_ts ? { newest_ts: args.entry.newest_ts } : {}),
        }),
    ...(timestamps.length > 0 ? { oldest_ts: timestamps[0] } : {}),
    ...(reconcileDue && reconcileComplete
      ? { last_reconcile: options.now.toISOString() }
      : args.entry.last_reconcile
        ? { last_reconcile: args.entry.last_reconcile }
        : {}),
    ...(pending.size > 0 ? { pending_threads: [...pending].slice(0, PENDING_THREAD_CAP) } : {}),
  }

  return {
    entry,
    added: records.filter((record) => !record.deleted_at && !record.thread_ts).length,
    replies,
    threads: threadParents.length,
    deletions,
    complete: incrementalComplete && reconcileComplete,
  }
}
