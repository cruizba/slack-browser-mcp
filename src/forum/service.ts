/**
 * The workflow both surfaces call: bind, register, post, accept, report, ack,
 * reopen. Every write funnels through `deliver`, so the `confirm` gate, the AI
 * signature, the audit entry and the hourly counter cannot be skipped by adding a
 * new command later.
 *
 * A write that is not confirmed returns the envelope it would post and changes
 * nothing — not even the local decision. That is the same contract as
 * `post_message`, and it is why the preview is a first-class return value instead of
 * a log line. The id and timestamp are minted at post time, so the preview shows
 * the content, not the bytes.
 */

import type { SlackAccount } from '../auth/store.ts'
import { FileSettingsStore } from '../config/settings.ts'
import { resolveNickname, signingFor, type Signing } from '../config/assist.ts'
import { ForumError } from '../errors.ts'
import { toRecord } from '../archive/record.ts'
import { recordSend } from '../slack/audit.ts'
import { signedMessage } from '../slack/assist.ts'
import { SlackClient, type SlackChannel } from '../slack/client.ts'
import { candidates, parseTarget, resolvePeer, targetLabel, type Target } from './addressing.ts'
import type { Binding } from './binding.ts'
import { labelOf, type Identity } from './identity.ts'
import { meLabel, reload, requireIdentity, type ForumContext } from './context.ts'
import { loadViews, type BindingView } from './inbox.ts'
import {
  beaconThrottleMs,
  lastBeacon,
  lastClaim,
  markBeacon,
  markClaim,
  presenceDisabled,
  type PresenceClaim,
} from './presence.ts'
import {
  assertFits,
  render,
  summaryFor,
  type Envelope,
  type ReplyKind,
} from './envelope.ts'
import { newId } from './id.ts'
import { matchesId } from './id.ts'
import { humanStatus, machineStatus, type Peer } from './roster.ts'
import { stateLabel, type ForumTask } from './ledger.ts'
import type { DecidedBy, DecisionName } from './decisions.ts'

export interface Transport {
  account: SlackAccount
  client: SlackClient
  signing: Signing
}

export async function transportFor(account: SlackAccount): Promise<Transport> {
  const settings = await new FileSettingsStore().read()
  return { account, client: new SlackClient(account), signing: signingFor(settings) }
}

/** Resolved per channel, because a task's channel decides which workspace answers. */
export type TransportResolver = (teamId: string) => Promise<Transport>

/**
 * A write decides against the thread, so it has to look at it first: the other side
 * may have accepted, reported or reopened since this machine last synced. The
 * caller supplies the catch-up it already knows how to run; without one the service
 * answers from the archive alone, which is what the tests want.
 */
export interface RefreshHook {
  refresh?: () => Promise<unknown>
  /** Only for tests: the pause before the single retry. */
  retryDelayMs?: number
}

const RETRY_DELAY_MS = 1200

async function freshen(context: ForumContext, hook?: RefreshHook): Promise<ForumContext> {
  if (!hook?.refresh) return context
  await hook.refresh()
  return reload(context)
}

export type WriteResult =
  | {
      kind: 'preview'
      envelope: Envelope
      binding: Binding
      text: string
      target: string
      warning?: string
    }
  | {
      kind: 'posted'
      envelope: Envelope
      binding: Binding
      ts: string
      audit: string
      target: string
      warning?: string
    }

export interface Located {
  view: BindingView
  task: ForumTask
}

function fail(message: string, hint?: string): never {
  throw new ForumError(message, hint)
}

export function locateTask(views: readonly BindingView[], prefix: string): Located {
  const all = views.flatMap((view) => view.ledger.tasks.map((task) => ({ view, task })))
  const matches = all.filter((entry) => matchesId(entry.task.id, prefix))
  if (matches.length === 1) return matches[0]!
  if (matches.length === 0) {
    fail(
      `No task matching "${prefix}".`,
      all.length === 0
        ? 'This forum has no tasks yet. Run `agent-forum peers` to see who is around.'
        : `${all.length} task(s) known; run \`agent-forum inbox --all\` to list them.`,
    )
  }
  fail(
    `"${prefix}" matches ${matches.length} tasks: ${matches
      .slice(0, 5)
      .map((entry) => `${entry.task.id} (${entry.task.title})`)
      .join(', ')}.`,
    'Pass a longer prefix.',
  )
}

function isAddressee(task: ForumTask, me: string): boolean {
  return task.to === '*' || task.to === me
}

function requireOpen(task: ForumTask, me: string): void {
  if (task.state !== 'open') {
    fail(`Task ${task.id} is ${task.state}, not open.`, 'Only an open task can be claimed or declined.')
  }
  if (!isAddressee(task, me)) {
    fail(`Task ${task.id} is addressed to ${task.to}.`, 'An agent can only claim what was sent to it.')
  }
}

function requireAccepted(task: ForumTask, me: string): void {
  if (task.state !== 'accepted') {
    fail(
      `Task ${task.id} is ${task.state}, not accepted.`,
      task.state === 'open' ? 'Claim it first with `accept`.' : 'Nothing more can be reported on it.',
    )
  }
  if (!isAddressee(task, me)) {
    fail(`Task ${task.id} was sent to ${task.to}, not to you.`)
  }
}

function requireRequester(task: ForumTask, me: string): void {
  if (task.from !== me) {
    fail(`Task ${task.id} was posted by ${task.from}.`, 'Only the requester can do that.')
  }
}

/**
 * Renders, signs, posts and records. The only function that talks to Slack, which
 * is what makes "every envelope is audited" a property of the module instead of a
 * habit.
 */
export async function deliver(
  context: ForumContext,
  transport: Transport,
  binding: Binding,
  envelope: Envelope,
  threadTs?: string,
): Promise<{ ts: string; audit: string }> {
  const summary = summaryFor(envelope)
  const text = render(summary, envelope)
  assertFits(text)

  const nickname = resolveNickname(transport.signing, {
    agent: relabel(context),
    user: transport.account.user,
  })
  const signed = nickname && binding.sign ? signedMessage(text, nickname) : { text }
  const posted = await transport.client.postMessage({
    channel: binding.channel,
    text: signed.text,
    ...(signed.blocks ? { blocks: signed.blocks } : {}),
    ...(threadTs ? { thread_ts: threadTs } : {}),
  })
  const audit = await recordSend({
    workspace: transport.account.team_name,
    channel: `#${binding.name}`,
    channel_id: binding.channel,
    ...(threadTs ? { thread_ts: threadTs } : {}),
    message_ts: posted.ts,
    text: signed.text,
  })
  // The poster's own archive has to know immediately: deriving the state from a copy
  // that only catches up on the next sync makes "accept, then report" fail with
  // "still open" in the same session.
  await rememberOwn(context, binding, posted.ts, signed.text, transport.account.user_id, threadTs)
  return { ts: posted.ts, audit }
}

/**
 * Files the message this machine just posted. The cursor is left alone on purpose:
 * moving `newest_ts` here would make the next backfill skip the gap between its own
 * cursor and this message.
 */
async function rememberOwn(
  context: ForumContext,
  binding: Binding,
  ts: string,
  text: string,
  user: string | undefined,
  threadTs: string | undefined,
): Promise<void> {
  try {
    await context.archive.append(binding.workspace, binding.channel, [
      toRecord(
        { id: binding.channel, name: binding.name },
        { ts, text, ...(user ? { user } : {}), ...(threadTs ? { thread_ts: threadTs } : {}) },
        new Date(),
      ),
    ])
  } catch {
    // A failed cache write must never fail a send that already reached Slack.
  }
}

/** The agent name to sign with: the forum name, not the MCP client key. */
function relabel(context: ForumContext): string {
  return context.identity?.name ?? context.identityKey
}

function preview(
  binding: Binding,
  envelope: Envelope,
  target: string,
  warning?: string,
): WriteResult {
  const text = render(summaryFor(envelope), envelope)
  assertFits(text)
  return { kind: 'preview', envelope, binding, text, target, ...(warning ? { warning } : {}) }
}

/**
 * Sending work to a peer that has been quiet for a day is allowed — it will read
 * the task when it comes back — but pretending it is there would be a lie the
 * requester's user pays for.
 */
function targetWarning(
  context: ForumContext,
  target: Target,
  roster: readonly Peer[],
  chosen: Peer | undefined,
): string | undefined {
  const machine = { ttlHours: context.limits.peer_ttl_hours, now: context.now }
  const human = {
    hereSeconds: context.limits.here_seconds,
    ttlHours: context.limits.peer_ttl_hours,
    now: context.now,
  }
  if (chosen) {
    return machineStatus(chosen, machine) === 'out'
      ? `${chosen.from} has not announced anything for ${Math.round((context.now.getTime() - Number(chosen.ts) * 1000) / 3_600_000)}h and reads as out; the task will wait for it`
      : undefined
  }
  if (target.kind !== 'anyone') return undefined
  const matching = candidates(roster, target)
  if (matching.length === 0) return undefined
  const here = matching.filter((peer) => humanStatus(peer, human) === 'here').length
  return here > 0
    ? undefined
    : `${matching.length} peer(s) match, none here: the task is claimed when one of them looks`
}

export interface PostTaskArgs {
  to: string
  title: string
  body: string
  tag?: string
  deadline?: string
  parent?: string
  binding?: string
  confirm: boolean
  by: DecidedBy
}

/**
 * Routing: a named peer picks the binding whose roster knows it; a broadcast needs
 * exactly one binding or an explicit `binding`. Guessing here would send work to
 * the wrong channel and look exactly like a success.
 */
function pickBinding(
  views: readonly BindingView[],
  to: string,
  tag: string | undefined,
  only: string | undefined,
): { view: BindingView; toValue: string; target: string; peer?: Peer } {
  if (only) {
    const view = views.find((entry) => entry.binding.name === only || entry.key === only)
    if (!view) fail(`No bound channel called "${only}".`, '`agent-forum list` shows the bindings.')
    const target = parseTarget(to)
    if (target.kind === 'anyone') {
      return { view, toValue: '*', target: tag ? `*#${tag}` : '*' }
    }
    const peer = resolvePeer(view.roster, target)
    return { view, toValue: peer.from, target: peer.from, peer }
  }

  const target = parseTarget(to)
  if (target.kind === 'anyone') {
    if (views.length !== 1) {
      fail(
        `A broadcast needs one channel to go to, and ${views.length} are bound.`,
        `Pick one with --binding <name>: ${views.map((view) => view.binding.name).join(', ')}.`,
      )
    }
    const view = views[0]!
    return { view, toValue: '*', target: tag ? `*#${tag}` : '*' }
  }

  const knowing = views.filter((view) => candidates(view.roster, target).length > 0)
  if (knowing.length === 0) {
    fail(
      `No peer "${targetLabel(target)}" in any bound channel.`,
      views.length === 0
        ? 'Bind one first: `agent-forum add <channel>` (or `agent-forum add #x`).'
        : `Run \`agent-forum peers\` to see who is around in ${views
            .map((view) => `#${view.binding.name}`)
            .join(', ')}.`,
    )
  }
  if (knowing.length > 1) {
    fail(
      `"${targetLabel(target)}" is known in ${knowing.length} channels: ${knowing
        .map((view) => `#${view.binding.name}`)
        .join(', ')}.`,
      'Pick one with --binding <name>.',
    )
  }
  const view = knowing[0]!
  const peer = resolvePeer(view.roster, target)
  // A bare name is resolved here, so the envelope carries the exact agent. A
  // reader has only the roster to go on, and `to: reviewer` would match nobody:
  // the state machine compares against `name@host`.
  return { view, toValue: peer.from, target: peer.from, peer }
}

export async function postTask(
  context: ForumContext,
  resolveTransport: TransportResolver,
  args: PostTaskArgs,
  hook?: RefreshHook,
): Promise<WriteResult> {
  context = await freshen(context, hook)
  const identity = requireIdentity(context)
  const me = labelOf(identity)
  const views = await loadViews(context)
  if (views.length === 0) {
    fail('No forum channel bound on this machine.', 'Bind one: `agent-forum add <channel>`.')
  }

  const { view, toValue, target, peer } = pickBinding(views, args.to, args.tag, args.binding)
  const warning = targetWarning(context, parseTarget(args.to), view.roster, peer)

  let depth = 0
  if (args.parent) {
    const parent = locateTask([view], args.parent)
    depth = parent.task.depth + 1
  }

  const envelope: Envelope = {
    v: 1,
    kind: 'task',
    id: newId(context.now.getTime()),
    from: me,
    to: toValue,
    sent: context.now.toISOString(),
    title: args.title,
    body: args.body,
    ...(args.tag && toValue === '*' ? { tag: args.tag } : {}),
    ...(args.deadline ? { deadline: args.deadline } : {}),
    ...(args.parent ? { parent: args.parent } : {}),
    ...(depth > 0 ? { depth } : {}),
  }

  if (!args.confirm) return preview(view.binding, envelope, target, warning)

  const transport = await resolveTransport(view.binding.workspace)
  const posted = await deliver(context, transport, view.binding, envelope)
  await context.decisions.record(view.binding.workspace, view.binding.channel, {
    task: envelope.id,
    decision: 'posted',
    by: args.by,
  })
  return {
    kind: 'posted',
    envelope,
    binding: view.binding,
    ts: posted.ts,
    audit: posted.audit,
    target,
    ...(warning ? { warning } : {}),
  }
}

const DECISION_OF: Partial<Record<ReplyKind, DecisionName>> = {
  accept: 'approved',
  decline: 'rejected',
  result: 'reported',
  error: 'reported',
  ack: 'acknowledged',
}

export interface ReplyArgs {
  id: string
  kind: ReplyKind
  body?: string
  ok?: boolean
  confirm: boolean
  by: DecidedBy
}

export async function replyTo(
  context: ForumContext,
  resolveTransport: TransportResolver,
  args: ReplyArgs,
  hook?: RefreshHook,
): Promise<WriteResult> {
  const identity = requireIdentity(context)
  const me = labelOf(identity)

  if ((args.kind === 'progress' || args.kind === 'result' || args.kind === 'error') && !args.body) {
    fail(`A ${args.kind} needs a body: say what happened.`)
  }

  const resolve = async (current: ForumContext) => {
    const views = await loadViews(current)
    const { view, task } = locateTask(views, args.id)
    validate(current, task, args)
    return { view, task }
  }

  context = await freshen(context, hook)
  let located
  try {
    located = await resolve(context)
  } catch (error) {
    // Slack serves a message a second or two after accepting it, so a write can act
    // on a view older than the thread. One retry after a pause turns that race into
    // a successful write; a real state error fails twice and is reported as it is.
    if (!hook?.refresh) throw error
    await new Promise((done) => setTimeout(done, hook.retryDelayMs ?? RETRY_DELAY_MS))
    context = await freshen(context, hook)
    located = await resolve(context)
  }
  const { view, task } = located

  const base = {
    v: 1 as const,
    id: newId(context.now.getTime()),
    from: me,
    sent: context.now.toISOString(),
    task: task.id,
    ...(args.body ? { body: args.body } : {}),
  }
  const envelope: Envelope =
    args.kind === 'result'
      ? { ...base, kind: 'result', body: args.body ?? '', ok: args.ok ?? true }
      : ({ ...base, kind: args.kind } as Envelope)

  if (!args.confirm) return preview(view.binding, envelope, task.title)

  const transport = await resolveTransport(view.binding.workspace)
  const posted = await deliver(context, transport, view.binding, envelope, task.ts)
  const decision = DECISION_OF[args.kind]
  if (decision) {
    await context.decisions.record(view.binding.workspace, view.binding.channel, {
      task: task.id,
      decision,
      by: args.by,
      ...(args.body ? { note: args.body.slice(0, 200) } : {}),
    })
  }
  return {
    kind: 'posted',
    envelope,
    binding: view.binding,
    ts: posted.ts,
    audit: posted.audit,
    target: task.title,
  }
}

/**
 * What each move is allowed to say, before anything is built: claiming needs an
 * open task and the right addressee, reporting needs an accepted one, and closing
 * or reopening needs the requester and a reported result.
 */
function validate(context: ForumContext, task: ForumTask, args: ReplyArgs): void {
  const me = meLabel(context)

  switch (args.kind) {
    case 'accept':
    case 'decline':
      requireOpen(task, me)
      break
    case 'progress':
      if (task.state !== 'accepted') {
        fail(
          `Task ${task.id} is ${stateLabel(task, context.now)}; a note needs an accepted task.`,
          'Claim it first, or wait for whoever was asked to claim it.',
        )
      }
      if (!isAddressee(task, me) && task.from !== me) {
        fail(`Task ${task.id} is between ${task.from} and ${task.to}.`)
      }
      break
    case 'result':
    case 'error':
      requireAccepted(task, me)
      break
    case 'ack':
    case 'reopen':
      if (task.state !== 'done' && task.state !== 'failed') {
        fail(
          `Task ${task.id} is ${stateLabel(task, context.now)}; only a reported task can be ${
            args.kind === 'ack' ? 'acknowledged' : 'reopened'
          }.`,
        )
      }
      requireRequester(task, me)
      break
    case 'cancel':
      if (task.state !== 'open' && task.state !== 'accepted') {
        fail(`Task ${task.id} is ${task.state}; nothing to cancel.`)
      }
      requireRequester(task, me)
      break
  }
}

export interface BindArgs {
  reference: string
  backfill?: boolean
  signed?: boolean
  noPresence?: boolean
}

export async function bindChannel(
  context: ForumContext,
  transport: Transport,
  args: BindArgs,
): Promise<{ binding: Binding; added: boolean }> {
  const channel = await transport.client.resolveChannel(args.reference)
  const mine = await transport.client.listMyChannels()
  if (!mine.some((entry) => entry.id === channel.id)) {
    fail(
      `${describeChannel(channel)} is not a channel this session can read.`,
      'Bind a channel you are a member of; private channels are fine as long as you are in them.',
    )
  }

  let since = '0'
  if (!args.backfill) {
    const page = await transport.client.history({ channel: channel.id, limit: 1 })
    since = page.messages[0]?.ts ?? '0'
  }

  const binding: Binding = {
    workspace: transport.account.team_id,
    channel: channel.id,
    name: channel.name ?? channel.id,
    private: channel.is_private === true,
    since_ts: since,
    added_at: context.now.toISOString(),
    sign: args.signed === true,
    announce: args.noPresence !== true,
  }
  const { added } = await context.forum.bind(binding)
  return { binding, added }
}

function describeChannel(channel: SlackChannel): string {
  return channel.name ? `#${channel.name}` : channel.id
}

export interface RegisterArgs {
  name: string
  host: string
  capabilities: string[]
}

export async function registerIdentity(
  context: ForumContext,
  identity: Identity,
): Promise<{ identity: Identity; announce: boolean }> {
  const registered = { ...identity, registered_at: context.now.toISOString() }
  await context.forum.register(context.identityKey, registered)
  const announced = await reload(context)
  const views = await loadViews(announced)
  const label = labelOf(identity)
  const known = views.some((view) => view.roster.some((peer) => peer.from === label))
  return { identity, announce: !known }
}

export function presenceEnvelope(
  context: ForumContext,
  identity: Identity,
  state: PresenceClaim | 'gone',
  note?: string,
): Envelope {
  return {
    v: 1,
    kind: 'presence',
    id: newId(context.now.getTime()),
    from: labelOf(identity),
    sent: context.now.toISOString(),
    name: identity.name,
    host: identity.host,
    state,
    ...(note ? { note } : {}),
  }
}

/** The one envelope a background poll may post: this machine is up, nothing more. */
export function beaconEnvelope(context: ForumContext, identity: Identity): Envelope {
  return {
    v: 1,
    kind: 'beacon',
    id: newId(context.now.getTime()),
    from: labelOf(identity),
    sent: context.now.toISOString(),
    name: identity.name,
    host: identity.host,
  }
}

/**
 * Says what the person in front of this session is doing. It is the only liveness
 * message a session posts, and it goes out on a change of state, not on a clock:
 * `here` at the start of a session, `away` when the user says so. Anything the
 * session writes afterwards keeps the claim fresh without another message, so a long
 * turn costs one line and not one per hour.
 */
export async function announcePresence(
  context: ForumContext,
  resolveTransport: TransportResolver,
  options: { state?: PresenceClaim; note?: string; force?: boolean } = {},
): Promise<{ posted: number; skipped: number }> {
  if (presenceDisabled()) return { posted: 0, skipped: 0 }
  const identity = context.identity
  if (!identity) return { posted: 0, skipped: 0 }

  const state = options.state ?? 'here'
  if (!options.force) {
    const claim = await lastClaim()
    const fresh =
      claim?.state === state &&
      context.now.getTime() - claim.at < context.limits.here_seconds * 1000
    if (fresh) return { posted: 0, skipped: 0 }
  }

  const views = await loadViews(context)
  const result = { posted: 0, skipped: 0 }
  for (const view of views) {
    if (!view.binding.announce) {
      result.skipped += 1
      continue
    }
    const transport = await resolveTransport(view.binding.workspace)
    await deliver(context, transport, view.binding, presenceEnvelope(context, identity, state, options.note))
    result.posted += 1
  }
  if (result.posted > 0) await markClaim(state, context.now)

  return result
}

/**
 * Says this machine is up, at most once per `presence_seconds`, so a roster entry
 * survives Slack retention without the poller ever speaking for its user. This is
 * the only write a daemon is allowed to make.
 */
export async function announceBeacon(
  context: ForumContext,
  resolveTransport: TransportResolver,
  options: { force?: boolean } = {},
): Promise<{ posted: number; skipped: number }> {
  if (presenceDisabled()) return { posted: 0, skipped: 0 }
  const identity = context.identity
  if (!identity) return { posted: 0, skipped: 0 }

  if (!options.force) {
    const marked = await lastBeacon()
    const fresh =
      marked !== undefined &&
      context.now.getTime() - marked < beaconThrottleMs(context.limits.presence_seconds)
    if (fresh) return { posted: 0, skipped: 0 }
  }

  const views = await loadViews(context)
  const result = { posted: 0, skipped: 0 }
  for (const view of views) {
    if (!view.binding.announce) {
      result.skipped += 1
      continue
    }
    const transport = await resolveTransport(view.binding.workspace)
    await deliver(context, transport, view.binding, beaconEnvelope(context, identity))
    result.posted += 1
  }
  if (result.posted > 0) await markBeacon(context.now)

  return result
}

/**
 * The one door for liveness, with the caller's path in the signature: who is allowed
 * to say what is a property of the call, not of the reviewer's memory. `poll` is the
 * background path and can only produce a beacon; `session` speaks for the person in
 * front of it and never runs from a timer.
 */
export async function announceLiveness(
  context: ForumContext,
  resolveTransport: TransportResolver,
  options: { path: 'poll' | 'session'; state?: PresenceClaim; note?: string; force?: boolean },
): Promise<{ posted: number; skipped: number }> {
  return options.path === 'poll'
    ? announceBeacon(context, resolveTransport, options)
    : announcePresence(context, resolveTransport, options)
}

/**
 * Leaves the forum: says so in every bound channel, then forgets the identity
 * locally and retires the label so its inbox stops filling with work it will never
 * do. Re-registering is all it takes to come back.
 */
export async function unregisterIdentity(
  context: ForumContext,
  resolveTransport: TransportResolver,
  options: { announce?: boolean } = {},
): Promise<{ label: string; posted: number }> {
  const identity = requireIdentity(context)
  const label = labelOf(identity)
  let posted = 0

  if (options.announce !== false) {
    const views = await loadViews(context)
    for (const view of views) {
      const transport = await resolveTransport(view.binding.workspace)
      await deliver(context, transport, view.binding, presenceEnvelope(context, identity, 'gone'))
      posted += 1
    }
  }

  await context.forum.unregister(context.identityKey, label)
  return { label, posted }
}

export function announceEnvelope(context: ForumContext, identity: Identity): Envelope {
  return {
    v: 1,
    kind: 'register',
    id: newId(context.now.getTime()),
    from: labelOf(identity),
    sent: context.now.toISOString(),
    name: identity.name,
    host: identity.host,
    capabilities: identity.capabilities,
    version: '0.1.0',
  }
}

export async function announce(
  context: ForumContext,
  resolveTransport: TransportResolver,
  identity: Identity,
): Promise<{ posted: number; skipped: number }> {
  const views = await loadViews(context)
  const envelope = announceEnvelope(context, identity)
  let posted = 0
  let skipped = 0
  for (const view of views) {
    if (view.roster.some((peer) => peer.from === envelope.from)) {
      skipped += 1
      continue
    }
    const transport = await resolveTransport(view.binding.workspace)
    await deliver(context, transport, view.binding, envelope)
    posted += 1
  }
  return { posted, skipped }
}
