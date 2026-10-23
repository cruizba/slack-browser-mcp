/**
 * The mark a message carries when an agent wrote it on the user's behalf: a rule
 * above and below, one bold line naming the bot, and the text in between, so the
 * block reads as one delivered message rather than something typed into the
 * channel.
 *
 * Slack renders the rules as `divider` blocks, not as characters: a plain text
 * line arrives glued to the author's name, which Slack prints immediately above
 * the message, and neither a leading newline nor a zero-width space survives that
 * trim. The same block carries a plain `text` fallback for notifications, search
 * and clients that do not render blocks, which is the only thing the dry run can
 * promise to be exact about.
 *
 * Slack truncates the `text` field at 4000 characters and a section block at
 * 3000 without saying so, so a message that does not fit fails with an error the
 * caller can act on instead of losing its tail.
 */

import { defaultNickname, type Signing } from '../config/assist.ts'
import { StoreError } from '../errors.ts'

export const ASSISTED_HEADER = 'Message Sent by AI'
export const ASSISTED_RULE = '----------------------------------------'
export const MAX_TEXT_CHARS = 4000
const MAX_SECTION_CHARS = 2900

export type SlackBlock =
  | { readonly type: 'divider' }
  | { readonly type: 'section'; readonly text: { readonly type: 'mrkdwn'; readonly text: string } }

/** What a send carries: the fallback text, plus the blocks that render it. */
export interface SignedMessage {
  readonly text: string
  readonly blocks?: readonly SlackBlock[] | undefined
}

export function describeSigning(signing: Signing, agent?: string | undefined): string {
  if (signing.kind === 'nickname') return `assisted nickname: ${signing.nickname}`
  if (signing.kind === 'off') return 'assisted nickname off: posted messages carry no header'
  return `no nickname configured: each send is signed as *${ASSISTED_HEADER} ${defaultNickname(agent)} of <the sending account>*`
}

export function signedMessage(text: string, nickname: string): SignedMessage {
  const header = `*${ASSISTED_HEADER} ${nickname}*`
  const fallback = `${ASSISTED_RULE}\n\n${header}\n\n${text}\n\n${ASSISTED_RULE}`
  if (fallback.length > MAX_TEXT_CHARS) {
    throw new StoreError(
      `The message plus its AI-assisted header is ${fallback.length} characters; Slack accepts ${MAX_TEXT_CHARS} and would cut the end off, so shorten the text.`,
    )
  }
  if (text.length > MAX_SECTION_CHARS) {
    return { text: fallback }
  }
  return {
    text: fallback,
    blocks: [
      { type: 'divider' },
      { type: 'section', text: { type: 'mrkdwn', text: header } },
      { type: 'section', text: { type: 'mrkdwn', text } },
      { type: 'divider' },
    ],
  }
}
