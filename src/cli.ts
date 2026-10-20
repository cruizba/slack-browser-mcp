#!/usr/bin/env node
/**
 * Human entry point: log in once, inspect the session, read a channel from the
 * shell. `serve` runs the MCP server on stdio.
 */

import { login } from './auth/login.ts'
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
import { syncAccount } from './archive/sync.ts'
import { FileSettingsStore, ensureDefaultWorkspace } from './config/settings.ts'
import { loginLockPath } from './config/paths.ts'
import { AuthError, SchemaError, StoreError } from './errors.ts'
import { main as serve } from './mcp/server.ts'
import { recordSend } from './slack/audit.ts'
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
  slack-browser status [--workspace <ref>]
  slack-browser read <channel> [--limit <n>] [--oldest <ts>] [--latest <ts>] [--workspace <ref>]
  slack-browser thread <channel> <thread_ts> [--workspace <ref>]
  slack-browser send <channel> <message> [--thread <ts>] [--send] [--workspace <ref>]
  slack-browser sync [--full] [--since <days>] [--deadline <seconds>] [--quiet] [--workspace <ref>]
  slack-browser archive search <query> [--channel <name>] [--workspace <ref>] [--limit <n>] [--deleted]
  slack-browser archive stats [--workspace <ref>]
  slack-browser archive reindex
  slack-browser archive migrate
  slack-browser serve

The browser opens once, during login. Everything else talks to the Slack Web API
with the captured session, stored at ~/.config/slack-browser-mcp/accounts with
mode 0600.

Without --workspace, a command uses SLACK_BROWSER_MCP_TEAM, then the default
saved by "workspace use", then the only stored session. "sync" without
--workspace covers every stored workspace.`

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

  if (args.flags.get('send') !== true) {
    console.log(`dry run — would post to ${target}${threadTs ? ` in thread ${threadTs}` : ''}:`)
    console.log(text)
    console.error('nothing sent; add --send to commit')
    return 0
  }

  const posted = await client.postMessage({
    channel: channel.id,
    text,
    ...(threadTs ? { thread_ts: threadTs } : {}),
  })
  const audit = await recordSend({
    workspace: account.team_name,
    channel: label,
    channel_id: channel.id,
    ...(threadTs ? { thread_ts: threadTs } : {}),
    message_ts: posted.ts,
    text,
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
    ...(stringFlag(args, 'since') ? { backfillDays: numberFlag(args, 'since', 30) } : {}),
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
    console.error(error instanceof Error ? error.message : String(error))
    process.exitCode = 1
  })
