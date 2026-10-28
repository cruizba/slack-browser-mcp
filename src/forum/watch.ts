/**
 * Keeping the bound channels fresh.
 *
 * The forum reuses the archive's sync instead of its own fetcher: one write path
 * means one lock, one set of tombstones, one place where a deletion is marked. The
 * only thing it adds is a channel filter, so a poll of one coordination channel
 * costs one `conversations.history` call instead of walking every channel the
 * account belongs to.
 */

import { syncAccount } from '../archive/sync.ts'
import type { SlackAccount } from '../auth/store.ts'
import type { SlackClient } from '../slack/client.ts'
import type { Binding } from './binding.ts'
import type { ForumContext } from './context.ts'
import { inboxEntries, loadViews, type InboxReport } from './inbox.ts'
import { markListening } from './presence.ts'

export interface CatchUpReport {
  synced: number
  locked: boolean
  errors: string[]
}

export interface CatchUpArgs {
  bindings: readonly Binding[]
  clientFor: (teamId: string) => SlackClient | undefined
  accountFor: (teamId: string) => SlackAccount | undefined
  deadlineMs?: number
  now?: Date
  onProgress?: (line: string) => void
}

export async function catchUp(args: CatchUpArgs): Promise<CatchUpReport> {
  const byTeam = new Map<string, string[]>()
  for (const binding of args.bindings) {
    const list = byTeam.get(binding.workspace) ?? []
    list.push(binding.channel)
    byTeam.set(binding.workspace, list)
  }

  const deadline = Date.now() + (args.deadlineMs ?? 25_000)
  const report: CatchUpReport = { synced: 0, locked: false, errors: [] }

  for (const [teamId, channels] of byTeam) {
    const account = args.accountFor(teamId)
    const client = args.clientFor(teamId)
    if (!account || !client) {
      report.errors.push(`${teamId}: no stored session`)
      continue
    }
    const remaining = deadline - Date.now()
    if (remaining <= 0) {
      report.errors.push('deadline reached before every channel was refreshed')
      break
    }
    // A poll that reaches Slack is the definition of "listening" for this machine.
    await markListening(args.now ?? new Date())
    try {
      const result = await syncAccount({
        account,
        client,
        options: {
          channels,
          deadlineMs: remaining,
          ...(args.now ? { now: args.now } : {}),
          ...(args.onProgress ? { onProgress: args.onProgress } : {}),
        },
      })
      if (result.skipped === 'locked') {
        report.locked = true
        continue
      }
      report.synced += result.channels_synced
      for (const failure of result.errors) report.errors.push(`${failure.channel}: ${failure.error}`)
    } catch (error) {
      report.errors.push(error instanceof Error ? error.message : String(error))
    }
  }

  return report
}

export interface WaitOptions {
  /** Seconds to keep looking; 0 checks once and returns. */
  waitSeconds: number
  intervalMs?: number
  /** Skip the network entirely and answer from the archive. */
  offline?: boolean
  refresh?: () => Promise<CatchUpReport>
}

export interface WaitResult {
  report: InboxReport
  cycles: number
  catchUp?: CatchUpReport
}

/**
 * The wait loop behind `forum_inbox { wait_seconds }` and `agent-forum wait`. It
 * stops as soon as something arrives, so a loop costs one turn rather than one
 * turn per poll.
 */
export async function waitForWork(
  context: ForumContext,
  options: WaitOptions,
): Promise<WaitResult> {
  const interval = options.intervalMs ?? 5000
  const until = Date.now() + Math.max(0, options.waitSeconds) * 1000
  let cycles = 0
  let last: CatchUpReport | undefined

  for (;;) {
    cycles += 1
    if (!options.offline && options.refresh) {
      last = await options.refresh()
      await markListening()
    }
    const views = await loadViews(context)
    const report = inboxEntries(context, views)
    if (report.entries.length > 0 || Date.now() >= until) {
      return { report, cycles, ...(last ? { catchUp: last } : {}) }
    }
    const pause = Math.min(interval, Math.max(0, until - Date.now()))
    if (pause <= 0) return { report, cycles, ...(last ? { catchUp: last } : {}) }
    await new Promise((done) => setTimeout(done, pause))
  }
}
