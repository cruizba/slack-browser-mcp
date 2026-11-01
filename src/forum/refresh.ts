/**
 * Refreshing one channel because somebody asked for it.
 *
 * The archive is written by the daemon, so a read that finds a stale channel asks
 * the daemon instead of starting its own sync. Coalescing happens here because the
 * daemon is one process: ten agents reading the same stale channel cost one
 * `conversations.history` call, which is the whole reason the daemon exists.
 *
 * Building the client per call is deliberate. A stored session can be replaced
 * while the daemon runs (`login_workspace` twice), and a cached client would keep
 * posting with the old credentials until the process restarted.
 */

import { syncAccount } from '../archive/sync.ts'
import { FileCredentialStore } from '../auth/store.ts'
import { SlackClient } from '../slack/client.ts'

const store = new FileCredentialStore()
const inFlight = new Map<string, Promise<RefreshOutcome>>()

export interface RefreshOutcome {
  team: string
  channel: string
  messages: number
  replies: number
  locked: boolean
  errors: string[]
}

/**
 * Hands back the run already under way for `key`, or starts one. Concurrent
 * readers of the same stale channel must share a single Slack call, and the entry
 * has to be gone by the time the run settles or the next reader would join a
 * finished promise and never refresh.
 */
export function singleFlight<T>(
  inFlight: Map<string, Promise<T>>,
  key: string,
  run: () => Promise<T>,
): Promise<T> {
  const running = inFlight.get(key)
  if (running) return running
  const started = run()
  inFlight.set(key, started)
  void started
    .finally(() => {
      if (inFlight.get(key) === started) inFlight.delete(key)
    })
    .catch(() => undefined)
  return started
}

export function refreshChannel(teamId: string, channelId: string): Promise<RefreshOutcome> {
  return singleFlight(inFlight, `${teamId}:${channelId}`, () => refresh(teamId, channelId))
}

async function refresh(teamId: string, channelId: string): Promise<RefreshOutcome> {
  const report: RefreshOutcome = {
    team: teamId,
    channel: channelId,
    messages: 0,
    replies: 0,
    locked: false,
    errors: [],
  }
  const account = await store.get(teamId).catch(() => undefined)
  if (!account) {
    return { ...report, errors: [`no stored Slack session for team ${teamId}`] }
  }
  const result = await syncAccount({
    account,
    client: new SlackClient(account),
    options: { channels: [channelId], deadlineMs: 20_000 },
  })
  return {
    ...report,
    messages: result.messages_added,
    replies: result.replies_added,
    locked: result.skipped === 'locked',
    errors: result.errors.map((entry) => `${entry.channel}: ${entry.error}`),
  }
}
