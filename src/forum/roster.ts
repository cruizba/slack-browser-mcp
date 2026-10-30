/**
 * The roster is derived from the announcements in the archive, one entry per
 * `name@host`, the latest one winning.
 *
 * It carries two separate answers, because they come from two different senders: a
 * `beacon` says the machine is up (the poller may post it, so a daemon can keep a
 * peer from rotting without ever speaking for its user), and a `presence` says what
 * the person in front of a session is doing. Reading them as one number is what let
 * a heartbeat claim "listening" for a day after everybody had gone home.
 *
 * It is informational, not authoritative: `from` is asserted by whoever posts, so
 * anybody in the channel can claim to be anybody. The roster exists to warn about a
 * name with two hosts and to resolve `to: builder` to a single agent; the gate that
 * matters is the local approval in front of every task.
 */

import type { ArchivedMessage } from '../archive/store.ts'
import { envelopeOf } from './carrier.ts'
import type { Envelope } from './envelope.ts'

export interface Peer {
  from: string
  name: string
  host: string
  capabilities: string[]
  sent: string
  /** Newest message from this peer of any kind: what machine liveness is measured from. */
  ts: string
  /**
   * The Slack user that posted the announcement: the person this agent works for.
   * It comes from the archived message, not from the envelope, because the envelope
   * is asserted by whoever posts and this field has to be the account.
   */
  owner?: string
  /** Newest message that is not a beacon. A daemon cannot make a peer look busy. */
  interactiveTs?: string
  /** What the last presence message claimed about the person; absent means no claim. */
  state?: 'here' | 'away'
  note?: string
}

function claimOf(envelope: Envelope): { state: 'here' | 'away'; note?: string } | undefined {
  if (envelope.kind !== 'presence') return undefined
  if (envelope.state !== 'here' && envelope.state !== 'away') return undefined
  return { state: envelope.state, ...(envelope.note ? { note: envelope.note } : {}) }
}

export function rosterFrom(
  messages: readonly ArchivedMessage[],
  ignored: readonly string[] = [],
): Peer[] {
  const retired = new Set(ignored)
  const byFrom = new Map<string, Peer>()
  // The archive hands messages back oldest first, so a later announcement wins.
  for (const message of messages) {
    if (message.deleted_at) continue
    const envelope = envelopeOf(message)
    if (!envelope) continue
    if (retired.has(envelope.from)) continue
    if (envelope.kind === 'presence' && envelope.state === 'gone') {
      byFrom.delete(envelope.from)
      continue
    }

    const previous = byFrom.get(envelope.from)
    if (envelope.kind !== 'register' && envelope.kind !== 'beacon' && envelope.kind !== 'presence') {
      // A task or a reply proves the peer was there, and nothing else.
      if (previous) {
        byFrom.set(envelope.from, { ...previous, ts: message.ts, interactiveTs: message.ts })
      }
      continue
    }

    // A presence message always speaks for the person, even when the answer is
    // "no claim" (the old `listening`/`idle` shape); a register or a beacon leaves
    // whatever the person last said alone.
    const claim = claimOf(envelope)
    const claimFields =
      envelope.kind === 'presence'
        ? claim
          ? { state: claim.state, ...(claim.note ? { note: claim.note } : {}) }
          : {}
        : previous?.state
          ? { state: previous.state, ...(previous.note ? { note: previous.note } : {}) }
          : {}
    // Only what a session writes counts as somebody being there. A beacon is the
    // poller, and a legacy `listening` chain is the poller under the old name.
    const interactive =
      envelope.kind !== 'beacon' && (envelope.kind !== 'presence' || claim !== undefined)

    byFrom.set(envelope.from, {
      from: envelope.from,
      name: envelope.name,
      host: envelope.host,
      capabilities:
        envelope.kind === 'register' ? envelope.capabilities : (previous?.capabilities ?? []),
      sent: envelope.sent,
      ts: message.ts,
      ...(interactive
        ? { interactiveTs: message.ts }
        : previous?.interactiveTs
          ? { interactiveTs: previous.interactiveTs }
          : {}),
      ...(message.user
        ? { owner: message.user }
        : previous?.owner
          ? { owner: previous.owner }
          : {}),
      ...claimFields,
    })
  }
  return [...byFrom.values()].sort((left, right) => left.from.localeCompare(right.from))
}

export function peerFor(roster: readonly Peer[], from: string): Peer | undefined {
  return roster.find((peer) => peer.from === from)
}

export function hostsOf(roster: readonly Peer[], name: string): string[] {
  return roster.filter((peer) => peer.name === name).map((peer) => peer.host)
}

export type MachineStatus = 'up' | 'out'
export type HumanStatus = 'here' | 'away' | 'unknown'

function ageOf(ts: string, now: Date): number {
  return now.getTime() - Number(ts) * 1000
}

/**
 * Whether the machine is there to answer at all. Silence is the only way out, so
 * nothing has to be posted to make it true: a box that is off, asleep or no longer
 * running the poller stops announcing and ages out on its own.
 */
export function machineStatus(peer: Peer, options: { ttlHours: number; now: Date }): MachineStatus {
  return ageOf(peer.ts, options.now) > options.ttlHours * 3_600_000 ? 'out' : 'up'
}

/**
 * Whether a person is in front of it now. `here` is a claim with a short life,
 * refreshed for free by any message the session writes, so a long turn does not
 * have to announce itself every hour. `away` is explicit and stands until something
 * replaces it, because only the person knows when they come back.
 */
export function humanStatus(
  peer: Peer,
  options: { hereSeconds: number; ttlHours: number; now: Date },
): HumanStatus {
  if (machineStatus(peer, options) === 'out') return 'unknown'
  if (peer.state === 'away') return 'away'
  if (peer.interactiveTs === undefined) return 'unknown'
  return ageOf(peer.interactiveTs, options.now) < options.hereSeconds * 1000 ? 'here' : 'unknown'
}

export function peerLabel(peer: Peer): string {
  const caps = peer.capabilities.length > 0 ? ` [${peer.capabilities.join(', ')}]` : ''
  return `${peer.from}${caps}`
}
