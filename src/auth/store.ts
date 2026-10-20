/**
 * Credential storage for the Slack sessions captured by the browser login.
 *
 * The tokens are the whole account: anyone who reads this file can post as the
 * user. The store therefore refuses to read a file that is not owned by the
 * current user and not exactly 0600, instead of silently accepting it.
 */

import { chmod, mkdir, open, readFile, readdir, rename, rm, stat } from 'node:fs/promises'
import { join } from 'node:path'
import { z } from 'zod'

import { accountsDir } from '../config/paths.ts'
import { FileSettingsStore, type SettingsStore } from '../config/settings.ts'
import { StoreError } from '../errors.ts'

export interface SlackAccount {
  team_id: string
  team_name: string
  team_domain?: string | undefined
  user?: string | undefined
  user_id?: string | undefined
  token: string
  d_cookie: string
  d_cookie_name?: string | undefined
  saved_at: string
  last_ok_at?: string | undefined
}

/** What the agent is allowed to see: everything except the secrets. */
export interface AccountSummary {
  team_id: string
  team_name: string
  team_domain?: string | undefined
  user?: string | undefined
  last_ok_at?: string | undefined
}

export interface CredentialStore {
  list(): Promise<AccountSummary[]>
  get(teamId: string): Promise<SlackAccount>
  /** Resolves a user-supplied reference (team id, domain or name). */
  resolve(reference?: string): Promise<SlackAccount>
  default(): Promise<DefaultWorkspace | null>
  save(account: SlackAccount): Promise<void>
  markValidated(teamId: string, when?: Date): Promise<void>
  remove(teamId: string): Promise<void>
}

export type DefaultSource = 'environment' | 'config' | 'only'

export interface DefaultWorkspace {
  team_id: string
  source: DefaultSource
}

const AccountSchema = z.object({
  team_id: z.string().regex(/^[A-Za-z0-9_-]{1,64}$/),
  team_name: z.string().min(1),
  team_domain: z.string().min(1).optional(),
  user: z.string().min(1).optional(),
  user_id: z.string().min(1).optional(),
  token: z.string().regex(/^xox[a-z]-/),
  d_cookie: z.string().min(1),
  d_cookie_name: z.string().min(1).optional(),
  saved_at: z.string().min(1),
  last_ok_at: z.string().min(1).optional(),
})

function summarize(account: SlackAccount): AccountSummary {
  return {
    team_id: account.team_id,
    team_name: account.team_name,
    ...(account.team_domain ? { team_domain: account.team_domain } : {}),
    ...(account.user ? { user: account.user } : {}),
    ...(account.last_ok_at ? { last_ok_at: account.last_ok_at } : {}),
  }
}

function formatAccounts(accounts: AccountSummary[]): string {
  return accounts.map((account) => `${account.team_id} (${account.team_name})`).join(', ')
}

/**
 * Team id to team name, for output that can span workspaces. Best effort: a
 * broken account file must not take down a search over the local archive.
 */
export async function workspaceLabels(store: CredentialStore): Promise<Map<string, string>> {
  try {
    const accounts = await store.list()
    return new Map(accounts.map((account) => [account.team_id, account.team_name]))
  } catch {
    return new Map()
  }
}

function permissionError(file: string, mode: number): StoreError {
  const octal = (mode & 0o777).toString(8).padStart(3, '0')
  return new StoreError(
    `Refusing to read ${file}: permissions are ${octal}, expected 600. ` +
      `Run: chmod 600 ${file}`,
  )
}

export class FileCredentialStore implements CredentialStore {
  private readonly dir: string
  private readonly settings: SettingsStore

  constructor(dir: string = accountsDir(), settings: SettingsStore = new FileSettingsStore()) {
    this.dir = dir
    this.settings = settings
  }

  /**
   * Precedence: the caller's reference, then SLACK_BROWSER_MCP_TEAM, then the
   * default saved with `slack-browser workspace use`, then the only session.
   */
  async resolve(reference?: string): Promise<SlackAccount> {
    const accounts = await this.list()
    if (accounts.length === 0) {
      throw new StoreError('No Slack session stored yet. Run `slack-browser login`.')
    }

    const explicit = reference?.trim()
    if (explicit) return this.get(this.pick(accounts, explicit).team_id)

    const fromEnv = process.env.SLACK_BROWSER_MCP_TEAM?.trim()
    if (fromEnv) {
      return this.get(this.pick(accounts, fromEnv, ' from SLACK_BROWSER_MCP_TEAM').team_id)
    }

    const configured = (await this.settings.read()).default_workspace
    if (configured) {
      return this.get(
        this.pick(accounts, configured, ' from default_workspace in config.json').team_id,
      )
    }

    if (accounts.length === 1) return this.get(accounts[0]!.team_id)
    throw new StoreError(
      `Several Slack sessions are stored: ${formatAccounts(accounts)}. ` +
        'Pass --workspace (or the workspace argument), or choose a permanent default with ' +
        '`slack-browser workspace use <name|domain|team-id>`.',
    )
  }

  async default(): Promise<DefaultWorkspace | null> {
    const accounts = await this.list()
    if (accounts.length === 0) return null

    const fromEnv = process.env.SLACK_BROWSER_MCP_TEAM?.trim()
    if (fromEnv) {
      return {
        team_id: this.pick(accounts, fromEnv, ' from SLACK_BROWSER_MCP_TEAM').team_id,
        source: 'environment',
      }
    }

    const configured = (await this.settings.read()).default_workspace
    if (configured) {
      return {
        team_id: this.pick(accounts, configured, ' from default_workspace in config.json').team_id,
        source: 'config',
      }
    }

    if (accounts.length === 1) return { team_id: accounts[0]!.team_id, source: 'only' }
    return null
  }

  private pick(accounts: AccountSummary[], wanted: string, context = ''): AccountSummary {
    const lowered = wanted.toLowerCase()
    const exact = accounts.filter(
      (a) =>
        a.team_id.toLowerCase() === lowered ||
        a.team_domain?.toLowerCase() === lowered ||
        a.team_name.toLowerCase() === lowered,
    )
    const matches =
      exact.length > 0
        ? exact
        : accounts.filter(
            (a) =>
              a.team_name.toLowerCase().includes(lowered) ||
              a.team_domain?.toLowerCase().includes(lowered),
          )

    if (matches.length === 1) return matches[0]!
    if (matches.length === 0) {
      const fix = context
        ? ' Fix it with `slack-browser workspace use <name|domain|team-id>`.'
        : ' Run `slack-browser accounts` to see the stored workspaces.'
      throw new StoreError(
        `No Slack session matches "${wanted}"${context}. Stored: ${formatAccounts(accounts)}.${fix}`,
      )
    }
    throw new StoreError(`"${wanted}"${context} is ambiguous: ${formatAccounts(matches)}.`)
  }

  async list(): Promise<AccountSummary[]> {
    let entries: string[]
    try {
      entries = await readdir(this.dir)
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return []
      throw error
    }
    const accounts: AccountSummary[] = []
    for (const entry of entries.filter((name) => name.endsWith('.json')).sort()) {
      const account = await this.readFile(join(this.dir, entry))
      accounts.push(summarize(account))
    }
    return accounts
  }

  async get(teamId: string): Promise<SlackAccount> {
    const account = await this.readFile(this.pathFor(teamId))
    if (account.team_id !== teamId) {
      throw new StoreError(`${this.pathFor(teamId)} declares team_id ${account.team_id}`)
    }
    return account
  }

  async save(account: SlackAccount): Promise<void> {
    const parsed = AccountSchema.parse(account)
    await mkdir(this.dir, { recursive: true, mode: 0o700 })
    await chmod(this.dir, 0o700)

    const target = this.pathFor(parsed.team_id)
    const temporary = join(this.dir, `.${parsed.team_id}.${process.pid}.tmp`)
    const handle = await open(temporary, 'w', 0o600)
    try {
      await handle.writeFile(`${JSON.stringify(parsed, null, 2)}\n`, 'utf8')
      await handle.sync()
    } finally {
      await handle.close()
    }
    await rename(temporary, target)
  }

  async markValidated(teamId: string, when: Date = new Date()): Promise<void> {
    const account = await this.get(teamId)
    await this.save({ ...account, last_ok_at: when.toISOString() })
  }

  async remove(teamId: string): Promise<void> {
    await rm(this.pathFor(teamId), { force: true })
  }

  private pathFor(teamId: string): string {
    if (!/^[A-Za-z0-9_-]{1,64}$/.test(teamId)) {
      throw new StoreError(`Invalid team id: ${teamId}`)
    }
    return join(this.dir, `${teamId}.json`)
  }

  private async readFile(file: string): Promise<SlackAccount> {
    let info
    try {
      info = await stat(file)
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') {
        throw new StoreError(`No stored Slack session at ${file}. Run \`slack-browser login\`.`)
      }
      throw error
    }

    if ((info.mode & 0o077) !== 0) throw permissionError(file, info.mode)
    const uid = process.getuid?.()
    if (uid !== undefined && info.uid !== uid) {
      throw new StoreError(`Refusing to read ${file}: it belongs to uid ${info.uid}, not ${uid}.`)
    }

    const raw = await readFile(file, 'utf8')
    const result = AccountSchema.safeParse(JSON.parse(raw))
    if (!result.success) {
      throw new StoreError(
        `Stored session ${file} is not readable as an account ` +
          `(${result.error.issues.map((i) => `${i.path.join('.')}: ${i.message}`).join('; ')}). ` +
          'Run `slack-browser login` again.',
      )
    }
    return result.data
  }
}
