/**
 * The nickname that follows "AI Bot" in the header a posted message carries.
 *
 * It is a preference, not a credential: unset means the message goes out bare.
 * The header is a single bold line of Slack mrkdwn, and mrkdwn has no escape for
 * `*`, so an asterisk or a line break inside the nickname would end the bold or
 * split the block. Both are refused instead of silently mangled.
 */

import { z } from 'zod'

import { StoreError } from '../errors.ts'
import type { Settings } from './settings.ts'

export const ASSISTED_NICKNAME_ENV = 'SLACK_BROWSER_MCP_ASSISTED_NICKNAME'
const MAX_NICKNAME_CHARS = 60

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
 * differently without editing shared state. An empty variable disables the
 * header, which is easier than unsetting it in a wrapper script.
 */
export function assistedNickname(
  settings: Settings = {},
  env: NodeJS.ProcessEnv = process.env,
): string | null {
  const fromEnv = env[ASSISTED_NICKNAME_ENV]
  if (fromEnv !== undefined) {
    const value = fromEnv.trim()
    if (value === '') return null
    const problem = nicknameProblem(value)
    if (problem) {
      throw new StoreError(`"${ASSISTED_NICKNAME_ENV}" cannot be used: ${problem}.`)
    }
    return value
  }

  const configured = settings.assisted_nickname
  return configured ? checkedNickname(configured) : null
}
