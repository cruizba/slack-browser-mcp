/**
 * Who an agent is on the forum: a name, a host, and the kinds of work it takes.
 *
 * `capabilities` is what the agent said it accepts, set at `register` and announced
 * in the same envelope, so a peer — or the manager — can address work to the machine
 * that declared it. A task carries one `capability`, and it is a declaration, not a
 * filter: nothing is declined on its behalf.
 *
 * The identity is per process, not per machine. Two agents on one laptop share
 * `forum.json`, so a single name would make 1:1 addressing collapse into a queue:
 * `builder@laptop` and `reviewer@laptop` come from the same file and stay
 * distinct. The resolution order is environment, then the MCP client name, then
 * whatever the file says, so the common case needs no configuration.
 */

import { hostname } from 'node:os'

import { MAX_TAG_CHARS } from './envelope.ts'

export const AGENT_ENV = 'SLACK_BROWSER_MCP_AGENT'
export const MAX_NAME_CHARS = 32
/** The register envelope accepts this many; the parser refuses more, not silently fewer. */
export const MAX_CAPABILITIES = 8

export interface Identity {
  name: string
  host: string
  capabilities: string[]
}

/**
 * Slack ids, channel names and this file all end up in the same line, so a name
 * is kept to what survives a copy-paste: lowercase, no spaces, no `@` (it is the
 * separator), no line break.
 */
export function normalizeName(raw: string, max: number = MAX_NAME_CHARS): string {
  const flat = raw
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, '-')
    .replace(/-{2,}/g, '-')
    .replace(/^[-.]+|[-.]+$/g, '')
  return flat.slice(0, max).replace(/[-.]+$/g, '')
}

export function defaultHost(): string {
  const short = hostname().split('.')[0] ?? ''
  return normalizeName(short) || 'localhost'
}

export function labelOf(identity: Pick<Identity, 'name' | 'host'>): string {
  return `${identity.name}@${identity.host}`
}

/** Set equality: a re-register that only reorders the tags is not a change. */
export function sameCapabilities(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((tag) => b.includes(tag))
}

/**
 * An explicit `SLACK_BROWSER_MCP_AGENT` wins because it is scoped to one client
 * registration; the MCP client name is the zero-config default, and a plain shell
 * falls back to the file.
 */
export function agentName(candidates: {
  env?: NodeJS.ProcessEnv
  client?: string | undefined
  fallback?: string | undefined
}): string {
  const fromEnv = candidates.env?.[AGENT_ENV]
  if (fromEnv !== undefined) {
    const name = normalizeName(fromEnv)
    if (name) return name
  }
  if (candidates.client) {
    const name = normalizeName(candidates.client)
    if (name) return name
  }
  return normalizeName(candidates.fallback ?? '') || 'agent'
}

/**
 * A capability is matched as a string, so the same word has to come out of every
 * surface: `Review`, `review ` and `review` are one thing to a peer reading the
 * roster. Spaces become dashes because a roster line is space-separated.
 */
export function parseCapability(raw: string): string {
  const tag = raw.trim().toLowerCase().replace(/\s+/g, '-')
  if (!tag) throw new Error('a capability cannot be empty')
  if (tag.length > MAX_TAG_CHARS) {
    throw new Error(`"${tag}" is longer than ${MAX_TAG_CHARS} characters; capabilities are short words`)
  }
  return tag
}

export function parseCapabilities(raw: string): string[] {
  const tags = raw
    .split(',')
    .map((entry) => entry.trim())
    .filter((entry) => entry.length > 0)
    .map(parseCapability)
  const unique = [...new Set(tags)]
  if (unique.length > MAX_CAPABILITIES) {
    throw new Error(`at most ${MAX_CAPABILITIES} capabilities; narrow the list`)
  }
  return unique
}
