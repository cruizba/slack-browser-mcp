/**
 * The mark a message carries when an agent wrote it on the user's behalf: one
 * bold line naming the bot, then the text. Composed where the send is decided,
 * never inside the transport, so a dry run prints exactly what Slack receives.
 *
 * Slack truncates the `text` field at 4000 characters without saying so, so the
 * header cannot simply be prepended to a message that already fills the field:
 * the caller gets an error it can act on instead of a message missing its end.
 */

import { StoreError } from '../errors.ts'

export const ASSISTED_HEADER = 'Message assisted by AI Bot'
export const MAX_TEXT_CHARS = 4000

export function withAssistHeader(text: string, nickname: string): string {
  const header = `*${ASSISTED_HEADER} ${nickname}*\n\n`
  const total = header.length + text.length
  if (total > MAX_TEXT_CHARS) {
    throw new StoreError(
      `The message plus its AI-assisted header is ${total} characters; Slack accepts ${MAX_TEXT_CHARS} and would cut the end off, so shorten the text.`,
    )
  }
  return header + text
}
