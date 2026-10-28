/**
 * The one place the forum's state is assembled: which file, which archive, which
 * local identity, which limits.
 *
 * Both surfaces (CLI and MCP) build this and then call the same service, so a rule
 * cannot exist on one side and not the other. Reading the identity is part of it
 * because "am I registered" changes what every command is allowed to do.
 */

import { ArchiveStore } from '../archive/store.ts'
import { ForumError } from '../errors.ts'
import { ForumStore, limitsOf, type ForumFile, type StoredIdentity } from './binding.ts'
import { DecisionLog } from './decisions.ts'
import { labelOf } from './identity.ts'
import type { ForumLimits } from './limits.ts'

export interface ForumContext {
  forum: ForumStore
  file: ForumFile
  archive: ArchiveStore
  decisions: DecisionLog
  /** MCP client name (or the fallback) this process acts as. */
  identityKey: string
  identity?: StoredIdentity
  limits: ForumLimits
  now: Date
}

export interface OpenOptions {
  clientName?: string
  now?: Date
  forum?: ForumStore
  archive?: ArchiveStore
  decisions?: DecisionLog
}

export async function openForum(options: OpenOptions = {}): Promise<ForumContext> {
  const forum = options.forum ?? new ForumStore()
  const file = await forum.read()
  const resolved = forum.resolveIdentity(file, options.clientName)
  return {
    forum,
    file,
    archive: options.archive ?? new ArchiveStore(),
    decisions: options.decisions ?? new DecisionLog(),
    identityKey: resolved.key,
    ...(resolved.identity ? { identity: resolved.identity } : {}),
    limits: limitsOf(file),
    now: options.now ?? new Date(),
  }
}

export function meLabel(context: ForumContext): string {
  return context.identity ? labelOf(context.identity) : ''
}

export function requireIdentity(context: ForumContext): StoredIdentity {
  if (context.identity) return context.identity
  throw new ForumError(
    'No forum identity on this machine yet.',
    'Pick a name once: `slack-browser agent-forum register <name>`.',
  )
}

/** Re-reads the file after a write so callers never act on a stale snapshot. */
export async function reload(context: ForumContext): Promise<ForumContext> {
  return openForum({
    clientName: context.identityKey,
    now: context.now,
    forum: context.forum,
    archive: context.archive,
    decisions: context.decisions,
  })
}
