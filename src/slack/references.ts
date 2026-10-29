/**
 * Slack stores references as markup, not as names: a mention is `<@U123>`, a
 * channel is `<#C123|general>`, a user group is `<!subteam^S123|@team>`. An agent
 * reading a transcript needs the name, and a raw id costs context without
 * meaning anything, so the render pass resolves what the directory knows and
 * leaves the rest as it found it.
 *
 * Resolution is display-only: the archive keeps the markup so a later refresh of
 * the directory can improve old messages instead of freezing a stale name.
 */

import { unescapeEntities } from './entities.ts'

/** What the resolver can answer. Missing answers fall back to the raw id. */
export interface ReferenceScope {
  user(id: string): string | undefined
  channel(id: string): string | undefined
  usergroup(id: string): string | undefined
}

const REFERENCE = /<([^<>]+)>/g

const SPECIAL_MENTIONS = new Set(['here', 'channel', 'everyone'])

function splitLabel(body: string): { target: string; label?: string } {
  const bar = body.indexOf('|')
  if (bar === -1) return { target: body }
  return { target: body.slice(0, bar), label: body.slice(bar + 1) }
}

function resolveOne(body: string, scope: ReferenceScope | undefined): string {
  const { target, label } = splitLabel(body)

  if (target.startsWith('@')) {
    const id = target.slice(1)
    return `@${label || scope?.user(id) || id}`
  }

  if (target.startsWith('#')) {
    const id = target.slice(1)
    return `#${label || scope?.channel(id) || id}`
  }

  if (target.startsWith('!subteam^')) {
    if (label) return label
    const id = target.slice('!subteam^'.length)
    return `@${scope?.usergroup(id) || id}`
  }

  if (target.startsWith('!') && SPECIAL_MENTIONS.has(target.slice(1))) {
    return `@${target.slice(1)}`
  }

  // <!date^1518032400^{date_short}|Feb 7, 2018>: Slack always ships the fallback.
  if (target.startsWith('!')) {
    return label ?? `<${body}>`
  }

  if (target.includes('://') || target.startsWith('mailto:')) {
    return label ? `${label} (${target})` : target
  }

  return `<${body}>`
}

/**
 * Turns message markup into the text a person would have read in Slack. Text
 * with no markup comes back unchanged, so the common case costs one regex.
 */
export function resolveReferences(text: string, scope?: ReferenceScope): string {
  if (!text.includes('<')) return unescapeEntities(text)
  return unescapeEntities(text.replace(REFERENCE, (match, body: string) => resolveOne(body, scope)))
}
