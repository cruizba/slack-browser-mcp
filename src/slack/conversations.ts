/**
 * Conversations named the way a person names them: `#random`, `private:leads`,
 * `dm:Ana Ruiz`, `group:Ana, Michel`.
 *
 * Slack gives a channel a name and a DM none; a DM is identified by the person on
 * the other side. The archive keeps whatever Slack stored (for a DM, the peer's
 * user id), so labels are derived here at render time through the directory and
 * a refreshed directory improves old output instead of freezing a stale name.
 */

import type { SlackChannel } from './client.ts'
import {
  conversationKind,
  directoryChannel,
  directoryUser,
  directoryUserByHandle,
  directoryUserByReference,
  type ConversationKind,
  type Directory,
  type DirectoryChannel,
} from './directory.ts'

export interface ConversationRef {
  id: string
  name?: string | undefined
  /** The person on the other side of a DM, as a user id. */
  peer?: string | undefined
  kind?: ConversationKind | undefined
}

export interface ConversationFilter {
  /** Conversation id, when the reference resolves to one. */
  channel?: string
  /** Stored `channel_name`: a channel's name, or a DM's peer user id. */
  channelName?: string
}

const PREFIX: Record<ConversationKind, string> = {
  channel: '#',
  private: 'private:',
  dm: 'dm:',
  mpim: 'group:',
}

const USER_ID = /^[UW][A-Z0-9]{5,}$/
const CONVERSATION_ID = /^[CGD][A-Z0-9]{5,}$/
const GROUP_DM_NAME = /^mpdm-(.+?)(?:-\d+)?$/

function groupDmHandles(name: string): string[] {
  const match = GROUP_DM_NAME.exec(name)
  return match ? match[1]!.split('--').filter(Boolean) : []
}

function inferKind(ref: ConversationRef, entry: DirectoryChannel | undefined): ConversationKind {
  if (ref.kind) return ref.kind
  if (entry) return entry.kind
  // The archive of a DM records the peer's user id as the channel name, because
  // that is all Slack has to say about who the conversation is with.
  if (ref.peer || (ref.name && USER_ID.test(ref.name)) || ref.id.startsWith('D')) return 'dm'
  if (ref.name && GROUP_DM_NAME.test(ref.name)) return 'mpim'
  return 'channel'
}

function directName(
  ref: ConversationRef,
  entry: DirectoryChannel | undefined,
  directory: Directory | undefined,
): string {
  const peer =
    ref.peer ?? entry?.user ?? (ref.name && USER_ID.test(ref.name) ? ref.name : undefined)
  if (peer) return directoryUser(directory, peer) ?? peer
  return entry?.name ?? ref.name ?? ref.id
}

function groupName(
  ref: ConversationRef,
  entry: DirectoryChannel | undefined,
  directory: Directory | undefined,
): string {
  const name = ref.name ?? entry?.name
  const members = name ? groupDmHandles(name) : []
  const labels = members.map((handle) => {
    const user = directoryUserByHandle(directory, handle)
    return (user && directoryUser(directory, user.id)) ?? handle
  })
  if (labels.length > 0) return labels.join(', ')
  return name ?? ref.id
}

function conversationName(
  ref: ConversationRef,
  kind: ConversationKind,
  directory: Directory | undefined,
): string {
  const entry = directoryChannel(directory, ref.id)
  if (kind === 'dm') return directName(ref, entry, directory)
  if (kind === 'mpim') return groupName(ref, entry, directory)
  return entry?.name ?? ref.name ?? ref.id
}

/** The one-line label an agent reads in place of an id. */
export function conversationLabel(ref: ConversationRef, directory?: Directory): string {
  const entry = directoryChannel(directory, ref.id)
  const kind = inferKind(ref, entry)
  return `${PREFIX[kind]}${conversationName(ref, kind, directory)}`
}

export function slackChannelLabel(channel: SlackChannel, directory?: Directory): string {
  return conversationLabel(
    { id: channel.id, name: channel.name, peer: channel.user, kind: conversationKind(channel) },
    directory,
  )
}

/**
 * Turns what a person typed into what the archive index can filter on. A DM is
 * stored under the peer's user id, so naming the person resolves to that id.
 *
 * Several directories are accepted for a search that spans workspaces: a person
 * exists in one of them, and ids are unique across workspaces.
 */
export function conversationFilter(
  reference: string,
  directory?: Directory | Array<Directory | undefined>,
): ConversationFilter {
  const directories = Array.isArray(directory) ? directory : [directory]
  const raw = reference.trim()
  if (CONVERSATION_ID.test(raw)) return { channel: raw }

  const addressingPerson = /^(dm:|@)/.test(raw)
  const name = raw.replace(/^#/, '').replace(/^(dm:|group:|private:|@)/, '')
  if (!name) return {}

  const byName = directories
    .flatMap((entry) => entry?.channels ?? [])
    .find((channel) => channel.name !== undefined && channel.name.toLowerCase() === name.toLowerCase())
  if (byName && !addressingPerson) return { channel: byName.id }

  for (const entry of directories) {
    const person = directoryUserByReference(entry, name)
    if (!person) continue
    const direct = entry?.channels.find(
      (channel) => channel.kind === 'dm' && channel.user === person.id,
    )
    return direct ? { channel: direct.id } : { channelName: person.id }
  }

  return { channelName: name }
}
