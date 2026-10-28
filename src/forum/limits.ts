/**
 * The clocks that keep two agents from talking past each other: how long a task
 * lives, how long silence counts as stalled, how often a peer says it is there,
 * and how long a peer may be quiet before it reads as out.
 *
 * They live in `forum.json` so a user can tune them without a code change. There is
 * no post budget: the channel is the one the user bound for agents to hand work to
 * each other, and a counter that refuses a task mid-conversation costs more than
 * the noise it prevents.
 */

import { z } from 'zod'

export const LimitsSchema = z.object({
  ttl_hours: z.number().positive().max(24 * 30),
  ack_seconds: z.number().int().min(30).max(86_400),
  stall_seconds: z.number().int().min(60).max(7 * 86_400),
  here_seconds: z.number().int().min(60).max(86_400),
  presence_seconds: z.number().int().min(60).max(24 * 3600),
  peer_ttl_hours: z.number().int().min(1).max(24 * 30),
})

export type ForumLimits = z.infer<typeof LimitsSchema>

export const DEFAULT_LIMITS: ForumLimits = {
  ttl_hours: 24,
  ack_seconds: 900,
  stall_seconds: 1800,
  /** How long a `here` claim stands, and how often the same one is repeated. */
  here_seconds: 3600,
  /** One beacon per window: at most one a day keeps the roster from rotting. */
  presence_seconds: 86_400,
  /** Three daily heartbeats may be missed before a peer reads as out. */
  peer_ttl_hours: 72,
}

/** A file may carry a subset; anything absent keeps the default. */
export function limitsWith(partial: Partial<ForumLimits> | undefined): ForumLimits {
  return { ...DEFAULT_LIMITS, ...(partial ?? {}) }
}

export function forumTimings(limits: ForumLimits): { ackMs: number; stallMs: number; ttlMs: number } {
  return {
    ackMs: limits.ack_seconds * 1000,
    stallMs: limits.stall_seconds * 1000,
    ttlMs: limits.ttl_hours * 3_600_000,
  }
}

/**
 * How far back a forum refresh re-reads the channel. Replies never show up in
 * `conversations.history`, so a parent older than the cursor is only seen again if
 * the window reaches it; it has to cover a task's whole life, or a reply to a task
 * from this morning would wait for the daily reconcile.
 */
export function refreshWindowDays(limits: ForumLimits): number {
  return Math.max(1, Math.ceil(limits.ttl_hours / 24))
}

function humanDuration(seconds: number): string {
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`
  const hours = seconds / 3600
  return Number.isInteger(hours) ? `${hours}h` : `${hours.toFixed(1)}h`
}

/** One line both surfaces print, so the numbers cannot drift apart. */
export function describeLimits(limits: ForumLimits): string {
  return (
    `ttl ${limits.ttl_hours}h · ack ${humanDuration(limits.ack_seconds)} · ` +
    `stall ${humanDuration(limits.stall_seconds)} · here ${humanDuration(limits.here_seconds)} · ` +
    `beacon ${humanDuration(limits.presence_seconds)}`
  )
}
