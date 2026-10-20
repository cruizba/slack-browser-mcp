/**
 * One interactive login. The browser is only used to obtain the session: after
 * this, every tool call goes straight to the Slack Web API.
 */

import { mkdir } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { chromium, type Browser, type BrowserContext, type Page } from 'playwright'

import { profileDir } from '../config/paths.ts'
import { AuthError } from '../errors.ts'
import { launchChrome } from './chrome.ts'
import type { AccountSummary, SlackAccount, CredentialStore } from './store.ts'
import { SlackClient } from '../slack/client.ts'

const LOGIN_URL = 'https://app.slack.com/client'
const STORAGE_KEYS = ['localConfig_v2', 'localConfig_v1'] as const

export interface CapturedSession {
  team_id: string
  team_name: string
  team_domain?: string
  token: string
  d_cookie: string
  d_cookie_name: string
}

export interface CaptureOptions {
  timeoutMs?: number
  headless?: boolean
  onProgress?: (message: string) => void
}

export interface LoginResult {
  saved: AccountSummary[]
  rejected: Array<{ team_id: string; team_name: string; reason: string }>
}

interface BootTeam {
  id?: string
  name?: string
  domain?: string
  token?: string
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

async function readBootTeams(page: Page): Promise<BootTeam[]> {
  const raws = await page
    .evaluate((keys: string[]) => {
      const storage = (globalThis as unknown as {
        localStorage: { getItem(key: string): string | null }
      }).localStorage
      const out: Record<string, string | null> = {}
      for (const key of keys) out[key] = storage.getItem(key)
      return out
    }, [...STORAGE_KEYS])
    .catch(() => ({}) as Record<string, string | null>)

  const teams = new Map<string, BootTeam>()
  for (const raw of Object.values(raws)) {
    if (!raw) continue
    let parsed: unknown
    try {
      parsed = JSON.parse(raw)
    } catch {
      continue
    }
    const container = (parsed as { teams?: Record<string, unknown> }).teams
    if (!container || typeof container !== 'object') continue
    for (const value of Object.values(container)) {
      const team = value as BootTeam
      if (team?.id && team.token && !teams.has(team.id)) teams.set(team.id, team)
    }
  }
  return [...teams.values()]
}

async function findSessionCookie(
  context: BrowserContext,
): Promise<{ name: string; value: string } | undefined> {
  const cookies = await context.cookies(['https://app.slack.com', 'https://slack.com'])
  for (const name of ['d', 'd-s']) {
    const cookie = cookies.find((candidate) => candidate.name === name && candidate.value)
    if (cookie) return { name, value: cookie.value }
  }
  return undefined
}

/**
 * Slack sends its xoxc token in the body of every `/api/` POST. Watching those
 * requests is the fallback for the day `localConfig_v2` changes shape.
 */
function watchApiTokens(context: BrowserContext): Set<string> {
  const tokens = new Set<string>()
  context.on('request', (request) => {
    if (request.method() !== 'POST' || !request.url().includes('/api/')) return
    const body = request.postData()
    if (!body) return
    for (const pair of body.split('&')) {
      const index = pair.indexOf('=')
      if (index <= 0) continue
      if (pair.slice(0, index) !== 'token') continue
      const token = decodeURIComponent(pair.slice(index + 1))
      if (token.startsWith('xox')) tokens.add(token)
    }
  })
  return tokens
}

function teamIdFromUrl(url: string): string | undefined {
  return /\/client\/([A-Z0-9]+)/.exec(url)?.[1]
}

export async function captureSessions(options: CaptureOptions = {}): Promise<CapturedSession[]> {
  const timeoutMs = options.timeoutMs ?? 300_000
  const progress = options.onProgress ?? (() => {})
  const directory = profileDir()
  await mkdir(directory, { recursive: true, mode: 0o700 })

  const chrome = await launchChrome({ profileDir: directory, headless: options.headless ?? false })
  let browser: Browser | undefined

  try {
    browser = await chromium.connectOverCDP(chrome.endpoint)
    const context = browser.contexts()[0]
    if (!context) throw new Error('Chrome exposed no browser context to attach to.')
    const observedTokens = watchApiTokens(context)

    const page = context.pages()[0] ?? (await context.newPage())
    await page.goto(LOGIN_URL, { waitUntil: 'domcontentloaded', timeout: 60_000 }).catch(() => {})
    progress('Sign in to Slack in the window that just opened.')

    const deadline = Date.now() + timeoutMs
    while (Date.now() < deadline) {
      const cookie = await findSessionCookie(context)
      const teams = await readBootTeams(page)

      if (cookie && teams.length > 0) {
        return teams.map((team) => ({
          team_id: team.id!,
          team_name: team.name || team.domain || team.id!,
          ...(team.domain ? { team_domain: team.domain } : {}),
          token: team.token!,
          d_cookie: cookie.value,
          d_cookie_name: cookie.name,
        }))
      }

      const teamId = teamIdFromUrl(page.url())
      if (cookie && teamId && observedTokens.size > 0) {
        progress('Signed in, reading the session token from the network.')
        return [...observedTokens].map((token) => ({
          team_id: teamId,
          team_name: teamId,
          token,
          d_cookie: cookie.value,
          d_cookie_name: cookie.name,
        }))
      }

      await sleep(1500)
    }

    const screenshot = join(tmpdir(), 'slack-browser-login-failure.png')
    const title = await page.title().catch(() => '')
    await page.screenshot({ path: screenshot }).catch(() => {})
    throw new AuthError(
      `No Slack session captured after ${Math.round(timeoutMs / 1000)}s ` +
        `(page title: ${JSON.stringify(title)}). The window was closed, the sign-in did not ` +
        `complete, or Slack refused the browser. Screenshot: ${screenshot}`,
      'Run `slack-browser login` again and finish the sign-in in the browser.',
    )
  } finally {
    await browser?.close().catch(() => {})
    await chrome.close()
  }
}

/**
 * Captures the session, proves it with `auth.test` and only then writes it to
 * disk. A stale cookie for a secondary workspace is reported, not stored.
 */
export async function login(
  store: CredentialStore,
  options: CaptureOptions = {},
): Promise<LoginResult> {
  const sessions = await captureSessions(options)
  const saved: AccountSummary[] = []
  const rejected: LoginResult['rejected'] = []

  for (const session of sessions) {
    const account: SlackAccount = {
      team_id: session.team_id,
      team_name: session.team_name,
      ...(session.team_domain ? { team_domain: session.team_domain } : {}),
      token: session.token,
      d_cookie: session.d_cookie,
      d_cookie_name: session.d_cookie_name,
      saved_at: new Date().toISOString(),
    }

    try {
      const client = new SlackClient(account)
      const info = await client.authTest()
      const validated: SlackAccount = {
        ...account,
        team_name: info.team || account.team_name,
        user: info.user,
        user_id: info.user_id,
        last_ok_at: new Date().toISOString(),
      }
      await store.save(validated)
      saved.push({
        team_id: validated.team_id,
        team_name: validated.team_name,
        ...(validated.team_domain ? { team_domain: validated.team_domain } : {}),
        ...(validated.user ? { user: validated.user } : {}),
        last_ok_at: validated.last_ok_at!,
      })
    } catch (error) {
      rejected.push({
        team_id: session.team_id,
        team_name: session.team_name,
        reason: error instanceof Error ? error.message : String(error),
      })
    }
  }

  return { saved, rejected }
}
