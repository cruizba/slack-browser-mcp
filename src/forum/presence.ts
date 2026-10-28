/**
 * The two local marks behind the two liveness signals.
 *
 * A beacon says the machine is up and belongs to the poller, so it is throttled by
 * a timestamp: one post per window, and the window is `presence_seconds`. A claim
 * says what the person in front of the session is doing, never comes from the
 * poller, and only has to survive the archive lag between posting it and reading it
 * back; `here_seconds` is how long the same claim may stand before it is repeated.
 *
 * They are separate files because they answer different questions. Reading the
 * archive instead is not an option: a message that was just posted is not in it
 * until the next sync, so an archive-only check would repost on every tool call.
 */

import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { join } from 'node:path'

import { forumDir } from '../config/paths.ts'

/** What a session can claim about the person in it. `gone` is posted by unregister. */
export type PresenceClaim = 'here' | 'away'

/** How often a peer re-announces that its machine is up: one beacon per window. */
export function beaconThrottleMs(presenceSeconds: number): number {
  return presenceSeconds * 1000
}

export function beaconMarkPath(root: string = forumDir()): string {
  return join(root, '.beacon')
}

export function claimMarkPath(root: string = forumDir()): string {
  return join(root, '.claim')
}

export interface ClaimMark {
  state: PresenceClaim
  at: number
}

async function writeMark(file: string, contents: string): Promise<void> {
  await mkdir(join(file, '..'), { recursive: true, mode: 0o700 })
  await writeFile(file, contents, { encoding: 'utf8', mode: 0o600 })
}

export async function markBeacon(when: Date): Promise<void> {
  await writeMark(beaconMarkPath(), when.toISOString())
}

export async function lastBeacon(): Promise<number | undefined> {
  try {
    const stamp = Date.parse((await readFile(beaconMarkPath(), 'utf8')).trim())
    return Number.isFinite(stamp) ? stamp : undefined
  } catch {
    return undefined
  }
}

export async function markClaim(state: PresenceClaim, when: Date): Promise<void> {
  await writeMark(claimMarkPath(), JSON.stringify({ state, at: when.getTime() }))
}

export async function lastClaim(): Promise<ClaimMark | undefined> {
  try {
    const parsed: unknown = JSON.parse(await readFile(claimMarkPath(), 'utf8'))
    if (typeof parsed !== 'object' || parsed === null) return undefined
    const { state, at } = parsed as { state?: unknown; at?: unknown }
    if (state !== 'here' && state !== 'away') return undefined
    if (typeof at !== 'number' || !Number.isFinite(at)) return undefined
    return { state, at }
  } catch {
    return undefined
  }
}

export function presenceDisabled(env: NodeJS.ProcessEnv = process.env): boolean {
  return env.SLACK_BROWSER_MCP_NO_PRESENCE === '1'
}
