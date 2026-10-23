/**
 * What signs a posted message: the nickname that follows "Message Sent by AI".
 *
 * Three states, not two. A configured nickname, the generated default, and off
 * are different answers, and the last two cannot be collapsed: an empty
 * `SLACK_BROWSER_MCP_ASSISTED_NICKNAME` means "post it bare" and must not fall
 * through to the default, or the only way to opt out would be editing the send.
 *
 * It is a preference, not a credential. The header is one bold line of Slack
 * mrkdwn, and mrkdwn has no escape for `*`, so an asterisk or a line break inside
 * a nickname would end the bold or split the block. Both are refused instead of
 * silently mangled; the generated default is cleaned the same way.
 */

import { z } from 'zod'

import { StoreError } from '../errors.ts'
import type { Settings } from './settings.ts'

export const ASSISTED_NICKNAME_ENV = 'SLACK_BROWSER_MCP_ASSISTED_NICKNAME'
const MAX_NICKNAME_CHARS = 60
/** The default also holds the words around the two names, so each name is capped. */
const MAX_AGENT_CHARS = 30
const MAX_USER_CHARS = 24

export type Signing =
  | { readonly kind: 'nickname'; readonly nickname: string }
  | { readonly kind: 'default' }
  | { readonly kind: 'off' }

function nicknameProblem(value: string): string | null {
  if (value.length > MAX_NICKNAME_CHARS) {
    return `at most ${MAX_NICKNAME_CHARS} characters, got ${value.length}`
  }
  if (/[*\r\n]/.test(value)) {
    return 'no * and no line break: the header is one bold line of Slack mrkdwn'
  }
  return null
}

export const assistedNicknameSchema = z.string().trim().superRefine((value, ctx) => {
  if (value === '') {
    ctx.addIssue({ code: 'custom', message: 'empty; remove the key to post without a header' })
    return
  }
  const problem = nicknameProblem(value)
  if (problem) ctx.addIssue({ code: 'custom', message: problem })
})

export function checkedNickname(raw: string): string {
  const value = raw.trim()
  if (value === '') throw new StoreError('The assisted nickname cannot be empty; clear it instead.')
  const problem = nicknameProblem(value)
  if (problem) throw new StoreError(`The assisted nickname cannot be used: ${problem}.`)
  return value
}

/**
 * Environment beats the settings file, so one shell can tag its messages
 * differently without editing shared state. An empty variable turns signing off
 * for that shell, which is easier than unsetting it in a wrapper script.
 */
export function signingFor(
  settings: Settings = {},
  env: NodeJS.ProcessEnv = process.env,
): Signing {
  const fromEnv = env[ASSISTED_NICKNAME_ENV]
  if (fromEnv !== undefined) {
    const value = fromEnv.trim()
    if (value === '') return { kind: 'off' }
    const problem = nicknameProblem(value)
    if (problem) {
      throw new StoreError(`"${ASSISTED_NICKNAME_ENV}" cannot be used: ${problem}.`)
    }
    return { kind: 'nickname', nickname: value }
  }

  const configured = settings.assisted_nickname
  if (configured) return { kind: 'nickname', nickname: checkedNickname(configured) }
  return { kind: 'default' }
}

function oneLine(raw: string, max: number): string {
  const flat = raw
    .replace(/[*\r\n\t]+/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
  return flat.slice(0, max).trim()
}

/**
 * The default names both ends: the client that produced the text and the account
 * it was posted from (`Agent 'pi' of carlos.ruiz`), so a reader in a channel sees
 * who is behind the message. An unidentified client says `unknown` instead of
 * leaving the sentence half-built; an account with no stored handle drops the
 * "of ..." part rather than naming nobody.
 */
export function defaultNickname(agent?: string, user?: string): string {
  const named = oneLine(agent ?? '', MAX_AGENT_CHARS) || 'unknown'
  const account = oneLine(user ?? '', MAX_USER_CHARS)
  const signature = account ? `Agent '${named}' of ${account}` : `Agent '${named}'`
  return signature.slice(0, MAX_NICKNAME_CHARS)
}

export function resolveNickname(
  signing: Signing,
  who: { agent?: string | undefined; user?: string | undefined } = {},
): string | null {
  if (signing.kind === 'off') return null
  if (signing.kind === 'nickname') return signing.nickname
  return defaultNickname(who.agent, who.user)
}
