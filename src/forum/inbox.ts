/**
 * Views over the bound channels and the filter that decides what lands in an
 * inbox.
 *
 * The filter is state-based, not cursor-based: a task shows up while it is open
 * and addressed to me, and disappears when it closes, so calling the inbox twice
 * returns the same pending set instead of an empty second answer. The cursor only
 * bounds how far back a scan goes.
 */

import type { Decision } from './decisions.ts'
import type { Binding } from './binding.ts'
import { bindingKey } from './binding.ts'
import { meLabel, type ForumContext } from './context.ts'
import { DirectoryStore, directoryUser } from '../slack/directory.ts'
import { buildLedger, type ForumTask, type Ledger } from './ledger.ts'
import { rosterFrom, type Peer } from './roster.ts'

export interface BindingView {
  binding: Binding
  key: string
  roster: Peer[]
  ledger: Ledger
  decisions: Map<string, Decision>
  /** Slack user id to a readable name, from the cached directory when it exists. */
  people: Map<string, string>
}

export async function loadView(context: ForumContext, binding: Binding): Promise<BindingView> {
  const messages = await context.archive.read(binding.workspace, binding.channel)
  const roster = rosterFrom(messages, context.file.ignored)
  const me = meLabel(context)
  const ledger = buildLedger({
    messages,
    teamId: binding.workspace,
    channelId: binding.channel,
    roster,
    me,
    limits: context.limits,
    now: context.now,
  })
  const decisions = await context.decisions.read(binding.workspace, binding.channel)
  const directory = await new DirectoryStore(context.archive.root).read(binding.workspace)
  const people = new Map<string, string>()
  for (const peer of roster) {
    const label = peer.owner ? directoryUser(directory, peer.owner) : undefined
    if (peer.owner && label) people.set(peer.owner, label)
  }
  return { binding, key: bindingKey(binding), roster, ledger, decisions, people }
}

export async function loadViews(
  context: ForumContext,
  filter: { workspace?: string; only?: string } = {},
): Promise<BindingView[]> {
  const wanted = filter.only
  const bindings = context.file.channels.filter(
    (binding) =>
      (filter.workspace === undefined || binding.workspace === filter.workspace) &&
      (wanted === undefined || bindingKey(binding) === wanted),
  )
  return Promise.all(bindings.map((binding) => loadView(context, binding)))
}

export interface InboxEntry {
  view: BindingView
  task: ForumTask
  decision?: Decision
}

export interface InboxOptions {
  /** Everything in the bound channels, not only what is addressed to me. */
  all?: boolean
  /** Ignore the stored cursor and show the whole history the binding can see. */
  sinceStart?: boolean
}

export interface InboxReport {
  entries: InboxEntry[]
  views: BindingView[]
  unreadable: number
  orphans: number
  /** Tasks addressed to me whose author is not in the roster, or is retired. */
  hidden: number
  /** `*` tasks another agent already claimed. */
  taken: number
}

const MINE = (task: ForumTask, me: string, capabilities: readonly string[]): boolean => {
  if (task.to === me) return true
  if (task.to !== '*') return false
  return task.tag === undefined || capabilities.includes(task.tag)
}

export function inboxEntries(
  context: ForumContext,
  views: readonly BindingView[],
  options: InboxOptions = {},
): InboxReport {
  const me = meLabel(context)
  const capabilities = context.identity?.capabilities ?? []
  const entries: InboxEntry[] = []
  let hidden = 0
  let taken = 0
  let unreadable = 0
  let orphans = 0

  for (const view of views) {
    unreadable += view.ledger.unreadable
    orphans += view.ledger.orphans
    hidden += view.ledger.unvouched
    // The boundary is when the channel was bound. Reading must not move it: the
    // filter is the task's state, and a watermark advanced by showing something is
    // what makes the same task vanish on the next call.
    const floor = options.sinceStart ? 0 : Number(view.binding.since_ts) || 0

    for (const task of view.ledger.tasks) {
      const decision = view.decisions.get(task.id)
      const mine = MINE(task, me, capabilities)
      if (!options.all) {
        if (!mine) continue
        if (task.from === me) continue
        if (Number(task.ts) <= floor) continue
        if (decision?.decision === 'rejected') continue
        if (task.closed) continue
        if (task.takenBy !== undefined && task.takenBy !== me) {
          taken += 1
          continue
        }
      }
      entries.push({ view, task, ...(decision ? { decision } : {}) })
    }
  }

  entries.sort((left, right) => Number(left.task.ts) - Number(right.task.ts))
  return { entries, views: [...views], unreadable, orphans, hidden, taken }
}

/** The highest ts an inbox saw, so the caller can advance the cursor without guessing. */
export function newestTs(report: InboxReport): string | undefined {
  return report.entries.at(-1)?.task.ts
}
