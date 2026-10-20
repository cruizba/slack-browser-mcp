/**
 * Freshness of the archive, so a read tool can say how old what it returns is
 * instead of implying it is live.
 */

import type { SyncState } from './store.ts'

export interface TeamArchiveStatus {
  team_id: string
  last_run: string | null
  age_ms: number | null
  channels: number
  pending_threads: number
  errors: Array<{ channel: string; error: string }>
}

function latestRun(state: SyncState, teamId: string): string | null {
  const team = state.teams[teamId]
  if (!team) return null
  if (team.last_run) return team.last_run
  const syncs = Object.values(team.channels)
    .map((channel) => channel.last_sync)
    .filter((value): value is string => Boolean(value))
    .sort()
  return syncs[syncs.length - 1] ?? null
}

export function teamStatus(state: SyncState, teamId: string, now: Date): TeamArchiveStatus {
  const team = state.teams[teamId]
  const lastRun = latestRun(state, teamId)
  const entries = team ? Object.entries(team.channels) : []
  return {
    team_id: teamId,
    last_run: lastRun,
    age_ms: lastRun ? Math.max(0, now.getTime() - Date.parse(lastRun)) : null,
    channels: entries.length,
    pending_threads: entries.reduce(
      (total, [, channel]) => total + (channel.pending_threads?.length ?? 0),
      0,
    ),
    errors: entries
      .filter(([, channel]) => channel.last_error)
      .slice(0, 10)
      .map(([id, channel]) => ({ channel: id, error: channel.last_error! })),
  }
}

export function oldestAge(state: SyncState, now: Date): number | null {
  const ages = Object.keys(state.teams)
    .map((teamId) => teamStatus(state, teamId, now).age_ms)
    .filter((age): age is number => age !== null)
  if (ages.length === 0) return null
  return Math.max(...ages)
}

export function formatAge(ageMs: number | null): string {
  if (ageMs === null) return 'never synced'
  const minutes = Math.floor(ageMs / 60_000)
  if (minutes < 1) return 'just now'
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return `${hours} h ago`
  return `${Math.floor(hours / 24)} d ago`
}
