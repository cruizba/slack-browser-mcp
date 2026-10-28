/**
 * How a task and an inbox look on both surfaces.
 *
 * One renderer, two callers: the CLI prints these lines as they are and the MCP
 * tools return them, so the agent and the person reading over its shoulder are
 * looking at the same thing.
 */

import { meLabel, type ForumContext } from './context.ts'
import type { BindingView } from './inbox.ts'
import type { Decision } from './decisions.ts'
import type { InboxReport } from './inbox.ts'
import { stateLabel, type ForumTask } from './ledger.ts'
import type { Binding } from './binding.ts'
import type { ForumLimits } from './limits.ts'
import { peerStatus } from './roster.ts'

export function ago(ts: string | number, now: Date): string {
  const then = typeof ts === 'number' ? ts : Number(ts) * 1000
  if (!Number.isFinite(then) || then === 0) return 'unknown'
  const minutes = Math.max(0, Math.round((now.getTime() - then) / 60_000))
  if (minutes < 1) return 'now'
  if (minutes < 60) return `${minutes}m`
  const hours = Math.round(minutes / 60)
  if (hours < 48) return `${hours}h`
  return `${Math.round(hours / 24)}d`
}

export function decisionLabel(decision: Decision | undefined): string {
  if (!decision) return 'local:-'
  const self = decision.self_approved ? '(self)' : ''
  return `local:${decision.decision}${self}`
}

/**
 * An agent label as a person reads it: the agent name, plus the person behind it
 * when the cached directory can name the Slack account that registered it.
 */
export function peerLabel(from: string, view?: BindingView): string {
  const owner = view?.roster.find((peer) => peer.from === from)?.owner
  const person = owner ? (view?.people.get(owner) ?? owner) : undefined
  return person ? `${from} (${person})` : from
}

/**
 * The roster as each surface prints it. Peers are discovered from the channel, so
 * this is what "who is on the forum" looks like after a plain read.
 */
export function renderPeers(view: BindingView, now: Date, limits: ForumLimits): string[] {
  const lines = [`${channelLabel(view.binding)} (${view.binding.channel})`]
  if (view.roster.length === 0) {
    lines.push('  nobody registered yet')
    return lines
  }
  for (const peer of view.roster) {
    const caps = peer.capabilities.length > 0 ? ` [${peer.capabilities.join(', ')}]` : ''
    const person = peer.owner ? (view.people.get(peer.owner) ?? peer.owner) : undefined
    const who = person ? ` · ${person}` : ''
    const status = peerStatus(peer, {
      presenceSeconds: limits.presence_seconds,
      ttlHours: limits.peer_ttl_hours,
      now,
    })
    const seen = ago(peer.ts, now)
    const text =
      status === 'listening'
        ? `listening, seen ${seen} ago`
        : status === 'out'
          ? `out, last seen ${seen} ago`
          : peer.state
            ? `idle, last seen ${seen} ago`
            : `last seen ${seen} ago`
    lines.push(`  ${peer.from}${who}${caps} — ${text}`)
  }
  return lines
}

export function channelLabel(binding: Binding): string {
  return `${binding.private ? 'private:' : '#'}${binding.name}`
}

export function inboxLine(
  task: ForumTask,
  decision: Decision | undefined,
  now: Date,
  withChannel?: string,
): string {
  const channel = withChannel ? `${withChannel} · ` : ''
  const taken = task.takenBy ? ` taken:${task.takenBy}` : ''
  return (
    `${channel}${task.id}  ${stateLabel(task, now).padEnd(9)} ${decisionLabel(decision).padEnd(16)} ` +
    `<- ${task.from}${taken}  ${task.title}  ${ago(task.ts, now)}`
  )
}

export function renderInbox(
  context: ForumContext,
  report: InboxReport,
  options: { offline?: boolean; note?: string } = {},
): string {
  const me = meLabel(context) || 'unregistered'
  const bindings = report.views
    .map((view) => `${channelLabel(view.binding)} (${view.binding.channel})`)
    .join(', ')
  const counts = [
    `${report.entries.length} task(s)`,
    report.taken > 0 ? `${report.taken} taken` : '',
    report.hidden > 0 ? `${report.hidden} from unknown or retired peers` : '',
    report.unreadable > 0 ? `${report.unreadable} unreadable envelope(s)` : '',
    report.orphans > 0 ? `${report.orphans} orphan repl(y/ies)` : '',
  ].filter(Boolean)

  const head =
    `agent forum as ${me}` +
    (bindings ? ` · ${bindings}` : ' · no channel bound') +
    `${options.offline ? ' · offline' : ''}${options.note ?? ''}`
  const body =
    report.entries.length === 0
      ? 'nothing pending'
      : report.entries
          .map((entry) =>
            inboxLine(
              entry.task,
              entry.decision,
              context.now,
              report.views.length > 1 ? channelLabel(entry.view.binding) : undefined,
            ),
          )
          .join('\n')
  return `${head}\n${counts.join(' · ')}\n${body}`
}

function eventLine(task: ForumTask, now: Date): string {
  return task.events
    .map((event) => {
      const body = event.body ? `: ${event.body.split('\n')[0]}` : ''
      return `  [${event.ts}] ${event.kind} from ${event.from}${body}`
    })
    .join('\n')
}

export function renderTask(
  context: ForumContext,
  task: ForumTask,
  binding: Binding,
  decision: Decision | undefined,
  view?: BindingView,
): string {
  const header =
    `${task.id}  ${stateLabel(task, context.now)}  ${decisionLabel(decision)}  ` +
    `${binding.workspace}/${channelLabel(binding)}`
  const person = task.owner ? (view?.people.get(task.owner) ?? task.owner) : undefined
  const route = `${task.from}${person ? ` (${person})` : ''} -> ${task.to}${
    task.tag ? `#${task.tag}` : ''
  } · sent ${task.sent}` +
    (task.deadline ? ` · deadline ${task.deadline}` : '') +
    (task.reopens > 0 ? ` · reopened ${task.reopens}x` : '')
  const thread = task.events.length > 0 ? `\n\n${eventLine(task, context.now)}` : ''
  const ignored =
    task.ignored.length > 0
      ? `\n\n  ${task.ignored.length} ignored event(s): ${task.ignored
          .map((event) => `${event.kind} from ${event.from}`)
          .join(', ')}`
      : ''
  return `${header}\n${route}\n\n${task.title}\n${task.body}${thread}${ignored}`
}
