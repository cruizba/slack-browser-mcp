#!/usr/bin/env node
/**
 * Human entry point: log in once, inspect the session, read a channel from the
 * shell. `serve` runs the MCP server on stdio.
 */

import { login } from './auth/login.ts'
import { autoOff, autoOn, autoStatus } from './forum/auto.ts'
import { openForum, reload, requireIdentity, type ForumContext } from './forum/context.ts'
import { defaultHost, normalizeCapabilities, normalizeName } from './forum/identity.ts'
import { inboxEntries, loadViews, newestTs, type InboxEntry } from './forum/inbox.ts'
import { locateTask, postTask, replyTo, transportFor, announce, announcePresence, registerIdentity, unregisterIdentity, bindChannel, type Transport, type WriteResult } from './forum/service.ts'
import { channelLabel as forumChannelLabel, renderInbox, renderPeers, renderTask } from './forum/render.ts'
import { stateLabel } from './forum/ledger.ts'
import { catchUp, waitForWork, type CatchUpReport } from './forum/watch.ts'
import { Lock } from './archive/lock.ts'
import {
  FileCredentialStore,
  workspaceLabels,
  type AccountSummary,
  type DefaultSource,
  type DefaultWorkspace,
} from './auth/store.ts'
import { ArchiveIndex, rebuildIndex } from './archive/index.ts'
import { ensureArchiveSchema } from './archive/schema.ts'
import { DEFAULT_BACKFILL_DAYS, syncAccount } from './archive/sync.ts'
import { ASSISTED_NICKNAME_ENV, resolveNickname, signingFor } from './config/assist.ts'
import { FileSettingsStore, ensureDefaultWorkspace } from './config/settings.ts'
import { loginLockPath } from './config/paths.ts'
import { AuthError, ForumError, SchemaError, StoreError } from './errors.ts'
import { main as serve } from './mcp/server.ts'
import { recordSend } from './slack/audit.ts'
import { ASSISTED_HEADER, describeSigning, signedMessage } from './slack/assist.ts'
import { SlackClient } from './slack/client.ts'
import { channelLabel, renderMessages } from './slack/format.ts'
import { conversationFilter, conversationLabel } from './slack/conversations.ts'
import {
  directoryScope,
  directoryUser,
  directoryUserByReference,
  loadDirectory,
  memoizedDirectory,
  DirectoryStore,
  type Directory,
} from './slack/directory.ts'
import { resolveReferences } from './slack/references.ts'

const HELP = `slack-browser — Slack read access from a browser session

Usage:
  slack-browser login [--timeout <seconds>] [--headless]
  slack-browser accounts
  slack-browser workspace [list]
  slack-browser workspace use <name|domain|team-id>
  slack-browser workspace clear
  slack-browser assist [<nickname>] [--clear]
  slack-browser status [--workspace <ref>]
  slack-browser read <channel> [--limit <n>] [--oldest <ts>] [--latest <ts>] [--workspace <ref>]
  slack-browser thread <channel> <thread_ts> [--workspace <ref>]
  slack-browser send <channel> <message> [--thread <ts>] [--send] [--no-assist] [--workspace <ref>]
  slack-browser sync [--full] [--since <days>] [--deadline <seconds>] [--quiet] [--workspace <ref>]
  slack-browser archive search <query> [--channel <name>] [--workspace <ref>] [--limit <n>] [--deleted]
  slack-browser archive stats [--workspace <ref>]
  slack-browser archive reindex
  slack-browser archive migrate
  slack-browser agent-forum [list]                 # bindings, identity, peers pending
  slack-browser agent-forum add <channel> [--backfill] [--signed] [--no-presence] [--workspace <ref>]
  slack-browser agent-forum remove <channel>
  slack-browser agent-forum register <name> [--host <h>] [--capabilities a,b] [--no-post]
  slack-browser agent-forum unregister [<name>] [--no-post]
  slack-browser agent-forum peers
  slack-browser agent-forum inbox [--all] [--json] [--offline]
  slack-browser agent-forum task <id>
  slack-browser agent-forum post <to> <title> --body <text> [--tag <cap>] [--parent <id>] [--binding <name>] [--post]
  slack-browser agent-forum accept|decline|progress|result|ack|reopen|cancel <id> [--body <text>] [--post]
  slack-browser agent-forum wait [--timeout <s>] [--follow] [--json] [--offline]
  slack-browser agent-forum poll [--quiet] [--deadline <s>]
  slack-browser agent-forum auto [on|off|status]
  slack-browser agent-forum forget <name|name@host>
  slack-browser serve

The browser opens once, during login. Everything else talks to the Slack Web API
with the captured session, stored at ~/.config/slack-browser-mcp/accounts with
mode 0600.

Without --workspace, a command uses SLACK_BROWSER_MCP_TEAM, then the default
saved by "workspace use", then the only stored session. "sync" without
--workspace covers every stored workspace.

With nothing configured, every sent message is still marked: the header reads
"*Message Sent by AI Agent '<client>' of <account>*", naming the MCP client and the
account it goes out from. Set your own with "assist <nickname>"; --no-assist posts
the text alone, and ${ASSISTED_NICKNAME_ENV} wins over config.json.

"agent-forum" (alias "forum") is the channel where agents hand each other work:
bind a channel once, register a name, then post tasks to a peer by name. Every
write is a dry run unless --post, and every post is audited.`

interface ParsedArgs {
  positional: string[]
  flags: Map<string, string | boolean>
}

function parseArgs(argv: string[]): ParsedArgs {
  const positional: string[] = []
  const flags = new Map<string, string | boolean>()

  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index]!
    if (!arg.startsWith('--')) {
      positional.push(arg)
      continue
    }
    const withoutPrefix = arg.slice(2)
    const equals = withoutPrefix.indexOf('=')
    if (equals > 0) {
      flags.set(withoutPrefix.slice(0, equals), withoutPrefix.slice(equals + 1))
      continue
    }
    const next = argv[index + 1]
    if (next !== undefined && !next.startsWith('--')) {
      flags.set(withoutPrefix, next)
      index += 1
    } else {
      flags.set(withoutPrefix, true)
    }
  }

  return { positional, flags }
}

function stringFlag(args: ParsedArgs, name: string): string | undefined {
  const value = args.flags.get(name)
  return typeof value === 'string' ? value : undefined
}

function numberFlag(args: ParsedArgs, name: string, fallback: number): number {
  const raw = stringFlag(args, name)
  if (raw === undefined) return fallback
  const parsed = Number(raw)
  if (!Number.isFinite(parsed) || parsed <= 0) {
    throw new Error(`--${name} expects a positive number, got "${raw}"`)
  }
  return parsed
}

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

const DEFAULT_SOURCE: Record<DefaultSource, string> = {
  environment: 'SLACK_BROWSER_MCP_TEAM',
  config: 'config.json',
  only: 'the only stored session',
}

async function defaultWorkspace(store: FileCredentialStore): Promise<DefaultWorkspace | null> {
  try {
    return await store.default()
  } catch (error) {
    console.error(`default workspace is broken: ${messageOf(error)}`)
    return null
  }
}

function describeDefault(
  fallback: DefaultWorkspace | null,
  accounts: AccountSummary[],
): string {
  if (!fallback) {
    return (
      'no default workspace: pass --workspace, or run ' +
      '`slack-browser workspace use <name|domain|team-id>`'
    )
  }
  const name = accounts.find((account) => account.team_id === fallback.team_id)?.team_name
  return `default workspace: ${name ?? fallback.team_id} (${fallback.team_id}) via ${
    DEFAULT_SOURCE[fallback.source]
  }`
}

async function commandLogin(args: ParsedArgs): Promise<number> {
  const store = new FileCredentialStore()
  const lock = new Lock(loginLockPath())
  if (!(await lock.acquire())) {
    const owner = await lock.holder()
    console.error(
      `another sign-in is already running${owner ? ` (pid ${owner.pid}, since ${owner.started_at})` : ''}; ` +
        'finish it in the browser window that is open',
    )
    return 1
  }

  try {
    const timeoutMs = numberFlag(args, 'timeout', 300) * 1000
    const result = await login(store, {
      timeoutMs,
      headless: args.flags.get('headless') === true,
      onProgress: (message) => console.error(message),
    })

    for (const account of result.saved) {
      console.log(`saved  ${account.team_name} (${account.team_id}) as ${account.user ?? 'unknown user'}`)
    }
    for (const rejected of result.rejected) {
      console.error(`failed ${rejected.team_name} (${rejected.team_id}): ${rejected.reason}`)
    }
    if (result.saved.length === 0) {
      console.error('Nothing was stored. If you have several workspaces, sign in to one at a time.')
      return 1
    }
    const first = result.saved[0]!
    if (await ensureDefaultWorkspace(new FileSettingsStore(), first.team_id)) {
      console.log(`default workspace: ${first.team_name} (${first.team_id})`)
    }
    return result.rejected.length > 0 ? 1 : 0
  } finally {
    await lock.release()
  }
}

async function commandAccounts(): Promise<number> {
  const store = new FileCredentialStore()
  const accounts = await store.list()
  if (accounts.length === 0) {
    console.log('No stored session. Run `slack-browser login`.')
    return 0
  }
  const fallback = await defaultWorkspace(store)
  for (const account of accounts) {
    const lastOk = account.last_ok_at ? `last ok ${account.last_ok_at}` : 'never validated'
    const marker = fallback?.team_id === account.team_id ? '* ' : '  '
    console.log(
      `${marker}${account.team_id}  ${account.team_name}  ${account.user ?? '-'}  ${account.team_domain ?? '-'}  ${lastOk}`,
    )
  }
  console.log(describeDefault(fallback, accounts))
  return 0
}

async function commandWorkspace(args: ParsedArgs): Promise<number> {
  const subcommand = args.positional[0] ?? 'list'
  const store = new FileCredentialStore()

  switch (subcommand) {
    case 'list':
      return commandAccounts()
    case 'use': {
      const reference = args.positional[1]
      if (!reference) throw new Error('workspace use needs a name, domain or team id')
      const account = await store.resolve(reference)
      await new FileSettingsStore().setDefaultWorkspace(account.team_id)
      console.log(`default workspace: ${account.team_name} (${account.team_id})`)
      return 0
    }
    case 'clear': {
      await new FileSettingsStore().setDefaultWorkspace(null)
      const accounts = await store.list()
      console.log(
        accounts.length > 1
          ? 'default workspace cleared; pass --workspace on every call that needs one'
          : 'default workspace cleared; the only stored session is still used implicitly',
      )
      return 0
    }
    default:
      throw new Error(`unknown workspace subcommand: ${subcommand}`)
  }
}

async function commandAssist(args: ParsedArgs): Promise<number> {
  const store = new FileSettingsStore()

  if (args.flags.get('clear') === true) {
    if (args.positional.length > 0) throw new Error('assist --clear takes no nickname')
    await store.setAssistedNickname(null)
    console.log(`cleared; ${describeSigning(signingFor(await store.read()))}`)
    return 0
  }

  const nickname = args.positional.join(' ').trim()
  if (nickname) {
    await store.setAssistedNickname(nickname)
    console.log(`assisted nickname: ${nickname}`)
    console.log(`every sent message now opens with *${ASSISTED_HEADER} ${nickname}* between two rule lines`)
    return 0
  }

  const fromEnv = process.env[ASSISTED_NICKNAME_ENV] !== undefined
  const source = fromEnv ? ` (from ${ASSISTED_NICKNAME_ENV})` : ''
  console.log(`${describeSigning(signingFor(await store.read()))}${source}`)
  return 0
}

async function commandStatus(args: ParsedArgs): Promise<number> {
  const store = new FileCredentialStore()
  const reference = stringFlag(args, 'workspace')
  const accounts = reference
    ? [await store.get((await store.resolve(reference)).team_id)]
    : await Promise.all((await store.list()).map((summary) => store.get(summary.team_id)))

  if (accounts.length === 0) {
    console.log('No stored session. Run `slack-browser login`.')
    return 0
  }

  let failures = 0
  for (const account of accounts) {
    const client = new SlackClient(account)
    try {
      const info = await client.authTest()
      await store.markValidated(account.team_id)
      console.log(`ok    ${info.team} (${info.team_id}) as ${info.user}`)
    } catch (error) {
      failures += 1
      console.error(
        `FAIL  ${account.team_name} (${account.team_id}): ${error instanceof Error ? error.message : String(error)}`,
      )
    }
  }
  return failures > 0 ? 2 : 0
}

async function commandRead(args: ParsedArgs): Promise<number> {
  const channelReference = args.positional[0]
  if (!channelReference) throw new Error('read needs a channel name or id')

  const store = new FileCredentialStore()
  const account = await store.resolve(stringFlag(args, 'workspace'))
  const client = new SlackClient(account)
  const directory = memoizedDirectory(() => loadDirectory({ client, teamId: account.team_id }))
  const channel = await client.resolveChannel(channelReference, { person: directory.person })
  const page = await client.history({
    channel: channel.id,
    limit: numberFlag(args, 'limit', 20),
    ...(stringFlag(args, 'oldest') ? { oldest: stringFlag(args, 'oldest')! } : {}),
    ...(stringFlag(args, 'latest') ? { latest: stringFlag(args, 'latest')! } : {}),
  })

  const chronological = [...page.messages].reverse()
  const dir = await directory.get()
  const names = await client.namesFor(chronological, (id) => directoryUser(dir, id))
  console.log(
    `${account.team_name} · ${channelLabel(channel, dir)} (${channel.id}) — ${chronological.length} messages, oldest first`,
  )
  console.log(renderMessages(chronological, { names, scope: directoryScope(dir) }))
  if (page.has_more) {
    console.error(`more history exists; use --oldest=${chronological[0]?.ts}`)
  }
  return 0
}

async function commandThread(args: ParsedArgs): Promise<number> {
  const channelReference = args.positional[0]
  const threadTs = args.positional[1]
  if (!channelReference || !threadTs) throw new Error('thread needs a channel and a thread_ts')

  const store = new FileCredentialStore()
  const account = await store.resolve(stringFlag(args, 'workspace'))
  const client = new SlackClient(account)
  const directory = memoizedDirectory(() => loadDirectory({ client, teamId: account.team_id }))
  const channel = await client.resolveChannel(channelReference, { person: directory.person })
  const messages = await client.replies({ channel: channel.id, thread_ts: threadTs })
  const dir = await directory.get()
  const names = await client.namesFor(messages, (id) => directoryUser(dir, id))
  console.log(
    `${account.team_name} · thread in ${channelLabel(channel, dir)} (${channel.id}) — ${messages.length} messages`,
  )
  console.log(renderMessages(messages, { names, scope: directoryScope(dir) }))
  return 0
}

async function commandSend(args: ParsedArgs): Promise<number> {
  const channelReference = args.positional[0]
  const text = args.positional.slice(1).join(' ')
  if (!channelReference || !text) throw new Error('send needs a channel and a message')

  const store = new FileCredentialStore()
  const account = await store.resolve(stringFlag(args, 'workspace'))
  const client = new SlackClient(account)
  // A send names the target from the directory on disk; it never refreshes it.
  const directory = await new DirectoryStore().read(account.team_id)
  const channel = await client.resolveChannel(channelReference, {
    person: (name) => directoryUserByReference(directory, name)?.id,
  })
  const label = channelLabel(channel, directory)
  const threadTs = stringFlag(args, 'thread')
  const target = `${account.team_name} · ${label} (${channel.id})`
  const nickname = resolveNickname(signingFor(await new FileSettingsStore().read()), {
    user: account.user,
  })
  const signed =
    nickname && args.flags.get('no-assist') !== true ? signedMessage(text, nickname) : { text }

  if (args.flags.get('send') !== true) {
    console.log(`dry run — would post to ${target}${threadTs ? ` in thread ${threadTs}` : ''}:`)
    console.log(signed.text)
    console.error('nothing sent; add --send to commit')
    return 0
  }

  const posted = await client.postMessage({
    channel: channel.id,
    text: signed.text,
    ...(signed.blocks ? { blocks: signed.blocks } : {}),
    ...(threadTs ? { thread_ts: threadTs } : {}),
  })
  const audit = await recordSend({
    workspace: account.team_name,
    channel: label,
    channel_id: channel.id,
    ...(threadTs ? { thread_ts: threadTs } : {}),
    message_ts: posted.ts,
    text: signed.text,
  })
  console.log(`sent ts ${posted.ts} to ${target}; audit: ${audit}`)
  return 0
}

async function commandSync(args: ParsedArgs): Promise<number> {
  const store = new FileCredentialStore()
  const reference = stringFlag(args, 'workspace')
  const quiet = args.flags.get('quiet') === true

  const accounts = reference
    ? [await store.resolve(reference)]
    : await Promise.all((await store.list()).map((summary) => store.get(summary.team_id)))
  if (accounts.length === 0) {
    // Not an error: the boot sync runs before anyone signs in, and a red unit on
    // every session start says nothing useful.
    console.error('No Slack session stored yet. Run `slack-browser login`.')
    return 0
  }

  // Before any workspace touches the archive: an old layout must be upgraded
  // first, and this also holds the sync lock for the rewrite.
  const schema = await ensureArchiveSchema(
    quiet ? {} : { log: (line: string) => console.error(line) },
  )
  if (schema.locked) {
    console.error('another sync is running; archive schema left untouched')
  } else if (schema.applied.length > 0) {
    console.error(`archive schema migrated ${schema.from} -> ${schema.to}`)
  }

  // One budget for the whole run: the timer asks for a deadline, not one per workspace.
  const deadline = Date.now() + numberFlag(args, 'deadline', 600) * 1000
  const options = (remainingMs: number) => ({
    full: args.flags.get('full') === true,
    deadlineMs: remainingMs,
    ...(stringFlag(args, 'since') ? { backfillDays: numberFlag(args, 'since', DEFAULT_BACKFILL_DAYS) } : {}),
    ...(stringFlag(args, 'reconcile-days')
      ? { reconcileDays: numberFlag(args, 'reconcile-days', 7) }
      : {}),
    ...(quiet ? {} : { onProgress: (line: string) => console.error(line) }),
  })

  let failures = 0
  let synced = 0

  for (const [index, account] of accounts.entries()) {
    const remaining = deadline - Date.now()
    if (remaining <= 0) {
      console.error(
        `deadline reached; ${accounts.length - index} workspace(s) left for the next run`,
      )
      break
    }

    const prefix = accounts.length > 1 ? `${account.team_name}: ` : ''
    if (accounts.length > 1 && !quiet) {
      console.error(`— ${account.team_name} (${account.team_id})`)
    }

    let report
    const client = new SlackClient(account)
    try {
      report = await syncAccount({
        account,
        client,
        options: options(remaining),
      })
      await loadDirectory({ client, teamId: account.team_id })
    } catch (error) {
      failures += 1
      console.error(`FAIL ${account.team_name} (${account.team_id}): ${messageOf(error)}`)
      continue
    }

    if (report.skipped === 'locked') {
      console.log('another sync is already running; nothing to do')
      return 0
    }

    synced += 1
    console.log(
      `${prefix}synced ${report.channels_synced}/${report.channels_seen} channels in ` +
        `${Math.round(report.duration_ms / 1000)}s: +${report.messages_added} messages, ` +
        `+${report.replies_added} replies, ${report.deletions_marked} deletions, ` +
        `${report.threads_fetched} threads`,
    )
    if (report.truncated) {
      console.error(`${prefix}deadline reached; the remaining channels continue on the next run`)
    }
    for (const failure of report.errors) {
      console.error(`${prefix}${failure.channel}: ${failure.error}`)
    }
  }

  return synced === 0 && failures > 0 ? 1 : 0
}

async function commandArchive(args: ParsedArgs): Promise<number> {
  const subcommand = args.positional[0] ?? 'stats'
  const store = new FileCredentialStore()

  if (subcommand === 'migrate') {
    const status = await ensureArchiveSchema({ log: (line) => console.error(line) })
    console.log(
      status.locked
        ? `schema v${status.from}: a sync holds the lock, retry when it finishes`
        : status.applied.length > 0
          ? `schema migrated ${status.from} -> ${status.to}`
          : `schema v${status.to}, up to date`,
    )
    return 0
  }

  await ensureArchiveSchema()
  const index = new ArchiveIndex()
  try {
    if (subcommand === 'search') {
      const query = args.positional.slice(1).join(' ')
      if (!query) throw new Error('archive search needs a query')
      const channel = stringFlag(args, 'channel')
      const reference = stringFlag(args, 'workspace')
      const team = reference ? (await store.resolve(reference)).team_id : undefined
      const directories = new Map<string, Awaited<ReturnType<DirectoryStore['read']>>>()
      const directoryFor = async (teamId: string): Promise<Directory | undefined> => {
        if (!directories.has(teamId)) {
          directories.set(teamId, await new DirectoryStore().read(teamId))
        }
        return directories.get(teamId)
      }
      const teamIds = team ? [team] : (await store.list()).map((summary) => summary.team_id)
      const filter = channel
        ? conversationFilter(channel, await Promise.all(teamIds.map(directoryFor)))
        : {}
      const hits = index.search({
        query,
        limit: numberFlag(args, 'limit', 20),
        includeDeleted: args.flags.get('deleted') === true,
        ...filter,
        ...(team ? { team } : {}),
      })
      if (hits.length === 0) {
        console.log(`no hits for "${query}"`)
        return 0
      }
      const labels = team ? new Map<string, string>() : await workspaceLabels(store)
      for (const hit of hits) await directoryFor(hit.team)
      for (const hit of hits) {
        const directory = directories.get(hit.team)
        const label = conversationLabel({ id: hit.channel, name: hit.channel_name }, directory)
        const author =
          hit.username ?? directoryUser(directory, hit.user ?? '') ?? hit.user ?? 'unknown'
        const deleted = hit.deleted ? ' [deleted]' : ''
        const scope = labels.get(hit.team)
        const prefix = scope ? `${scope} · ` : ''
        const text = resolveReferences(hit.text ?? '', directoryScope(directory))
        console.log(`[${hit.ts}] ${prefix}${label} ${author}: ${text}${deleted}`)
      }
      return 0
    }

    if (subcommand === 'reindex') {
      const total = await rebuildIndex({ index })
      console.log(`reindexed ${total} messages`)
      return 0
    }

    if (subcommand === 'stats') {
      const reference = stringFlag(args, 'workspace') ?? stringFlag(args, 'team')
      const team = reference ? (await store.resolve(reference)).team_id : undefined
      const stats = index.stats(team)
      const scope = team
        ? `${(await workspaceLabels(store)).get(team) ?? team}: `
        : ''
      console.log(
        `${scope}${stats.messages} messages (${stats.deleted} deleted) in ${stats.channels} channels` +
          (stats.oldest ? `, ${stats.oldest} .. ${stats.newest}` : ''),
      )
      return 0
    }

    throw new Error(`unknown archive subcommand: ${subcommand}`)
  } finally {
    index.close()
  }
}

// --- agent forum ---------------------------------------------------------
//
// The group is `agent-forum`, with `forum` as an alias. Every write is a dry run
// unless `--post` says otherwise, exactly like `send`.

async function forumContext(): Promise<ForumContext> {
  return openForum()
}

async function resolveTransport(teamId: string): Promise<Transport> {
  const account = await new FileCredentialStore().get(teamId)
  return transportFor(account)
}

async function forumRefresh(context: ForumContext, deadlineMs = 25_000): Promise<CatchUpReport> {
  const store = new FileCredentialStore()
  const accounts = await Promise.all(
    (await store.list()).map((summary) => store.get(summary.team_id)),
  )
  if (accounts.length === 0) {
    return { synced: 0, locked: false, errors: ['no stored Slack session; reading the archive only'] }
  }
  const byTeam = new Map(accounts.map((account) => [account.team_id, account]))
  const clients = new Map<string, SlackClient>()
  const clientFor = (teamId: string): SlackClient | undefined => {
    const account = byTeam.get(teamId)
    if (!account) return undefined
    let client = clients.get(teamId)
    if (!client) {
      client = new SlackClient(account)
      clients.set(teamId, client)
    }
    return client
  }
  return catchUp({
    bindings: context.file.channels,
    clientFor,
    accountFor: (teamId) => byTeam.get(teamId),
    deadlineMs,
    ...(process.stderr.isTTY
      ? { onProgress: (line: string) => console.error(`    ${line}`) }
      : {}),
  })
}

function printWrite(result: WriteResult): number {
  const where = forumChannelLabel(result.binding)
  if (result.warning) console.error(`note: ${result.warning}`)
  if (result.kind === 'preview') {
    console.log(`dry run — would post to ${where} (${result.target}):`)
    console.log(result.text)
    console.error('nothing sent; add --post to commit')
    return 0
  }
  console.log(`sent ts ${result.ts} to ${where}; audit: ${result.audit}`)
  return 0
}

function forumInboxLine(entry: InboxEntry, now: Date): string {
  const decision = entry.decision ? `local:${entry.decision.decision}` : 'local:-'
  return `${entry.task.id}  ${stateLabel(entry.task, now)}  ${decision}  ${entry.task.from}: ${entry.task.title}`
}

async function commandForum(args: ParsedArgs): Promise<number> {
  const subcommand = args.positional[0] ?? 'list'
  const rest = args.positional.slice(1)
  const confirm = args.flags.get('post') === true
  const by = 'cli' as const
  const context = await forumContext()
  // Interacting with the forum is what keeps this machine visible; the announcement
  // is throttled and never blocks the command.
  void announcePresence(context, resolveTransport).catch(() => {})

  switch (subcommand) {
    case 'list': {
      const identity = context.identity
      console.log(
        `agent forum as ${
          identity ? `${identity.name}@${identity.host}` : 'unregistered'
        } (${context.identityKey})`,
      )
      if (context.file.channels.length === 0) {
        console.log('no channel bound; add one with `slack-browser agent-forum add <channel>`')
      }
      const names = await workspaceLabels(new FileCredentialStore())
      for (const binding of context.file.channels) {
        const scope = names.get(binding.workspace) ?? binding.workspace
        const flags = [
          binding.private ? 'private' : 'public',
          scope,
          binding.sign ? 'signed' : 'unsigned',
          `since ${binding.since_ts}`,
        ].join(' · ')
        console.log(`  ${forumChannelLabel(binding)} (${binding.channel}) — ${flags}`)
      }
      const limits = context.limits
      console.log(
        `limits: ${limits.posts_per_hour}/h posts · ${limits.open_tasks} open · depth ${limits.depth} · ` +
          `ttl ${limits.ttl_hours}h · ack ${Math.round(limits.ack_seconds / 60)}m · ` +
          `stall ${Math.round(limits.stall_seconds / 60)}m`,
      )
      const auto = await autoStatus()
      console.log(`auto: ${auto.detail}${auto.enabled ? ' (enabled)' : ''}`)
      if (context.file.ignored.length > 0) {
        console.log(`retired: ${context.file.ignored.join(', ')}`)
      }
      return 0
    }

    case 'add': {
      const reference = rest[0]
      if (!reference) throw new Error('agent-forum add needs a channel name or id')
      const account = await new FileCredentialStore().resolve(stringFlag(args, 'workspace'))
      const transport = await transportFor(account)
      const { binding, added } = await bindChannel(context, transport, {
        reference,
        backfill: args.flags.get('backfill') === true,
        signed: args.flags.get('signed') === true,
        noPresence: args.flags.get('no-presence') === true,
      })
      console.log(
        added
          ? `bound ${forumChannelLabel(binding)} (${binding.channel}) in ${account.team_name}`
          : `${forumChannelLabel(binding)} was already bound in ${account.team_name}`,
      )
      console.log(
        args.flags.get('backfill') === true
          ? 'reading the whole channel history, oldest first'
          : `reading from ts ${binding.since_ts}; pass --backfill to read what came before`,
      )
      if (!binding.announce) console.log('this machine will not announce presence in that channel')
      if (!context.identity) {
        console.log('next: pick a name with `slack-browser agent-forum register <name>`')
      }
      return 0
    }

    case 'remove': {
      const reference = rest[0]
      if (!reference) throw new Error('agent-forum remove needs a channel name or id')
      const binding = context.file.channels.find(
        (entry) => entry.name === reference || entry.channel === reference,
      )
      if (!binding) throw new Error(`no binding matches "${reference}"; agent-forum list shows them`)
      await context.forum.unbind(binding)
      console.log(`unbound ${forumChannelLabel(binding)} (${binding.channel}); the decision log is kept`)
      return 0
    }

    case 'register': {
      const name = rest[0]
      if (!name) throw new Error('agent-forum register needs a name')
      const capabilities = (stringFlag(args, 'capabilities') ?? '')
        .split(',')
        .map((entry) => entry.trim())
        .filter(Boolean)
      const identity = {
        name: normalizeName(name),
        host: normalizeName(stringFlag(args, 'host') ?? defaultHost()),
        capabilities: normalizeCapabilities(capabilities),
      }
      if (!identity.name) throw new Error(`"${name}" is not a usable name`)
      const result = await registerIdentity(context, identity)
      console.log(
        `registered ${identity.name}@${identity.host} as ${context.identityKey}` +
          (identity.capabilities.length > 0 ? ` [${identity.capabilities.join(', ')}]` : ''),
      )
      // The one write that does not ask first: it announces this machine's own
      // name, and a peer nobody can discover is a peer nobody can address.
      if (args.flags.get('no-post') === true) {
        console.log('not announced; peers will not discover this name until you post the register')
        return 0
      }
      if (!result.announce) {
        console.log('the roster already knows this name; nothing to announce')
        return 0
      }
      if (context.file.channels.length === 0) {
        console.log('no channel bound yet; the announcement goes out the next time you register')
        return 0
      }
      const announced = await announce(context, resolveTransport, identity)
      console.log(
        `announced in ${announced.posted} channel(s)` +
          (announced.skipped > 0 ? `, ${announced.skipped} already knew` : ''),
      )
      return 0
    }

    case 'unregister': {
      const identity = requireIdentity(context)
      const named = rest[0]
      if (named && normalizeName(named) !== identity.name) {
        throw new Error(
          `this machine is ${identity.name}@${identity.host}; unregister "${named}" is a different peer`,
        )
      }
      const quiet = args.flags.get('no-post') === true
      const result = await unregisterIdentity(context, resolveTransport, { announce: !quiet })
      console.log(`unregistered ${result.label}`)
      console.log(
        result.posted > 0
          ? `announced the departure in ${result.posted} channel(s); peers drop it from their roster`
          : 'nothing announced; peers keep it in the roster until the claim expires',
      )
      console.log('register again with `slack-browser agent-forum register <name>` whenever you want')
      return 0
    }

    case 'peers': {
      const views = await loadViews(context, {
        ...(stringFlag(args, 'workspace') ? { workspace: stringFlag(args, 'workspace')! } : {}),
      })
      if (views.length === 0) {
        console.log('no channel bound; add one with `slack-browser agent-forum add <channel>`')
        return 0
      }
      for (const view of views) {
        for (const line of renderPeers(view, context.now, context.limits)) {
          console.log(line)
        }
      }
      return 0
    }

    case 'inbox': {
      const offline = args.flags.get('offline') === true
      let note = ''
      if (!offline) {
        const result = await forumRefresh(context)
        if (result.errors.length > 0) note = ` · ${result.errors[0]}`
        else if (result.locked) note = ' · a sync holds the lock'
      }
      const fresh = await reload(context)
      const views = await loadViews(fresh)
      const report = inboxEntries(fresh, views, { all: args.flags.get('all') === true })
      if (args.flags.get('json') === true) {
        for (const entry of report.entries) {
          console.log(
            JSON.stringify({
              id: entry.task.id,
              state: entry.task.state,
              local: entry.decision?.decision ?? null,
              self_approved: entry.decision?.self_approved ?? false,
              from: entry.task.from,
              to: entry.task.to,
              title: entry.task.title,
              channel: entry.view.binding.name,
              ts: entry.task.ts,
            }),
          )
        }
      } else {
        console.log(renderInbox(fresh, report, { offline, note }))
      }
      const newest = newestTs(report)
      if (newest && !offline) {
        for (const view of new Set(report.entries.map((entry) => entry.view))) {
          await fresh.forum.setCursor(fresh.identityKey, view.key, newest)
        }
      }
      return 0
    }

    case 'task': {
      const id = rest[0]
      if (!id) throw new Error('agent-forum task needs an id (or a unique prefix)')
      if (!args.flags.get('offline')) await forumRefresh(context, 10_000)
      const fresh = await reload(context)
      const views = await loadViews(fresh)
      const { view, task } = locateTask(views, id)
      console.log(renderTask(fresh, task, view.binding, view.decisions.get(task.id), view))
      return 0
    }

    case 'post': {
      const to = rest[0]
      const title = rest[1]
      if (!to || !title) throw new Error('agent-forum post needs a destination and a title')
      const body = stringFlag(args, 'body') ?? rest.slice(2).join(' ')
      if (!body) throw new Error('agent-forum post needs a body: pass --body "<text>"')
      const result = await postTask(context, resolveTransport, {
        to,
        title,
        body,
        ...(stringFlag(args, 'tag') ? { tag: stringFlag(args, 'tag')! } : {}),
        ...(stringFlag(args, 'deadline') ? { deadline: stringFlag(args, 'deadline')! } : {}),
        ...(stringFlag(args, 'parent') ? { parent: stringFlag(args, 'parent')! } : {}),
        ...(stringFlag(args, 'binding') ? { binding: stringFlag(args, 'binding')! } : {}),
        confirm,
        by,
      })
      return printWrite(result)
    }

    case 'accept':
    case 'decline':
    case 'progress':
    case 'result':
    case 'ack':
    case 'reopen':
    case 'cancel': {
      const id = rest[0]
      if (!id) throw new Error(`agent-forum ${subcommand} needs a task id`)
      const body = stringFlag(args, 'body') ?? (subcommand === 'progress' || subcommand === 'result' ? rest.slice(1).join(' ') : undefined)
      const ok = subcommand === 'result' ? args.flags.get('fail') !== true : undefined
      const result = await replyTo(context, resolveTransport, {
        id,
        kind: subcommand,
        ...(body ? { body } : {}),
        ...(ok === undefined ? {} : { ok }),
        confirm,
        by,
      })
      return printWrite(result)
    }

    case 'wait':
    case 'poll': {
      const offline = args.flags.get('offline') === true
      const deadline = numberFlag(args, 'deadline', 25)
      const refresh = () => forumRefresh(context, deadline * 1000)
      if (subcommand === 'poll') {
        const result = await refresh()
        if (args.flags.get('quiet') !== true) {
          console.log(
            `refreshed ${result.synced} channel(s)${result.errors.length > 0 ? ` · ${result.errors.join(' · ')}` : ''}`,
          )
        }
        return result.errors.length > 0 ? 1 : 0
      }

      const timeout = numberFlag(args, 'timeout', args.flags.get('follow') === true ? 30 : 0)
      const follow = args.flags.get('follow') === true
      let seen = new Set<string>()
      let rounds = 0
      for (;;) {
        rounds += 1
        const result = await waitForWork(context, { waitSeconds: timeout, offline, refresh })
        for (const entry of result.report.entries) {
          if (seen.has(entry.task.id)) continue
          seen.add(entry.task.id)
          if (args.flags.get('json') === true) {
            console.log(
              JSON.stringify({
                event: 'task',
                id: entry.task.id,
                from: entry.task.from,
                to: entry.task.to,
                title: entry.task.title,
                channel: entry.view.binding.name,
                ts: entry.task.ts,
              }),
            )
          } else {
            console.log(forumInboxLine(entry, context.now))
          }
        }
        if (!follow) {
          if (result.report.entries.length === 0) console.log('nothing pending')
          return 0
        }
        await new Promise((done) => setTimeout(done, Math.max(5, timeout) * 1000))
      }
    }

    case 'auto': {
      const action = rest[0] ?? 'status'
      if (action === 'on') {
        console.log(await autoOn())
        return 0
      }
      if (action === 'off') {
        console.log(await autoOff())
        return 0
      }
      if (action !== 'status') throw new Error(`unknown auto action: ${action}`)
      const status = await autoStatus()
      console.log(`auto: ${status.detail}${status.enabled ? ' (enabled)' : ''}`)
      return 0
    }

    case 'forget': {
      const label = rest[0]
      if (!label) throw new Error('agent-forum forget needs a name or name@host')
      const [rawName, rawHost] = label.split('@')
      const retired = normalizeName(rawName ?? '') + (rawHost ? `@${normalizeName(rawHost)}` : '')
      const added = await context.forum.ignore(retired)
      console.log(
        added
          ? `retired ${label} locally; its tasks stop showing up in the inbox`
          : `${label} was already retired`,
      )
      return 0
    }

    default:
      throw new Error(
        `unknown agent-forum subcommand: ${subcommand}\n` +
          'try list, add, remove, register, peers, inbox, task, post, accept, decline, progress, result, ack, reopen, cancel, wait, poll, auto, forget',
      )
  }
}

async function main(): Promise<number> {
  const argv = process.argv.slice(2)
  const command = argv[0] ?? 'help'
  const args = parseArgs(argv.slice(1))

  switch (command) {
    case 'login':
      return commandLogin(args)
    case 'accounts':
      return commandAccounts()
    case 'workspace':
    case 'workspaces':
      return commandWorkspace(args)
    case 'assist':
      return commandAssist(args)
    case 'status':
      return commandStatus(args)
    case 'read':
      return commandRead(args)
    case 'thread':
      return commandThread(args)
    case 'send':
      return commandSend(args)
    case 'sync':
      return commandSync(args)
    case 'archive':
      return commandArchive(args)
    case 'agent-forum':
    case 'forum':
      return commandForum(args)
    case 'serve':
      await serve()
      return 0
    case 'help':
    case '--help':
    case '-h':
      console.log(HELP)
      return 0
    default:
      console.error(`Unknown command: ${command}\n`)
      console.error(HELP)
      return 1
  }
}

main()
  .then((code) => {
    process.exitCode = code
  })
  .catch((error: unknown) => {
    if (error instanceof AuthError || error instanceof SchemaError) {
      console.error(`${error.message}\n${error.hint}`)
      process.exitCode = 2
      return
    }
    if (error instanceof ForumError) {
      console.error(error.hint ? `${error.message}\n${error.hint}` : error.message)
      process.exitCode = 1
      return
    }
    console.error(error instanceof Error ? error.message : String(error))
    process.exitCode = 1
  })
