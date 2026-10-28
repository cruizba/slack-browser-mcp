/**
 * The wire format: one JSON object inside a fenced `forum` block, behind a line
 * a person can read.
 *
 * The fence lives in the message `text`, not in Slack's `metadata`, because
 * metadata needs a schema registered in an app manifest and an `xoxc` session
 * has no app: Slack would answer `invalid_metadata_schema` and drop it silently.
 * `text` is also the only field the archive keeps, so the ledger can rebuild
 * every task from disk without asking Slack again.
 *
 * Decoding is deliberately forgiving: a channel is full of prose, and one
 * malformed message must never break the thread. Anything that does not parse is
 * simply not an envelope; `looksLikeEnvelope` is what lets a caller count the
 * ones it could not read instead of pretending they were never there.
 */

import { z } from 'zod'

import { MAX_TEXT_CHARS } from '../slack/assist.ts'
import { StoreError } from '../errors.ts'
import { ID_LENGTH, isId } from './id.ts'

export const FORUM_FENCE = 'forum'
export const MAX_BODY_CHARS = 2000
export const MAX_TITLE_CHARS = 120
export const MAX_FROM_CHARS = 64
export const MAX_TARGET_CHARS = 64
export const MAX_NAME_CHARS = 32
export const MAX_TAG_CHARS = 24
export const MAX_DEADLINE_CHARS = 24

const FENCE = /(?:^|\n)[ \t]*```forum[ \t]*\r?\n([\s\S]*?)\r?\n[ \t]*```/

const idSchema = z.string().length(ID_LENGTH).refine(isId, 'not a forum id')

const who = z.string().trim().min(1).max(MAX_FROM_CHARS)
const when = z.string().trim().min(10).max(MAX_DEADLINE_CHARS)
const tag = z.string().trim().min(1).max(MAX_TAG_CHARS)
const body = z.string().trim().min(1).max(MAX_BODY_CHARS)

const common = {
  v: z.literal(1),
  id: idSchema,
  from: who,
  sent: when,
}

const registerSchema = z
  .object({
    ...common,
    kind: z.literal('register'),
    name: z.string().trim().min(1).max(MAX_NAME_CHARS),
    host: z.string().trim().min(1).max(MAX_NAME_CHARS),
    capabilities: z.array(tag).max(8).default([]),
    version: z.string().trim().max(16).optional(),
  })
  .refine((value) => value.from === `${value.name}@${value.host}`, {
    message: 'from must be name@host',
    path: ['from'],
  })

const taskSchema = z
  .object({
    ...common,
    kind: z.literal('task'),
    to: z.string().trim().min(1).max(MAX_TARGET_CHARS),
    title: z.string().trim().min(1).max(MAX_TITLE_CHARS),
    body,
    tag: tag.optional(),
    deadline: when.optional(),
    parent: idSchema.optional(),
    depth: z.number().int().min(0).max(9).optional(),
  })
  .refine((value) => value.tag === undefined || value.to === '*', {
    message: 'tag only narrows a broadcast (to: "*")',
    path: ['tag'],
  })

const replyBase = {
  ...common,
  task: idSchema,
}

const optionalBody = body.optional()

const acceptSchema = z.object({ ...replyBase, kind: z.literal('accept'), body: optionalBody })
const declineSchema = z.object({ ...replyBase, kind: z.literal('decline'), body: optionalBody })
const progressSchema = z.object({ ...replyBase, kind: z.literal('progress'), body })
const resultSchema = z.object({
  ...replyBase,
  kind: z.literal('result'),
  body,
  ok: z.boolean().default(true),
})
const errorSchema = z.object({ ...replyBase, kind: z.literal('error'), body })
const ackSchema = z.object({ ...replyBase, kind: z.literal('ack'), body: optionalBody })
const reopenSchema = z.object({ ...replyBase, kind: z.literal('reopen'), body })
const cancelSchema = z.object({ ...replyBase, kind: z.literal('cancel'), body: optionalBody })

export const EnvelopeSchema = z.discriminatedUnion('kind', [
  registerSchema,
  taskSchema,
  acceptSchema,
  declineSchema,
  progressSchema,
  resultSchema,
  errorSchema,
  ackSchema,
  reopenSchema,
  cancelSchema,
])

export type Envelope = z.infer<typeof EnvelopeSchema>
export type TaskEnvelope = z.infer<typeof taskSchema>
export type RegisterEnvelope = z.infer<typeof registerSchema>
export type ReplyEnvelope = z.infer<typeof acceptSchema>

export const REPLY_KINDS = [
  'accept',
  'decline',
  'progress',
  'result',
  'error',
  'ack',
  'reopen',
  'cancel',
] as const

export type ReplyKind = (typeof REPLY_KINDS)[number]

export function isReply(envelope: Envelope): envelope is Extract<Envelope, { task: string }> {
  return envelope.kind !== 'task' && envelope.kind !== 'register'
}

/**
 * Returns the JSON one-liner. Pretty-printing would cost characters the 4000-char
 * message budget does not have, and a single line is what a reader can copy.
 */
export function encode(envelope: Envelope): string {
  return JSON.stringify(EnvelopeSchema.parse(envelope))
}

export function render(summary: string, envelope: Envelope): string {
  return `${summary}\n\n\`\`\`${FORUM_FENCE}\n${encode(envelope)}\n\`\`\``
}

/** True when the text carries a fence at all, readable or not; used to count failures. */
export function looksLikeEnvelope(text: string | undefined): boolean {
  return text !== undefined && FENCE.test(text)
}

export function decode(text: string | undefined): Envelope | undefined {
  const match = text?.match(FENCE)
  if (!match) return undefined
  let parsed: unknown
  try {
    parsed = JSON.parse(match[1]!.trim())
  } catch {
    return undefined
  }
  const result = EnvelopeSchema.safeParse(parsed)
  return result.success ? result.data : undefined
}

function firstLine(body: string | undefined, max = 80): string {
  const flat = (body ?? '').split('\n')[0]!.trim()
  return flat.length > max ? `${flat.slice(0, max - 1)}…` : flat
}

/**
 * The human line above the fence. For a task it says who is asking whom; for a
 * reply it says what happened, because in a thread the reader already knows
 * which task it is.
 */
export function summaryFor(envelope: Envelope): string {
  switch (envelope.kind) {
    case 'register':
      return `${envelope.from} joined the forum`
    case 'task':
      return envelope.tag
        ? `${envelope.from} asks anyone who can ${envelope.tag}: ${envelope.title}`
        : `${envelope.from} asks ${envelope.to}: ${envelope.title}`
    case 'accept':
      return `${envelope.from} accepted ${envelope.task}`
    case 'decline':
      return `${envelope.from} declined ${envelope.task}${envelope.body ? `: ${firstLine(envelope.body)}` : ''}`
    case 'progress':
      return `${envelope.from} on ${envelope.task}: ${firstLine(envelope.body)}`
    case 'result':
      return envelope.ok === false
        ? `${envelope.from} could not finish ${envelope.task}: ${firstLine(envelope.body)}`
        : `${envelope.from} finished ${envelope.task}: ${firstLine(envelope.body)}`
    case 'error':
      return `${envelope.from} failed ${envelope.task}: ${firstLine(envelope.body)}`
    case 'ack':
      return `${envelope.from} received ${envelope.task}`
    case 'reopen':
      return `${envelope.from} reopened ${envelope.task}: ${firstLine(envelope.body)}`
    case 'cancel':
      return `${envelope.from} cancelled ${envelope.task}${envelope.body ? `: ${firstLine(envelope.body)}` : ''}`
  }
}

/**
 * Slack cuts `text` at 4000 and a section block at 2900. Beyond 4000 the message
 * is refused rather than truncated: a work request with its tail missing is a
 * wrong request, and the caller can shorten it or move the detail into a link.
 */
export function assertFits(text: string): void {
  if (text.length > MAX_TEXT_CHARS) {
    throw new StoreError(
      `The message is ${text.length} characters and Slack accepts ${MAX_TEXT_CHARS}, ` +
        'so the end would be cut off. Shorten the body or point at the detail instead of pasting it.',
    )
  }
}
