/**
 * Taking back and correcting what this machine said.
 *
 * Slack cannot swap the attachment of a message, and an envelope is an upload: the
 * file is the message. So editing a body is a new version posted in the same place
 * plus the old one deleted, which is why it is refused once the thread has answers —
 * those hang off the ts of the message that would disappear.
 *
 * Only this account's own postings can be touched: `chat.delete` refuses anybody
 * else's, so checking first turns a cryptic API error into a sentence. Every
 * removal leaves an audit line with a preview of what it destroyed, because after
 * the call there is nothing left in the channel to read.
 */

import type { ArchivedMessage } from '../archive/store.ts'
import { ForumError } from '../errors.ts'
import { recordRemoval } from '../slack/audit.ts'
import { envelopeOf } from './carrier.ts'
import type { ForumContext } from './context.ts'
import { loadViews, type BindingView } from './inbox.ts'
import type { ForumTask } from './ledger.ts'
import { deliver, locateTask, type TransportResolver } from './service.ts'

export interface RemovalResult {
  kind: 'preview' | 'done'
  text: string
}

export interface RemoveArgs {
  ref: string
  force: boolean
  confirm: boolean
}

export interface EditArgs {
  id: string
  body: string
  confirm: boolean
}

interface Located {
  view: BindingView
  ts: string
  task?: ForumTask
  message?: ArchivedMessage
}

function fail(message: string, hint?: string): never {
  throw new ForumError(message, hint)
}

const TS_SHAPE = /^\d{10,}\.\d{4,}$/

function plural(count: number, one: string, many: string): string {
  return `${count} ${count === 1 ? one : many}`
}

async function locate(context: ForumContext, ref: string): Promise<Located> {
  const views = await loadViews(context)
  if (views.length === 0) {
    fail('No forum channel bound on this machine.', 'Bind one: `agent-forum add <channel>`.')
  }
  const value = ref.trim()
  if (value === '') fail('delete needs a task id or a message ts.')

  if (TS_SHAPE.test(value)) {
    for (const view of views) {
      const records = await context.archive.read(view.binding.workspace, view.binding.channel)
      const message = records.find((entry) => entry.ts === value)
      if (!message) continue
      if (message.deleted_at) {
        fail(`Message ${value} is already marked deleted.`, 'Nothing left to do.')
      }
      const task = view.ledger.tasks.find((entry) => entry.ts === value)
      return { view, ts: value, message, ...(task ? { task } : {}) }
    }
    fail(
      `No message with ts ${value} in the bound channels.`,
      '`agent-forum task <id>` prints the ts of a task, and `read <channel>` lists the rest.',
    )
  }

  const { view, task } = locateTask(views, value)
  const records = await context.archive.read(view.binding.workspace, view.binding.channel)
  const message = records.find((entry) => entry.ts === task.ts)
  return { view, ts: task.ts, task, ...(message ? { message } : {}) }
}

async function markDeleted(context: ForumContext, view: BindingView, ts: string): Promise<void> {
  const now = new Date().toISOString()
  await context.archive
    .append(view.binding.workspace, view.binding.channel, [
      {
        ts,
        channel: view.binding.channel,
        channel_name: view.binding.name,
        deleted_at: now,
        synced_at: now,
      },
    ])
    // The message is already gone from Slack; a failed cache write must not turn a
    // finished deletion into an error the caller retries.
    .catch(() => undefined)
}

function excerptOf(located: Located): string {
  return located.task?.body ?? located.message?.text ?? ''
}

/** What this message is, for a sentence a person reads before approving. */
function describe(located: Located, binding: BindingView['binding']): string {
  const where = `${binding.name} (${binding.channel}) · ts ${located.ts}`
  return located.task
    ? `task ${located.task.id} "${located.task.title}" in ${where}`
    : `message in ${where}`
}

export async function deletePosting(
  context: ForumContext,
  resolveTransport: TransportResolver,
  args: RemoveArgs,
): Promise<RemovalResult> {
  const located = await locate(context, args.ref)
  const { view } = located
  const transport = await resolveTransport(view.binding.workspace)

  const author = located.message?.user ?? located.task?.owner
  if (author !== undefined && author !== transport.account.user_id) {
    fail(
      'That message was posted by another account, and Slack only lets its author delete it.',
      'Delete it from that account, or ask whoever posted it.',
    )
  }

  const replies = located.task?.events.length ?? Number(located.message?.reply_count ?? 0)
  if (replies > 0 && !args.force) {
    fail(
      `That message is the root of a thread with ${plural(replies, 'reply', 'replies')}.`,
      'Deleting it leaves them answering nothing. Pass --force if that is what you want, ' +
        'or cancel the task so the other side sees why it stopped.',
    )
  }

  const target = describe(located, view.binding)
  const orphan = replies > 0 ? `, leaving ${plural(replies, 'reply', 'replies')} without a parent` : ''
  if (!args.confirm) {
    return {
      kind: 'preview',
      text:
        `dry run — would delete ${target}${orphan}\n\n` +
        `${excerptOf(located)}\n\nnothing deleted; add --post to commit`,
    }
  }

  await transport.client.deleteMessage({ channel: view.binding.channel, ts: located.ts })
  const audit = await recordRemoval({
    action: 'delete',
    workspace: transport.account.team_name,
    channel: `#${view.binding.name}`,
    channel_id: view.binding.channel,
    message_ts: located.ts,
    ...(located.message?.thread_ts ? { thread_ts: located.message.thread_ts } : {}),
    text: excerptOf(located),
  })
  await markDeleted(context, view, located.ts)
  return { kind: 'done', text: `deleted ${target}${orphan}; audit: ${audit}` }
}

export async function editTaskBody(
  context: ForumContext,
  resolveTransport: TransportResolver,
  args: EditArgs,
): Promise<RemovalResult> {
  const views = await loadViews(context)
  if (views.length === 0) {
    fail('No forum channel bound on this machine.', 'Bind one: `agent-forum add <channel>`.')
  }
  const { view, task } = locateTask(views, args.id)
  const records = await context.archive.read(view.binding.workspace, view.binding.channel)
  const message = records.find((entry) => entry.ts === task.ts)
  if (!message || message.deleted_at) {
    fail(
      `This machine has no live copy of the message behind ${task.id}.`,
      'Sync the channel first: `agent-forum poll`.',
    )
  }
  const envelope = envelopeOf(message)
  if (!envelope || envelope.kind !== 'task') {
    fail(`${task.id} is not a task this machine can rewrite.`)
  }

  const transport = await resolveTransport(view.binding.workspace)
  if (task.owner !== undefined && task.owner !== transport.account.user_id) {
    fail(
      'That task was posted by another account, and Slack only lets its author delete the old message.',
      'Ask whoever posted it to correct it in their own thread.',
    )
  }
  if (task.events.length > 0) {
    fail(
      `Task ${task.id} already has ${plural(task.events.length, 'reply', 'replies')} in its thread.`,
      'Replacing the envelope would leave that thread hanging off a message that no longer ' +
        'exists. Correct it with `reopen`, or post a `progress` note.',
    )
  }

  const where = `${view.binding.name} (${view.binding.channel})`
  if (!args.confirm) {
    return {
      kind: 'preview',
      text:
        `dry run — would replace the body of ${task.id} in ${where} and delete the previous ` +
        `message (ts ${task.ts})\n\n` +
        `now:\n${task.body}\n\nwould be:\n${args.body}\n\n` +
        'nothing sent; add --post to commit',
    }
  }

  // New version first: a failure between the two calls leaves two live copies, which
  // the ledger resolves by newest, instead of none.
  const posted = await deliver(context, transport, view.binding, { ...envelope, body: args.body })
  await transport.client.deleteMessage({ channel: view.binding.channel, ts: task.ts })
  const audit = await recordRemoval({
    action: 'edit',
    workspace: transport.account.team_name,
    channel: `#${view.binding.name}`,
    channel_id: view.binding.channel,
    message_ts: task.ts,
    text: task.body,
  })
  await markDeleted(context, view, task.ts)
  return {
    kind: 'done',
    text:
      `replaced the body of ${task.id} in ${where}` +
      (posted.ts ? ` at ts ${posted.ts}` : ' (Slack reported no ts; it is posted)') +
      `; audit: ${audit}`,
  }
}
