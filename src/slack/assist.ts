/**
 * The mark a message carries when an agent wrote it on the user's behalf: a rule
 * line, one bold line naming the bot, the text, and a closing rule, so the block
 * reads as one delivered message rather than something typed into the channel.
 * Composed where the send is decided, never inside the transport, so a dry run
 * prints exactly what Slack receives.
 *
 * Slack truncates the `text` field at 4000 characters without saying so, so the
 * decoration cannot simply be wrapped around a message that already fills the
 * field: the caller gets an error it can act on instead of a message missing its
 * end.
 */

import { defaultNickname, type Signing } from '../config/assist.ts'
import { StoreError } from '../errors.ts'

export const ASSISTED_HEADER = 'Message Sent by AI'
export const ASSISTED_RULE = '----------------------------------------'
export const MAX_TEXT_CHARS = 4000

export function describeSigning(signing: Signing, agent?: string | undefined): string {
  if (signing.kind === 'nickname') return `assisted nickname: ${signing.nickname}`
  if (signing.kind === 'off') return 'assisted nickname off: posted messages carry no header'
  return `no nickname configured: each send is signed as *${ASSISTED_HEADER} ${defaultNickname(agent)} of <the sending account>*`
}

export function withAssistHeader(text: string, nickname: string): string {
  // Slack prints the author's name right above the text, so the opening rule
  // needs a line of its own or it reads as part of that name.
  const opening = `\n${ASSISTED_RULE}\n\n*${ASSISTED_HEADER} ${nickname}*\n\n`
  const closing = `\n\n${ASSISTED_RULE}`
  const total = opening.length + text.length + closing.length
  if (total > MAX_TEXT_CHARS) {
    throw new StoreError(
      `The message plus its AI-assisted header is ${total} characters; Slack accepts ${MAX_TEXT_CHARS} and would cut the end off, so shorten the text.`,
    )
  }
  return opening + text + closing
}
