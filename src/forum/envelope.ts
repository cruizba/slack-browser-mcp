/**
 * The wire format: a human summary in the message `text`, and the envelope itself as
 * a `forum-<id>.json` attachment on the same message.
 *
 * It travelled inside a fenced `forum` block in `text` up to v0.2.1, which is why
 * the fence is still decoded here: the channels hold those messages and the ledger
 * rebuilds itself from them. Nothing writes one any more. Slack escapes `&`, `<`
 * and `>` in `text`, so a body near any limit arrived longer than it was sent, and a
 * fence also cannot carry a body with three backticks in it — exactly what a pasted
 * Slack thread has. A file is bytes nobody rewrites, so the body budget is a real
 * megabyte and a transcript fits.
 *
 * Not Slack's `metadata` either: that needs a schema registered in an app manifest
 * and an `xoxc` session has no app, so Slack answers `invalid_metadata_schema` and
 * drops it silently.
 *
 * Decoding is deliberately forgiving: a channel is full of prose, and one malformed
 * message must never break the thread. Anything that does not parse is simply not an
 * envelope; `carriesEnvelope` in `carrier.ts` is what lets a caller count the ones it
 * could not read instead of pretending they were never there.
 */

import { z } from 'zod'

import { MAX_TEXT_CHARS } from '../slack/assist.ts'
import { unescapeEntities } from '../slack/entities.ts'
import { StoreError } from '../errors.ts'
import { ID_LENGTH, isId } from './id.ts'

export const FORUM_FENCE = 'forum'
/** What a body may weigh once it travels as a file: a whole transcript, not a paste. */
export const MAX_BODY_BYTES = 1024 * 1024
export const MAX_TITLE_CHARS = 120
export const MAX_FROM_CHARS = 64
export const MAX_TARGET_CHARS = 64
export const MAX_NAME_CHARS = 32
export const MAX_TAG_CHARS = 24
export const MAX_DEADLINE_CHARS = 24

/**
 * Slack flattens `text` to a single line when the message carries `blocks`: the
 * stored fallback joins every section with spaces, so a line-anchored fence stops
 * matching on the very messages this tool posts. The fence is therefore found by
 * its delimiters and its JSON braces, not by its newlines.
 */
const FENCE = /```forum[ \t\r\n]*(\{[\s\S]*?\})[ \t\r\n]*```/

const idSchema = z.string().length(ID_LENGTH).refine(isId, 'not a forum id')

const who = z.string().trim().min(1).max(MAX_FROM_CHARS)
const when = z.string().trim().min(10).max(MAX_DEADLINE_CHARS)
const tag = z.string().trim().min(1).max(MAX_TAG_CHARS)

/**
 * A field carrying a fence would end a fenced envelope early and unparse the
 * message. Only the legacy format can be broken that way, so only the short fields
 * keep the rule: the body is the one field whose whole job is to hold a pasted
 * transcript, backticks included.
 */
function fenceFree<T extends z.ZodType<string>>(schema: T) {
  return schema.refine((value) => !value.includes('```'), {
    message: 'no triple backticks inside this field: they would end a fenced envelope',
  })
}

const body = z
  .string()
  .trim()
  .min(1)
  .refine((value) => Buffer.byteLength(value, 'utf8') <= MAX_BODY_BYTES, {
    message: `a body carries at most ${MAX_BODY_BYTES} bytes, which is a whole thread; link to the file instead of pasting more`,
  })

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

/**
 * A machine saying it is up, and nothing else. It is the one envelope a background
 * poll may post: it claims nothing about the person in front of it, so a daemon
 * cannot speak for anybody. `name` and `host` are there so the roster needs no
 * other message to keep the entry alive.
 */
const beaconSchema = z
  .object({
    ...common,
    kind: z.literal('beacon'),
    name: z.string().trim().min(1).max(MAX_NAME_CHARS),
    host: z.string().trim().min(1).max(MAX_NAME_CHARS),
  })
  .refine((value) => value.from === `${value.name}@${value.host}`, {
    message: 'from must be name@host',
    path: ['from'],
  })

/**
 * What a session claims about the person using it. `here` expires on its own
 * (`here_seconds`); `away` is explicit and carries the note that says until when.
 * `listening` and `idle` are the old shape: still decoded so a channel written by
 * the previous version keeps its peers, but never posted again — a heartbeat is a
 * fact about the machine, and it now travels as a beacon.
 */
const presenceSchema = z
  .object({
    ...common,
    kind: z.literal('presence'),
    name: z.string().trim().min(1).max(MAX_NAME_CHARS),
    host: z.string().trim().min(1).max(MAX_NAME_CHARS),
    /** `gone` retires the peer: readers drop it from the roster. */
    state: z.enum(['here', 'away', 'gone', 'listening', 'idle']),
    note: fenceFree(z.string().trim().max(80)).optional(),
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
    title: fenceFree(z.string().trim().min(1).max(MAX_TITLE_CHARS)),
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
  beaconSchema,
  presenceSchema,
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
export type BeaconEnvelope = z.infer<typeof beaconSchema>
export type PresenceEnvelope = z.infer<typeof presenceSchema>
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
  return (
    envelope.kind !== 'task' &&
    envelope.kind !== 'register' &&
    envelope.kind !== 'beacon' &&
    envelope.kind !== 'presence'
  )
}

/** The values a fenced envelope carried before Slack escaped them. */
function unescapeForm(value: unknown): unknown {
  if (typeof value === 'string') return unescapeEntities(value)
  if (Array.isArray(value)) return value.map(unescapeForm)
  if (value !== null && typeof value === 'object') {
    return Object.fromEntries(
      Object.entries(value).map(([key, entry]) => [key, unescapeForm(entry)]),
    )
  }
  return value
}

/**
 * The bytes of the attachment, one line so a reader can open it in anything.
 * Pretty-printing would buy nothing: nothing parses this by eye.
 */
export function encode(envelope: Envelope): string {
  return JSON.stringify(EnvelopeSchema.parse(envelope))
}

/**
 * The fenced form, which nothing writes any more and the decoder still understands.
 * Kept as the fixture that keeps the legacy reader honest.
 */
export function render(summary: string, envelope: Envelope): string {
  return `${summary}\n\n\`\`\`${FORUM_FENCE}\n${encode(envelope)}\n\`\`\``
}

/**
 * Undoes Slack's escaping on every string the JSON carries. It has to happen after
 * `JSON.parse`, never before: the quotes and braces that make the JSON are the
 * sender's, and only the values inside them travelled through Slack's escaping.
 */
export function decode(text: string | undefined): Envelope | undefined {
  const match = text?.match(FENCE)
  if (!match) return undefined
  return decodeAttachment(match[1]!.trim(), true)
}

/**
 * The envelope as it travelled in a file. Slack never touches those bytes, so the
 * values arrive exactly as they were sent and nothing is unescaped: a body quoting
 * `&lt;` on purpose has to stay that way. `legacy` is the fenced reader asking for
 * its escaping undone.
 */
export function decodeAttachment(
  json: string | undefined,
  legacy = false,
): Envelope | undefined {
  if (json === undefined) return undefined
  let parsed: unknown
  try {
    parsed = JSON.parse(json)
  } catch {
    return undefined
  }
  const result = EnvelopeSchema.safeParse(legacy ? unescapeForm(parsed) : parsed)
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
    case 'beacon':
      return `${envelope.from} is up`
    case 'presence':
      return envelope.state === 'gone'
        ? `${envelope.from} left the forum${envelope.note ? `: ${envelope.note}` : ''}`
        : `${envelope.from} is ${envelope.state}${envelope.note ? `: ${envelope.note}` : ''}`
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
