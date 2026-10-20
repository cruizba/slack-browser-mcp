/**
 * When a read may refresh the archive in the background, and how old an answer
 * may be before it does.
 *
 * Two thresholds, because two reads can ask different questions of the same
 * archive. A read that ends at "now" (no `latest`) is about what just happened,
 * and a day-old tail would answer the wrong question, so it tolerates minutes. A
 * read that pages back through history only wants the archive to be from the
 * same day: an older tail cannot change what happened last week.
 *
 * Environment beats the settings file, which beats the defaults, so a single
 * shell can be tuned without editing anything shared.
 */

import { StoreError } from '../errors.ts'
import type { Settings } from './settings.ts'

export const DEFAULT_STALE_AFTER_MINUTES = 24 * 60
export const DEFAULT_RECENT_AFTER_MINUTES = 15

export const STALE_AFTER_ENV = 'SLACK_BROWSER_MCP_STALE_AFTER_MINUTES'
export const RECENT_AFTER_ENV = 'SLACK_BROWSER_MCP_RECENT_AFTER_MINUTES'

export interface FreshnessPolicy {
  staleAfterMs: number
  recentAfterMs: number
}

function minutes(
  name: string,
  fromEnv: string | undefined,
  configured: number | undefined,
  fallback: number,
): number {
  const raw = fromEnv?.trim()
  const value = raw ? Number(raw) : (configured ?? fallback)
  if (!Number.isFinite(value) || value <= 0) {
    throw new StoreError(
      `${raw ? name : `"${name} in config.json"`} must be a positive number of minutes, got ${JSON.stringify(raw || configured)}.`,
    )
  }
  return value * 60_000
}

export function freshnessPolicy(
  settings: Settings = {},
  env: NodeJS.ProcessEnv = process.env,
): FreshnessPolicy {
  return {
    staleAfterMs: minutes(
      STALE_AFTER_ENV,
      env[STALE_AFTER_ENV],
      settings.stale_after_minutes,
      DEFAULT_STALE_AFTER_MINUTES,
    ),
    recentAfterMs: minutes(
      RECENT_AFTER_ENV,
      env[RECENT_AFTER_ENV],
      settings.recent_after_minutes,
      DEFAULT_RECENT_AFTER_MINUTES,
    ),
  }
}

export function wantsRefresh(
  ageMs: number | null,
  policy: FreshnessPolicy,
  /** True for a read whose window is open at the top: one that expects the newest messages. */
  atNow: boolean,
): boolean {
  // Nothing stored for this workspace yet: whatever the read finds is a miss, and
  // fetching it in the background cannot make the answer worse.
  if (ageMs === null) return true
  return ageMs >= (atNow ? policy.recentAfterMs : policy.staleAfterMs)
}

function humanize(ms: number): string {
  const minutes = ms / 60_000
  if (minutes < 60) return `${minutes} min`
  const hours = minutes / 60
  if (hours < 48) return `${hours} h`
  return `${hours / 24} d`
}

export function describePolicy(policy: FreshnessPolicy): string {
  return (
    `refresh: background sync when the archive is older than ${humanize(policy.staleAfterMs)}, ` +
    `or ${humanize(policy.recentAfterMs)} for a read that reaches the newest messages`
  )
}
