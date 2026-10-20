/**
 * Cache-aside for read tools: whatever a tool had to ask Slack for goes into the
 * archive, so the next call is answered from disk.
 *
 * Cursors stay sync's business. A read that moved `newest_ts` would make a
 * running backfill skip everything between its cursor and the messages just
 * fetched, and `changed()` would never look at that gap again.
 */

import type { SlackChannel, SlackMessage } from '../slack/client.ts'
import type { ArchiveIndex } from './index.ts'
import { changed, toRecord } from './record.ts'
import type { ArchiveStore } from './store.ts'

export interface RememberResult {
  /** Records appended; zero means Slack had nothing we did not already hold. */
  written: number
  /** False when the index refused the write, which leaves search blind until the next sync. */
  indexed: boolean
}

export async function rememberMessages(args: {
  store: ArchiveStore
  index: ArchiveIndex
  teamId: string
  channel: SlackChannel
  messages: SlackMessage[]
  now?: Date
  /** `search` for results filed from Slack search, which are not channel history. */
  source?: 'search'
}): Promise<RememberResult> {
  if (args.messages.length === 0) return { written: 0, indexed: true }
  const now = args.now ?? new Date()
  const cached = await args.store.read(args.teamId, args.channel.id)
  const cachedByTs = new Map(cached.map((record) => [record.ts, record]))
  const records = args.messages
    .map((message) => toRecord(args.channel, message, now, args.source))
    .filter((record) => changed(cachedByTs.get(record.ts), record))
  if (records.length === 0) return { written: 0, indexed: true }

  await args.store.append(args.teamId, args.channel.id, records)
  try {
    args.index.upsert(args.teamId, records)
    return { written: records.length, indexed: true }
  } catch {
    return { written: records.length, indexed: false }
  }
}
