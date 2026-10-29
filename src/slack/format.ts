/**
 * Rendering for agent-facing output: compact, one line per message, with the
 * raw `ts` preserved so a follow-up call can address the same message.
 */

import type { SlackChannel, SlackFile, SlackMessage } from './client.ts'
import { slackChannelLabel } from './conversations.ts'
import type { Directory } from './directory.ts'
import { resolveReferences, type ReferenceScope } from './references.ts'

/**
 * Slack's own recommendation is 4000 characters per message, and the archive holds
 * longer ones, so a channel read cuts there and says so instead of quietly handing
 * back half a message. `search_messages` prints a hit whole and is the way to ask
 * for the rest.
 */
const MAX_MESSAGE_CHARS = 4000

export function formatTs(ts: string): string {
  const seconds = Number(ts.split('.')[0])
  if (!Number.isFinite(seconds)) return ts
  return new Date(seconds * 1000).toISOString().replace('T', ' ').slice(0, 19)
}

/**
 * `#random`, `private:leads`, `dm:Ana Ruiz`. A DM has no name of its own, so the
 * directory supplies the person on the other side; without one, its id is shown
 * rather than a name guessed from nothing.
 */
export function channelLabel(channel: SlackChannel, directory?: Directory): string {
  return slackChannelLabel(channel, directory)
}

/**
 * The id is the handle `read_file` needs, so it has to travel next to the name:
 * a re-uploaded or renamed file keeps its id, the label does not.
 */
function fileLabel(file: SlackFile): string {
  const label = file.name ?? file.title ?? file.mimetype ?? 'unnamed'
  return file.id ? `[file: ${label} id=${file.id}]` : `[file: ${label}]`
}

function messageBody(message: SlackMessage, scope?: ReferenceScope): string {
  let text = message.text?.trim() ?? ''
  if (!text && message.subtype) text = `(${message.subtype})`
  text = resolveReferences(text, scope)
  for (const file of message.files ?? []) {
    text = `${text}${text ? ' ' : ''}${fileLabel(file)}`
  }
  if (text.length > MAX_MESSAGE_CHARS) {
    text = `${text.slice(0, MAX_MESSAGE_CHARS)}… [truncated: ${text.length} characters, search_messages prints a message whole]`
  }
  return text
}

function indentContinuation(text: string, prefix: string): string {
  return text.split('\n').join(`\n${prefix}`)
}

export interface RenderOptions {
  names: Map<string, string>
  scope?: ReferenceScope | undefined
}

export function renderMessages(messages: SlackMessage[], options: RenderOptions): string {
  return messages
    .map((message) => {
      const name = message.user
        ? (options.names.get(message.user) ?? message.user)
        : (message.username ?? message.bot_id ?? 'unknown')
      const body = indentContinuation(messageBody(message, options.scope), '    ')
      const thread =
        message.reply_count && message.thread_ts === undefined
          ? `  (${message.reply_count} replies, thread_ts ${message.ts})`
          : ''
      return `[${message.ts}] ${formatTs(message.ts)} ${name}: ${body}${thread}`
    })
    .join('\n')
}
