/**
 * A Slack message as the archive stores it, plus the comparison that decides
 * whether Slack's copy differs from ours. Shared by the bulk sync and by the
 * cache-aside writer the read tools use.
 */

import type { SlackChannel, SlackMessage } from '../slack/client.ts'
import type { ArchivedMessage } from './store.ts'

export function channelName(channel: SlackChannel): string | undefined {
  return channel.name ?? channel.user ?? undefined
}

export function toRecord(
  channel: SlackChannel,
  message: SlackMessage,
  now: Date,
  source?: 'search',
): ArchivedMessage {
  const name = channelName(channel)
  const files = (message.files ?? [])
    .map((file) => ({
      ...(file.id ? { id: file.id } : {}),
      ...(file.name ? { name: file.name } : {}),
      ...(file.title ? { title: file.title } : {}),
      ...(file.mimetype ? { mimetype: file.mimetype } : {}),
      ...(file.size !== undefined ? { size: file.size } : {}),
    }))
    .filter((file) => Object.keys(file).length > 0)
  return {
    ts: message.ts,
    channel: channel.id,
    ...(name ? { channel_name: name } : {}),
    ...(message.user ? { user: message.user } : {}),
    ...(message.username ? { username: message.username } : {}),
    ...(message.bot_id ? { bot_id: message.bot_id } : {}),
    ...(message.text !== undefined ? { text: message.text } : {}),
    ...(message.thread_ts ? { thread_ts: message.thread_ts } : {}),
    ...(message.subtype ? { subtype: message.subtype } : {}),
    ...(message.edited?.ts ? { edited_ts: message.edited.ts } : {}),
    ...(message.reply_count !== undefined ? { reply_count: message.reply_count } : {}),
    ...(files.length > 0 ? { files } : {}),
    ...(source ? { source } : {}),
    synced_at: now.toISOString(),
  }
}

export function changed(cached: ArchivedMessage | undefined, record: ArchivedMessage): boolean {
  if (!cached) return true
  if (cached.deleted_at) return true
  return (
    // A message filed from search becomes real history once a channel fetch sees
    // it, so the origin is part of what makes a record stale.
    cached.source !== record.source ||
    cached.text !== record.text ||
    cached.edited_ts !== record.edited_ts ||
    cached.reply_count !== record.reply_count ||
    JSON.stringify(cached.files ?? []) !== JSON.stringify(record.files ?? [])
  )
}

/** The inverse: what the archive holds, shaped the way the API would have said it. */
export function fromRecord(record: ArchivedMessage): SlackMessage {
  return {
    ts: record.ts,
    ...(record.user ? { user: record.user } : {}),
    ...(record.username ? { username: record.username } : {}),
    ...(record.bot_id ? { bot_id: record.bot_id } : {}),
    ...(record.text !== undefined ? { text: record.text } : {}),
    ...(record.thread_ts ? { thread_ts: record.thread_ts } : {}),
    ...(record.subtype ? { subtype: record.subtype } : {}),
    ...(record.edited_ts ? { edited: { ts: record.edited_ts } } : {}),
    ...(record.reply_count !== undefined ? { reply_count: record.reply_count } : {}),
    ...(record.files && record.files.length > 0 ? { files: record.files } : {}),
  }
}
