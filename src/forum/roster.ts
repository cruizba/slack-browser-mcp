/**
 * The roster is derived from `register` envelopes in the archive, one entry per
 * `name@host`, the latest `sent` winning.
 *
 * It is informational, not authoritative: `from` is asserted by whoever posts, so
 * anybody in the channel can claim to be anybody. The roster exists to warn about
 * a name with two hosts and to resolve `to: builder` to a single agent; the gate
 * that matters is the local approval in front of every task.
 */

import type { ArchivedMessage } from '../archive/store.ts'
import { decode } from './envelope.ts'

export interface Peer {
  from: string
  name: string
  host: string
  capabilities: string[]
  sent: string
  ts: string
  /**
   * The Slack user that posted the register: the person this agent works for. It
   * comes from the archived message, not from the envelope, because the envelope
   * is asserted by whoever posts and this field has to be the account.
   */
  owner?: string
  /** What the peer last claimed about itself; `ts` is when, so a reader can age it. */
  state?: 'listening' | 'idle'
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
    const envelope = decode(message.text)
    if (envelope?.kind !== 'register' && envelope?.kind !== 'presence') continue
    if (retired.has(envelope.from)) continue
    if (envelope.kind === 'presence' && envelope.state === 'gone') {
      byFrom.delete(envelope.from)
      continue
    }
    const previous = byFrom.get(envelope.from)
    byFrom.set(envelope.from, {
      from: envelope.from,
      name: envelope.name,
      host: envelope.host,
      capabilities: envelope.capabilities,
      sent: envelope.sent,
      ts: message.ts,
      // A presence message may drop the owner when its account is unnamed; the
      // person behind the peer does not disappear because one message did.
      ...(message.user ? { owner: message.user } : previous?.owner ? { owner: previous.owner } : {}),
      ...(envelope.kind === 'presence' && envelope.state !== 'gone'
        ? { state: envelope.state }
        : previous?.state
          ? { state: previous.state }
          : {}),
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

export type PeerStatus = 'listening' | 'idle' | 'out'

/**
 * Where a peer stands, from the only evidence the channel carries: when it last
 * said anything and what it said. `out` is the honest end of the scale — a machine
 * that has not announced itself in a day is not there to answer, whether it is off,
 * asleep or the person stopped using it — and it is time-based rather than
 * message-based so nothing has to be posted to make it true.
 */
export function peerStatus(
  peer: Peer,
  options: { presenceSeconds: number; ttlHours: number; now: Date },
): PeerStatus {
  const ageMs = options.now.getTime() - Number(peer.ts) * 1000
  if (ageMs > options.ttlHours * 3_600_000) return 'out'
  if (peer.state === 'listening' && ageMs < options.presenceSeconds * 1000) return 'listening'
  return 'idle'
}

export function peerLabel(peer: Peer): string {
  const caps = peer.capabilities.length > 0 ? ` [${peer.capabilities.join(', ')}]` : ''
  return `${peer.from}${caps}`
}
