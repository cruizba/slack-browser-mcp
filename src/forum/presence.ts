/**
 * Whether this machine is actually going to pick up a task.
 *
 * A register says "I exist"; it says nothing about whether anyone is looking. Slack
 * retention makes that worse: an old register can simply disappear from the channel,
 * so the roster has to be refreshed by living messages rather than rebuilt from
 * history. Presence is that living message, and the marker below is what turns
 * "the timer is enabled" and "a wait is running" into one fact cheaply, without
 * spawning `systemctl` on every tool call.
 */

import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { join } from 'node:path'

import { forumDir } from '../config/paths.ts'

export type PresenceState = 'listening' | 'idle'

/**
 * How often a peer re-announces itself. One post per window: presence is a
 * heartbeat that keeps a roster entry from rotting away with Slack retention, not
 * a live status feed, so a day between messages is enough.
 */
export function presenceThrottleMs(presenceSeconds: number): number {
  return presenceSeconds * 1000
}

export function listeningMarkPath(root: string = forumDir()): string {
  return join(root, '.listening')
}

/**
 * When this machine last announced itself. The archive cannot answer that: a
 * message that was just posted is not in it until the next sync, so without a local
 * mark every tool call would look "not announced yet" and the channel would fill up
 * with presence.
 */
export function presenceMarkPath(root: string = forumDir()): string {
  return join(root, '.presence')
}

export async function markPresence(when: Date): Promise<void> {
  const file = presenceMarkPath()
  await mkdir(join(file, '..'), { recursive: true, mode: 0o700 })
  await writeFile(file, when.toISOString(), { encoding: 'utf8', mode: 0o600 })
}

export async function lastPresence(): Promise<number | undefined> {
  try {
    const stamp = Date.parse((await readFile(presenceMarkPath(), 'utf8')).trim())
    return Number.isFinite(stamp) ? stamp : undefined
  } catch {
    return undefined
  }
}

export function presenceDisabled(env: NodeJS.ProcessEnv = process.env): boolean {
  return env.SLACK_BROWSER_MCP_NO_PRESENCE === '1'
}

/**
 * Called by the poller and by `wait`: while the mark is fresh, a presence message
 * may claim `listening`.
 */
export async function markListening(now: Date = new Date()): Promise<void> {
  const file = listeningMarkPath()
  await mkdir(join(file, '..'), { recursive: true, mode: 0o700 })
  await writeFile(file, now.toISOString(), { encoding: 'utf8', mode: 0o600 })
}

export async function listeningSince(now: Date = new Date()): Promise<number | undefined> {
  try {
    const stamp = Date.parse((await readFile(listeningMarkPath(), 'utf8')).trim())
    if (!Number.isFinite(stamp)) return undefined
    return Math.max(0, now.getTime() - stamp)
  } catch {
    return undefined
  }
}

/**
 * Listening is a claim with an expiry, not a mode: a crashed poller stops looking
 * alive after the window instead of claiming attention forever.
 */
export async function presenceState(
  presenceSeconds: number,
  now: Date = new Date(),
): Promise<PresenceState> {
  const since = await listeningSince(now)
  return since !== undefined && since < presenceSeconds * 1000 ? 'listening' : 'idle'
}
