/**
 * Rendering for agent-facing output: compact, one line per message, with the
 * raw `ts` preserved so a follow-up call can address the same message.
 */

import type { SlackChannel, SlackMessage } from './client.ts'

const MAX_MESSAGE_CHARS = 2000

export function formatTs(ts: string): string {
  const seconds = Number(ts.split('.')[0])
  if (!Number.isFinite(seconds)) return ts
  return new Date(seconds * 1000).toISOString().replace('T', ' ').slice(0, 19)
}

export function channelLabel(channel: SlackChannel): string {
  if (channel.is_im) return `dm:${channel.user ?? channel.id}`
  if (channel.is_mpim) return `group:${channel.name ?? channel.id}`
  return `${channel.is_private ? 'private:' : '#'}${channel.name ?? channel.id}`
}

function messageBody(message: SlackMessage): string {
  let text = message.text?.trim() ?? ''
  if (!text && message.subtype) text = `(${message.subtype})`
  for (const file of message.files ?? []) {
    const label = file.name ?? file.title ?? file.mimetype ?? 'unnamed'
    text = `${text}${text ? ' ' : ''}[file: ${label}]`
  }
  if (text.length > MAX_MESSAGE_CHARS) {
    text = `${text.slice(0, MAX_MESSAGE_CHARS)}… [truncated]`
  }
  return text
}

function indentContinuation(text: string, prefix: string): string {
  return text.split('\n').join(`\n${prefix}`)
}

export interface RenderOptions {
  names: Map<string, string>
}

export function renderMessages(messages: SlackMessage[], options: RenderOptions): string {
  return messages
    .map((message) => {
      const name = message.user
        ? (options.names.get(message.user) ?? message.user)
        : (message.username ?? message.bot_id ?? 'unknown')
      const body = indentContinuation(messageBody(message), '    ')
      const thread =
        message.reply_count && message.thread_ts === undefined
          ? `  (${message.reply_count} replies, thread_ts ${message.ts})`
          : ''
      return `[${message.ts}] ${formatTs(message.ts)} ${name}: ${body}${thread}`
    })
    .join('\n')
}
