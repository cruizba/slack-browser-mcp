/**
 * What `slack-browser help` prints, what `<command> --help` prints, and which
 * flags a command is allowed to read.
 *
 * The pages and the whitelist are one table because they answer the same
 * question, and a page that drifts from the flags the code really reads is
 * worse than no page at all. A flag the table does not know is refused before
 * the command runs, so `update --chek` stops instead of moving the checkout.
 */

import { ASSISTED_NICKNAME_ENV } from './config/assist.ts'

const PROGRAM = 'slack-browser'
const WIDTH = 80

interface FlagDoc {
  readonly value?: string
  readonly doc: string
}

interface Subcommand {
  readonly usage: string
  readonly flags: readonly string[]
  readonly notes?: readonly string[]
}

interface Command {
  readonly summary: string
  readonly flags?: readonly string[]
  readonly notes?: readonly string[]
  readonly subcommands?: Readonly<Record<string, Subcommand>>
}

const FLAGS: Readonly<Record<string, FlagDoc>> = {
  all: { doc: 'also list the tasks already decided on this machine' },
  'no-assist': { doc: 'post the text alone, without the AI-agent header' },
  attach: { value: '<path,...>', doc: 'local files to upload with the message, comma-separated' },
  backfill: { doc: 'read the whole channel history, not just what is new' },
  binding: { value: '<name>', doc: 'which bound channel to post in, when several are' },
  body: { value: '<text>', doc: 'the message body of the task or reply' },
  capabilities: { value: '<a,b>', doc: 'capability tags peers can address this machine by' },
  channel: { value: '<name|id>', doc: 'restrict the search to one channel' },
  check: { doc: 'say what would happen and change nothing' },
  clear: { doc: 'drop the setting instead of reading it' },
  deadline: { value: '<seconds>', doc: 'budget for the whole run, split across workspaces' },
  deleted: { doc: 'include the messages Slack no longer serves' },
  fail: { doc: 'report the task as failed instead of done' },
  follow: { doc: 'keep waiting for the next task instead of printing what is there' },
  full: { doc: 're-fetch the whole backfill window instead of the delta' },
  headless: { doc: 'sign in without a visible browser window' },
  host: { value: '<name>', doc: 'the host half of this machine identity (default: its hostname)' },
  json: { doc: 'print one JSON object per line instead of prose' },
  latest: { value: '<ts>', doc: 'Slack ts to bound the page on the newer side' },
  limit: { value: '<n>', doc: 'how many messages or hits to show (default 20)' },
  'no-post': { doc: 'keep the change local instead of announcing it' },
  'no-presence': { doc: 'never announce this machine in that channel' },
  'no-systemd': { doc: 'skip reinstalling the units after the rebuild' },
  offline: { doc: 'read the archive only; do not refresh from Slack' },
  oldest: { value: '<ts>', doc: 'Slack ts to bound the page on the older side' },
  parent: { value: '<id>', doc: 'the task this one continues' },
  post: { doc: 'post for real; without it, print the message and change nothing' },
  pre: { doc: 'consider prerelease tags when picking the latest' },
  quiet: { doc: 'one line per workspace instead of per channel' },
  'reconcile-days': { value: '<days>', doc: 'how far back to re-check edits and deletions (default 7)' },
  send: { doc: 'post for real; without it, print the message and change nothing' },
  signed: { doc: 'sign every post in that channel' },
  since: { value: '<days>', doc: 'days of history the first backfill covers (default 90)' },
  tag: { value: '<cap>', doc: 'the capability the task is addressed to' },
  team: { value: '<ref>', doc: 'same as --workspace' },
  thread: { value: '<ts>', doc: 'post inside that thread instead of the channel' },
  timeout: { value: '<seconds>', doc: 'give up after that long' },
  to: { value: '<tag>', doc: 'the release to move to (default: latest stable)' },
  workspace: { value: '<ref>', doc: 'pick the stored session by name, domain or team id' },
}

const COMMANDS: Readonly<Record<string, Command>> = {
  login: {
    summary: `${PROGRAM} login [--timeout <seconds>] [--headless]`,
    flags: ['timeout', 'headless'],
    notes: [
      'Opens Chrome once and keeps the session it finds there; nothing else in the tool opens a browser. The `xoxc-` token and the `xoxd-` cookie are stored at ~/.config/slack-browser-mcp/accounts with mode 0600.',
      'Sign in to one workspace at a time. The first one becomes the default, and a later run adds a workspace instead of replacing the first.',
    ],
  },
  accounts: {
    summary: `${PROGRAM} accounts`,
    flags: [],
    notes: [
      'One line per stored workspace. `*` marks the default, and the last line says what that choice came from.',
    ],
  },
  workspace: {
    summary: `${PROGRAM} workspace <list|use|clear>`,
    notes: ['The default workspace applies to every call that omits --workspace.'],
    subcommands: {
      list: { usage: `${PROGRAM} workspace [list]`, flags: [], notes: ['Same output as `accounts`.'] },
      use: {
        usage: `${PROGRAM} workspace use <name|domain|team-id>`,
        flags: [],
        notes: ['Saves the default, which outlives the session and the agent that set it.'],
      },
      clear: {
        usage: `${PROGRAM} workspace clear`,
        flags: [],
        notes: [
          'The saved default goes away. With one stored session that session is still used implicitly; the rule only bites once a second one exists.',
        ],
      },
    },
  },
  assist: {
    summary: `${PROGRAM} assist [<nickname>] [--clear]`,
    flags: ['clear'],
    notes: [
      'With no argument it prints what is in force. A nickname replaces the generated default in the header that opens every sent message.',
      `--no-assist on \`send\` skips the header for one message, and ${ASSISTED_NICKNAME_ENV} wins over the saved nickname.`,
    ],
  },
  status: {
    summary: `${PROGRAM} status [--workspace <ref>]`,
    flags: ['workspace'],
    notes: [
      'Asks Slack whether each stored session still works, one workspace at a time. Exit code 2 when any of them fails.',
    ],
  },
  read: {
    summary: `${PROGRAM} read <channel> [--limit <n>] [--oldest <ts>] [--latest <ts>] [--workspace <ref>]`,
    flags: ['limit', 'oldest', 'latest', 'workspace'],
    notes: [
      'Oldest first. Slack serves 100 messages at most, and a page that leaves more behind says so on stderr with the `--oldest` to continue from.',
    ],
  },
  thread: {
    summary: `${PROGRAM} thread <channel> <thread_ts> [--workspace <ref>]`,
    flags: ['workspace'],
    notes: ['Every message in the thread, oldest first.'],
  },
  send: {
    summary: `${PROGRAM} send <channel> <message> [--thread <ts>] [--attach <path,...>] [--send] [--no-assist] [--workspace <ref>]`,
    flags: ['thread', 'attach', 'send', 'no-assist', 'workspace'],
    notes: [
      'A dry run unless --send: it prints the final text, header included, and posts nothing. Every real send is written to the audit log.',
      'Attachments go up as files in the same message as the text, so the thread and the comment arrive together.',
    ],
  },
  sync: {
    summary: `${PROGRAM} sync [--full] [--since <days>] [--deadline <seconds>] [--quiet] [--workspace <ref>]`,
    flags: ['full', 'since', 'deadline', 'quiet', 'reconcile-days', 'workspace'],
    notes: [
      'Refreshes the local archive from every stored workspace unless --workspace names one. The deadline is one budget for the whole run, not per workspace.',
      'It upgrades an archive written by an older layout before touching it. The systemd timer calls it at session start with a deadline.',
    ],
  },
  archive: {
    summary: `${PROGRAM} archive <search|stats|reindex|migrate>`,
    notes: ['Everything here reads the archive that `sync` writes, so it works offline.'],
    subcommands: {
      search: {
        usage: `${PROGRAM} archive search <query> [--channel <name>] [--workspace <ref>] [--limit <n>] [--deleted]`,
        flags: ['channel', 'workspace', 'limit', 'deleted'],
        notes: [
          'Full-text and ranked, across every workspace. It reaches further back than a channel read, which is the point of keeping an archive.',
        ],
      },
      stats: {
        usage: `${PROGRAM} archive stats [--workspace <ref>]`,
        flags: ['workspace', 'team'],
        notes: ['How many messages and channels are filed, and how far back they go.'],
      },
      reindex: {
        usage: `${PROGRAM} archive reindex`,
        flags: [],
        notes: ['Rebuilds the search index from the JSONL. The archive itself is not touched.'],
      },
      migrate: {
        usage: `${PROGRAM} archive migrate`,
        flags: [],
        notes: ['Upgrades an archive written by an older layout. `sync` does the same before it starts.'],
      },
    },
  },
  'agent-forum': {
    summary: `${PROGRAM} agent-forum <subcommand> [args]`,
    notes: [
      'The channel where agents hand each other work: bind one, register a name, then address a peer by name. Every write is a dry run unless --post, and every post is audited.',
      '`forum` is an alias of `agent-forum`.',
    ],
    subcommands: {
      list: {
        usage: `${PROGRAM} agent-forum [list]`,
        flags: [],
        notes: ['Bound channels, this machine identity, the timings in force and any retired peers.'],
      },
      add: {
        usage: `${PROGRAM} agent-forum add <channel> [--backfill] [--signed] [--no-presence] [--workspace <ref>]`,
        flags: ['backfill', 'signed', 'no-presence', 'workspace'],
        notes: ['Binds a channel from the current ts forward; --backfill reads what came before.'],
      },
      remove: {
        usage: `${PROGRAM} agent-forum remove <channel>`,
        flags: [],
        notes: ['Unbinds the channel. The decision log for its tasks is kept.'],
      },
      register: {
        usage: `${PROGRAM} agent-forum register <name> [--host <name>] [--capabilities a,b] [--no-post]`,
        flags: ['host', 'capabilities', 'no-post'],
        notes: [
          'The announcement goes out on its own: a peer nobody can discover is a peer nobody can address. --no-post keeps the name local.',
        ],
      },
      unregister: {
        usage: `${PROGRAM} agent-forum unregister [<name>] [--no-post]`,
        flags: ['no-post'],
        notes: ['--no-post keeps the departure quiet; peers then hold the name until the claim expires.'],
      },
      peers: {
        usage: `${PROGRAM} agent-forum peers [--workspace <ref>]`,
        flags: ['workspace'],
        notes: ['Who else is in the bound channels, and whether they are around.'],
      },
      away: {
        usage: `${PROGRAM} agent-forum away [<note>]`,
        flags: [],
        notes: ['Tells the bound channels this machine is stepping out, with an optional note.'],
      },
      inbox: {
        usage: `${PROGRAM} agent-forum inbox [--all] [--json] [--offline]`,
        flags: ['all', 'json', 'offline'],
        notes: ['What is waiting for this machine, refreshing from Slack first unless --offline.'],
      },
      task: {
        usage: `${PROGRAM} agent-forum task <id>`,
        flags: ['offline'],
        notes: ['The full thread and state of one task, by id or a unique prefix of it.'],
      },
      post: {
        usage: `${PROGRAM} agent-forum post <to> <title> --body <text> [--tag <cap>] [--parent <id>] [--binding <name>] [--post]`,
        flags: ['body', 'tag', 'parent', 'binding', 'post'],
        notes: ['Hands work to a peer by name.'],
      },
      accept: {
        usage: `${PROGRAM} agent-forum accept|decline|progress|result|ack|reopen|cancel <id> [--body <text>] [--post]`,
        flags: ['body', 'post'],
        notes: ['The moves a task supports after it arrives; `result` reports back with --fail when it did not work.'],
      },
      wait: {
        usage: `${PROGRAM} agent-forum wait [--timeout <s>] [--follow] [--json] [--offline]`,
        flags: ['timeout', 'deadline', 'follow', 'json', 'offline'],
        notes: ['Blocks until work lands, or prints what is already there.'],
      },
      poll: {
        usage: `${PROGRAM} agent-forum poll [--quiet] [--deadline <s>]`,
        flags: ['quiet', 'deadline', 'offline'],
        notes: ['The refresh a timer runs. It posts a beacon and nothing else.'],
      },
      auto: {
        usage: `${PROGRAM} agent-forum auto [on|off|status]`,
        flags: [],
        notes: ['Installs or removes the systemd user timer that runs `poll`.'],
      },
      forget: {
        usage: `${PROGRAM} agent-forum forget <name|name@host>`,
        flags: [],
        notes: ['Retires a peer locally, so its tasks stop showing up in the inbox.'],
      },
    },
  },
  update: {
    summary: `${PROGRAM} update [--check] [--to <tag>] [--pre] [--no-systemd]`,
    flags: ['check', 'to', 'pre', 'no-systemd'],
    notes: [
      'Moves the checkout this build runs from to a release: the latest semver tag, or the one named with --to. It fetches the tags, checks the tag out detached and re-runs `install.sh --no-pull` to rebuild and relink.',
      '--check only says what it would do. A checkout on main that is already past the latest release is left alone. Restart the agent afterwards so its MCP server loads the new build.',
    ],
  },
  version: {
    summary: `${PROGRAM} version`,
    flags: [],
    notes: ['The package version and the tag or branch the checkout stands on.'],
  },
  serve: {
    summary: `${PROGRAM} serve`,
    flags: [],
    notes: ['The MCP server on stdio. `install.sh` registers it with each agent; it is not meant to be typed.'],
  },
}

const ALIASES: Readonly<Record<string, string>> = {
  forum: 'agent-forum',
  workspaces: 'workspace',
  '-v': 'version',
  '--version': 'version',
}

const INDEX_NOTES: readonly string[] = [
  'The browser opens once, during login. Everything else talks to the Slack Web API with the captured session, stored at ~/.config/slack-browser-mcp/accounts with mode 0600.',
  'Without --workspace, a command uses SLACK_BROWSER_MCP_TEAM, then the default saved by "workspace use", then the only stored session. "sync" without --workspace covers every stored workspace.',
  `With nothing configured, every sent message is still marked: the header reads "*Message Sent by AI Agent '<client>' of <account>*", naming the MCP client and the account it goes out from. Set your own with "assist <nickname>"; --no-assist posts the text alone, and ${ASSISTED_NICKNAME_ENV} wins over config.json.`,
  '"agent-forum" (alias "forum") is the channel where agents hand each other work: bind a channel once, register a name, then post tasks to a peer by name. Every write is a dry run unless --post, and every post is audited.',
  'Every command answers --help with its own page: what it does, its usage and the flags it takes. `slack-browser help <command>` prints the same page.',
]

function wrap(text: string, width = WIDTH): string {
  const lines: string[] = []
  let line = ''
  for (const word of text.split(/\s+/).filter(Boolean)) {
    if (line === '') line = word
    else if (line.length + 1 + word.length <= width) line += ` ${word}`
    else {
      lines.push(line)
      line = word
    }
  }
  if (line !== '') lines.push(line)
  return lines.join('\n')
}

function flagLines(flags: readonly string[]): string[] {
  const entries = [...new Set(flags)].sort().map((flag) => {
    const spec = FLAGS[flag]
    return {
      key: `--${flag}${spec?.value ? ` ${spec.value}` : ''}`,
      doc: spec?.doc ?? '(undocumented)',
    }
  })
  const width = Math.max(...entries.map((entry) => entry.key.length))
  return entries.map((entry) => `  ${entry.key.padEnd(width)}  ${entry.doc}`)
}

function subcommandOf(name: string, subcommand: string | undefined): Subcommand | undefined {
  const table = COMMANDS[name]?.subcommands
  if (!table || subcommand === undefined) return undefined
  return Object.hasOwn(table, subcommand) ? table[subcommand] : undefined
}

function flagsOf(name: string, subcommand?: string): readonly string[] {
  const command = COMMANDS[name]
  if (!command) return []
  if (!command.subcommands) return command.flags ?? []
  if (subcommand !== undefined) return subcommandOf(name, subcommand)?.flags ?? []
  return Object.values(command.subcommands).flatMap((sub) => sub.flags)
}

/** The table key for a command or one of its aliases; undefined when unknown. */
export function resolveCommand(name: string): string | undefined {
  const key = Object.hasOwn(ALIASES, name) ? ALIASES[name]! : name
  return Object.hasOwn(COMMANDS, key) ? key : undefined
}

export function commandNames(): string[] {
  return Object.keys(COMMANDS)
}

export function subcommandNames(name: string): string[] {
  const table = COMMANDS[name]?.subcommands
  return table ? Object.keys(table) : []
}

/** Every flag a command accepts, in the shape `parseArgs` needs. */
export function flagSpecs(name: string): Record<string, { value?: string }> {
  const specs: Record<string, { value?: string }> = {}
  for (const flag of flagsOf(name)) {
    const value = FLAGS[flag]?.value
    specs[flag] = value === undefined ? {} : { value }
  }
  return specs
}

/** Where `--help` or `-h` sits in the arguments after the command, or -1. */
export function helpRequest(tokens: readonly string[]): number {
  const end = tokens.indexOf('--')
  const scan = end === -1 ? tokens : tokens.slice(0, end)
  return scan.findIndex((token) => token === '--help' || token === '-h')
}

/**
 * The page a help flag asks for, or undefined when there is no help flag or it
 * sits in message text, as in a send whose body mentions --help.
 */
export function helpPageFor(command: string, tokens: readonly string[]): string | undefined {
  const name = resolveCommand(command)
  const at = helpRequest(tokens)
  if (!name || at === -1) return undefined
  if (at === 0) return commandPage(name)
  const subcommand = tokens.find((token) => !token.startsWith('-'))
  return subcommandOf(name, subcommand) ? commandPage(name, subcommand) : undefined
}

export function commandPage(command: string, subcommand?: string): string | undefined {
  const name = resolveCommand(command)
  if (!name) return undefined
  const spec = COMMANDS[name]!
  const sub = subcommandOf(name, subcommand)
  if (subcommand !== undefined && spec.subcommands && !sub) return undefined

  const lines = [`Usage: ${sub?.usage ?? spec.summary}`]
  if (spec.subcommands && !sub) {
    lines.push('', 'Subcommands:')
    for (const entry of Object.values(spec.subcommands)) lines.push(`  ${entry.usage}`)
  }
  for (const note of [...(spec.notes ?? []), ...(sub?.notes ?? [])]) {
    lines.push('', wrap(note))
  }
  const flags = sub ? sub.flags : flagsOf(name)
  if (flags.length > 0) lines.push('', 'Flags:', ...flagLines(flags))
  if (spec.subcommands && !sub) {
    lines.push('', `Run \`${PROGRAM} ${name} <subcommand> --help\` for one of them.`)
  }
  return lines.join('\n')
}

export function indexPage(): string {
  const lines = [`${PROGRAM} — Slack read access from a browser session`, '', 'Usage:']
  for (const spec of Object.values(COMMANDS)) {
    const usages = spec.subcommands
      ? Object.values(spec.subcommands).map((sub) => sub.usage)
      : [spec.summary]
    for (const usage of usages) lines.push(`  ${usage}`)
  }
  lines.push(`  ${PROGRAM} help [<command>]`)
  for (const note of INDEX_NOTES) lines.push('', wrap(note))
  return lines.join('\n')
}

/**
 * What is wrong with the subcommand and the flags a call came with, or null.
 * `parseArgs` has already refused what no command takes; this is the finer
 * whitelist, where a flag belongs to another subcommand or to none.
 */
export function usageProblem(
  command: string,
  subcommand: string | undefined,
  flags: Iterable<string>,
): string | null {
  const name = resolveCommand(command)
  if (!name) return null
  const spec = COMMANDS[name]!
  const hint = `run \`${PROGRAM} ${name} --help\` to see what it takes`

  if (subcommand !== undefined && spec.subcommands && !subcommandOf(name, subcommand)) {
    return (
      `${PROGRAM} ${name}: unknown subcommand "${subcommand}"\n` +
      `try ${Object.keys(spec.subcommands).join(', ')}`
    )
  }

  const allowed = flagsOf(name, subcommand)
  const unknown = [...flags].filter((flag) => !allowed.includes(flag))
  if (unknown.length === 0) return null
  const what = unknown.length === 1 ? 'flag' : 'flags'
  return `${PROGRAM} ${name}: unknown ${what} ${unknown.map((flag) => `--${flag}`).join(', ')}\n${hint}`
}
