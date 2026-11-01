/**
 * Where an envelope lives on a Slack message: its own file, `forum-<id>.md`, and the
 * message `text` carrying nothing but the summary a person reads.
 *
 * Everything that reads a message goes through `envelopeOf` so the shapes never have
 * to be handled twice: the current one (a file, kept on the archived record as
 * `forum`), the `forum-<id>.json` file written up to v0.3.2 and the fenced block
 * written up to v0.2.1, both still in the channels and still the only thing older
 * messages have.
 *
 * The file name is the lookup key. Slack's `files.info` and `conversations.history`
 * both give a name, so a reader needs no manifest, no marker and no extra state: a
 * `forum-*` next to a message is the envelope, the suffix says which shape it is, and
 * anything else is an attachment somebody posted.
 */

import type { ArchivedMessage } from '../archive/store.ts'
import {
  decodeFence,
  decodeJson,
  decodeMarkdown,
  MAX_BODY_BYTES,
  type Envelope,
} from './envelope.ts'

export const FORUM_FILE_PREFIX = 'forum-'
export const FORUM_FILE_SUFFIX = '.md'
/** What the envelope travelled in up to v0.3.2: still read, never written. */
export const LEGACY_FILE_SUFFIX = '.json'

/**
 * An envelope file bigger than this is not one of ours: the schema refuses the body
 * long before, so a huge `forum-*` is something a person attached and the sync
 * should not drag into the archive.
 */
export const MAX_ENVELOPE_FILE_BYTES = MAX_BODY_BYTES + 64 * 1024

/** What the file is called: the envelope id, so two messages never share a name. */
export function forumFileName(id: string): string {
  return `${FORUM_FILE_PREFIX}${id}${FORUM_FILE_SUFFIX}`
}

export function isForumFileName(name: string | undefined): boolean {
  if (name === undefined || !name.startsWith(FORUM_FILE_PREFIX)) return false
  const suffix = name.endsWith(FORUM_FILE_SUFFIX)
    ? FORUM_FILE_SUFFIX
    : name.endsWith(LEGACY_FILE_SUFFIX)
      ? LEGACY_FILE_SUFFIX
      : undefined
  return suffix !== undefined && name.length > FORUM_FILE_PREFIX.length + suffix.length
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
export function envelopeOf(
  carrier: Pick<ArchivedMessage, 'text' | 'forum' | 'files'>,
): Envelope | undefined {
  if (carrier.forum !== undefined) {
    return decodeCarried(carrier.forum, forumFileOf(carrier.files)?.name)
  }
  return decodeFence(carrier.text)
}

function decodeCarried(text: string, name: string | undefined): Envelope | undefined {
  if (name?.endsWith(FORUM_FILE_SUFFIX)) return decodeMarkdown(text)
  if (name?.endsWith(LEGACY_FILE_SUFFIX)) return decodeJson(text)
  // A record synced before the file entry was kept has the text and no name, so the
  // first line decides. A JSON envelope always opens with a brace.
  return text.trimStart().startsWith('---') ? decodeMarkdown(text) : decodeJson(text)
}

/**
 * True when the message claims to carry an envelope, readable or not. A record that
 * names a `forum-*` but has no `forum` yet is a message that arrived before the sync
 * could fetch it: counting it as prose would hide it behind a perfectly ordinary
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
