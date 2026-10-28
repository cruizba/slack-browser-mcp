/**
 * The limits that keep two agents from turning a coordination channel into a
 * billing incident: posts per hour, open tasks, delegation depth, task lifetime,
 * how long silence counts as stalled, and how many times a result may be sent
 * back for more.
 *
 * They live in `forum.json` so a user can tighten them without a code change;
 * every one of them is a refusal at write time, never a silent drop.
 */

import { z } from 'zod'

export const LimitsSchema = z
  .object({
    posts_per_hour: z.number().int().min(1).max(600),
    open_tasks: z.number().int().min(1).max(200),
    depth: z.number().int().min(0).max(9),
    ttl_hours: z.number().positive().max(24 * 30),
    ack_seconds: z.number().int().min(30).max(86_400),
    stall_seconds: z.number().int().min(60).max(7 * 86_400),
    reopens: z.number().int().min(0).max(9),
    presence_seconds: z.number().int().min(60).max(24 * 3600),
    max_body_chars: z.number().int().min(100).max(2000),
  })
  .strict()

export type ForumLimits = z.infer<typeof LimitsSchema>

export const DEFAULT_LIMITS: ForumLimits = {
  posts_per_hour: 30,
  open_tasks: 10,
  depth: 3,
  ttl_hours: 24,
  ack_seconds: 900,
  stall_seconds: 1800,
  reopens: 2,
  presence_seconds: 900,
  max_body_chars: 2000,
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
