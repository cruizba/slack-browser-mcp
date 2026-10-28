/**
 * Who an agent is on the forum: a name, a host, and the capabilities it
 * advertises.
 *
 * The identity is per process, not per machine. Two agents on one laptop share
 * `forum.json`, so a single name would make 1:1 addressing collapse into a queue:
 * `builder@laptop` and `reviewer@laptop` come from the same file and stay
 * distinct. The resolution order is environment, then the MCP client name, then
 * whatever the file says, so the common case needs no configuration.
 */

import { hostname } from 'node:os'

export const AGENT_ENV = 'SLACK_BROWSER_MCP_AGENT'
export const MAX_NAME_CHARS = 32
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

export function normalizeCapabilities(raw: readonly string[]): string[] {
  const seen = new Set<string>()
  for (const entry of raw) {
    const tag = normalizeName(entry, 24)
    if (tag) seen.add(tag)
    if (seen.size >= MAX_CAPABILITIES) break
  }
  return [...seen].sort()
}

export function defaultHost(): string {
  const short = hostname().split('.')[0] ?? ''
  return normalizeName(short) || 'localhost'
}

export function labelOf(identity: Pick<Identity, 'name' | 'host'>): string {
  return `${identity.name}@${identity.host}`
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
