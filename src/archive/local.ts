/**
 * What a read tool can answer from disk, and the point where it has to admit the
 * archive cannot help. Keeping both here means the tools decide about caching,
 * not about parsing the archive.
 */

import type { SlackMessage } from '../slack/client.ts'
import { fromRecord } from './record.ts'
import type { ArchiveStore } from './store.ts'

/**
 * A channel's top-level history. `undefined` means Slack has to be asked: the
 * archive holds nothing for the channel and no sync has walked it either.
 */
export async function localHistory(args: {
  store: ArchiveStore
  teamId: string
  channelId: string
  limit: number
  oldest?: string
  latest?: string
  /** A sync has walked the channel, so finding nothing in it is an answer too. */
  synced?: boolean
}): Promise<SlackMessage[] | undefined> {
  const records = await args.store.read(args.teamId, args.channelId)
  // conversations.history never returns thread replies, and neither does the tool.
  // Search hits are individual messages, not a window: serving them as history
  // would show gaps as if the channel were quiet.
  const top = records.filter(
    (record) => !record.deleted_at && !record.thread_ts && record.source !== 'search',
  )

  const oldest = args.oldest ? Number(args.oldest) : undefined
  const latest = args.latest ? Number(args.latest) : undefined
  const inWindow = top
    .filter((record) => oldest === undefined || Number(record.ts) > oldest)
    .filter((record) => latest === undefined || Number(record.ts) < latest)
    .sort((left, right) => Number(left.ts) - Number(right.ts))

  if (inWindow.length > 0) return inWindow.slice(-args.limit).map(fromRecord)
  // A quiet channel leaves no records behind; without this every read of it
  // would go to Slack only to hear the same empty answer.
  if (top.length === 0 && !args.synced) return undefined
  // Archived but outside the window: an empty answer, not a reason to ask Slack.
  return []
}

/**
 * A whole thread, or `undefined` while a single reply is missing: serving half a
 * thread silently would look like the conversation ended there.
 */
export async function localThread(args: {
  store: ArchiveStore
  teamId: string
  channelId: string
  threadTs: string
  limit: number
}): Promise<SlackMessage[] | undefined> {
  const records = await args.store.read(args.teamId, args.channelId)
  const parent = records.find((record) => record.ts === args.threadTs && record.source !== 'search')
  if (!parent || parent.deleted_at || parent.reply_count === undefined) return undefined

  const replies = records
    .filter((record) => record.thread_ts === args.threadTs && record.ts !== args.threadTs)
    .filter((record) => !record.deleted_at)
    .sort((left, right) => Number(left.ts) - Number(right.ts))
  if (replies.length < parent.reply_count) return undefined

  return [parent, ...replies].slice(0, args.limit).map(fromRecord)
}
