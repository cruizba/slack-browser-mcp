/**
 * Turning `to` into a peer.
 *
 * `to` is a string on the wire, but it carries three intended shapes: one exact
 * agent (`name@host`), one name that must resolve uniquely (`name`), and a
 * broadcast (`*`), optionally narrowed by a capability tag. Resolving here rather
 * than at posting time keeps the rules in one place: an ambiguous name is refused
 * with the candidates instead of guessed, because sending work to the wrong agent
 * looks exactly like sending it to the right one.
 */

import { ForumError } from '../errors.ts'
import { normalizeName } from './identity.ts'
import { hostsOf, type Peer } from './roster.ts'

export type Target =
  | { kind: 'peer'; name: string; host?: string }
  | { kind: 'anyone'; tag?: string }

export function parseTarget(raw: string): Target {
  const value = raw.trim()
  if (value === '') {
    throw new ForumError(
      'A task needs a destination.',
      'Use `name@host`, a unique `name`, or `*` for anyone (optionally with --tag).',
    )
  }
  if (value === '*') return { kind: 'anyone' }

  const parts = value.split('@')
  if (parts.length > 2) {
    throw new ForumError(`"${raw}" is not a destination.`, 'A peer is `name` or `name@host`.')
  }
  const name = normalizeName(parts[0] ?? '')
  if (!name) {
    throw new ForumError(`"${raw}" has no usable name.`, 'Names are letters, digits, dot, dash.')
  }
  const host = parts[1] === undefined ? undefined : normalizeName(parts[1])
  if (host === '') {
    throw new ForumError(`"${raw}" ends with an empty host.`, 'Drop the @ or name the host.')
  }
  return host ? { kind: 'peer', name, host } : { kind: 'peer', name }
}

export function targetLabel(target: Target): string {
  if (target.kind === 'anyone') return target.tag ? `*#${target.tag}` : '*'
  return target.host ? `${target.name}@${target.host}` : target.name
}

export function candidates(roster: readonly Peer[], target: Target): Peer[] {
  if (target.kind === 'anyone') {
    return target.tag === undefined
      ? [...roster]
      : roster.filter((peer) => peer.capabilities.includes(target.tag!))
  }
  const matching = roster.filter((peer) => peer.name === target.name)
  return target.host === undefined
    ? matching
    : matching.filter((peer) => peer.host === target.host)
}

export function resolvePeer(roster: readonly Peer[], target: Target): Peer {
  if (target.kind === 'anyone') {
    throw new ForumError('`*` does not name a single peer.')
  }
  const matching = candidates(roster, target)
  if (matching.length === 1) return matching[0]!
  if (matching.length === 0) {
    const near = hostsOf(roster, target.name)
    throw new ForumError(
      `No peer "${targetLabel(target)}" in this channel's roster.`,
      near.length > 0
        ? `"${target.name}" is known at: ${near.join(', ')}.`
        : 'Run `agent-forum peers`, or `agent-forum register <name>` if that agent is you.',
    )
  }
  throw new ForumError(
    `"${target.name}" matches ${matching.length} agents: ${matching.map((peer) => peer.from).join(', ')}.`,
    'Address one of them as `name@host`.',
  )
}
