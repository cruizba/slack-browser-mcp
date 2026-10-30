/**
 * Where an envelope lives on a Slack message: its own file, `forum-<id>.json`, and
 * the message `text` carrying nothing but the summary a person reads.
 *
 * Everything that reads a message goes through `envelopeOf` so the two shapes never
 * have to be handled twice: the current one (a file, kept on the archived record as
 * `forum`) and the fenced one written up to v0.2.1, still in the channels and still
 * the only thing older messages have.
 *
 * The file name is the lookup key. Slack's `files.info` and `conversations.history`
 * both give a name, so a reader needs no manifest, no marker and no extra state: a
 * `forum-*.json` next to a message is the envelope, and anything else is an
 * attachment somebody posted.
 */

import type { ArchivedMessage } from '../archive/store.ts'
import { decode, decodeAttachment, MAX_BODY_BYTES, type Envelope } from './envelope.ts'

export const FORUM_FILE_PREFIX = 'forum-'
export const FORUM_FILE_SUFFIX = '.json'

/**
 * An envelope file bigger than this is not one of ours: the schema refuses the body
 * long before, so a huge `forum-*.json` is something a person attached and the sync
 * should not drag into the archive.
 */
export const MAX_ENVELOPE_FILE_BYTES = MAX_BODY_BYTES + 64 * 1024

/** What the file is called: the envelope id, so two messages never share a name. */
export function forumFileName(id: string): string {
  return `${FORUM_FILE_PREFIX}${id}${FORUM_FILE_SUFFIX}`
}

export function isForumFileName(name: string | undefined): boolean {
  return (
    name !== undefined &&
    name.startsWith(FORUM_FILE_PREFIX) &&
    name.endsWith(FORUM_FILE_SUFFIX) &&
    name.length > FORUM_FILE_PREFIX.length + FORUM_FILE_SUFFIX.length
  )
}

/** The attachment on a Slack message that carries an envelope, if there is one. */
export function forumFileOf<T extends { name?: string; size?: number }>(
  files: readonly T[] | undefined,
): T | undefined {
  return files?.find(
    (file) => isForumFileName(file.name) && (file.size ?? 0) <= MAX_ENVELOPE_FILE_BYTES,
  )
}

/** The envelope a message carries: the file first, the legacy fence second. */
export function envelopeOf(carrier: Pick<ArchivedMessage, 'text' | 'forum'>): Envelope | undefined {
  return decodeAttachment(carrier.forum) ?? decode(carrier.text)
}

/**
 * True when the message claims to carry an envelope, readable or not. A record that
 * names a `forum-*.json` but has no `forum` yet is a message that arrived before the
 * sync could fetch it: counting it as prose would hide it behind a perfectly ordinary
 * sentence, which is how a whole thread went missing.
 */
export function carriesEnvelope(
  carrier: Pick<ArchivedMessage, 'text' | 'forum' | 'files'>,
): boolean {
  return (
    carrier.forum !== undefined ||
    forumFileOf(carrier.files) !== undefined ||
    (carrier.text ?? '').includes('```forum')
  )
}
