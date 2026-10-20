/**
 * Slack refuses to sign in a browser that reports `navigator.webdriver`, which
 * is exactly what Playwright's own launcher produces. So the login starts a
 * plain Chrome process and attaches to it over CDP: same profile, same cookies,
 * no automation flags on the browser that shows the sign-in page.
 */

import { spawn, type ChildProcess } from 'node:child_process'
import { constants } from 'node:fs'
import { access } from 'node:fs/promises'
import { createServer } from 'node:net'

const DEFAULT_CANDIDATES = [
  '/opt/google/chrome/chrome',
  '/usr/bin/google-chrome',
  '/usr/bin/google-chrome-stable',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
  '/snap/bin/chromium',
]

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

export async function findChromeBinary(
  candidates: Array<string | undefined> = [process.env.CHROME_PATH, ...DEFAULT_CANDIDATES],
): Promise<string> {
  for (const candidate of candidates) {
    if (!candidate) continue
    try {
      await access(candidate, constants.X_OK)
      return candidate
    } catch {
      continue
    }
  }
  throw new Error(
    'Google Chrome not found. Install Chrome, or point CHROME_PATH at the binary.',
  )
}

export async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = createServer()
    server.unref()
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      const address = server.address()
      const port = typeof address === 'object' && address !== null ? address.port : 0
      server.close(() => resolve(port))
    })
  })
}

function isAlive(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

function signalGroup(child: ChildProcess, signal: NodeJS.Signals): void {
  if (child.pid === undefined) return
  try {
    process.kill(-child.pid, signal)
  } catch {
    // best effort: the group already exited
  }
}

async function waitForDevTools(
  endpoint: string,
  child: ChildProcess,
  timeoutMs: number,
): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (child.pid !== undefined && !isAlive(child.pid)) {
      throw new Error(
        `Chrome exited before it accepted connections. Another window may be using the profile at ` +
          `${process.env.XDG_DATA_HOME ?? '~/.local/share'}/slack-browser-mcp/profile; close it and retry.`,
      )
    }
    const ready = await fetch(`${endpoint}/json/version`)
      .then((response) => response.ok)
      .catch(() => false)
    if (ready) return
    await sleep(250)
  }
  throw new Error(`Chrome did not expose its debugging endpoint on ${endpoint} in ${timeoutMs}ms.`)
}

export interface ChromeHandle {
  endpoint: string
  close(): Promise<void>
}

/**
 * `--disable-sync` and `--disable-extensions` are not cosmetic. Signing in to
 * Google during Slack's SSO also signs the browser into the user's account, and
 * sync then installs their extension set — wallet included — into the very
 * browser that captures the session cookie. An extension with the `cookies`
 * permission can read `d`, which page JavaScript cannot.
 */
export function chromeArgs(options: {
  profileDir: string
  port: number
  headless?: boolean
}): string[] {
  return [
    `--user-data-dir=${options.profileDir}`,
    `--remote-debugging-port=${options.port}`,
    '--no-first-run',
    '--no-default-browser-check',
    '--disable-sync',
    '--disable-extensions',
    '--disable-features=Translate,MediaRouter',
    ...(options.headless ? ['--headless=new'] : []),
    'about:blank',
  ]
}

export async function launchChrome(options: {
  profileDir: string
  headless?: boolean
  timeoutMs?: number
}): Promise<ChromeHandle> {
  const binary = await findChromeBinary()
  const port = await freePort()
  const child = spawn(binary, chromeArgs({ profileDir: options.profileDir, port, ...(options.headless ? { headless: true } : {}) }), {
    detached: true,
    stdio: 'ignore',
  })
  child.unref()

  const endpoint = `http://127.0.0.1:${port}`
  try {
    await waitForDevTools(endpoint, child, options.timeoutMs ?? 20_000)
  } catch (error) {
    signalGroup(child, 'SIGKILL')
    throw error
  }

  return {
    endpoint,
    close: async () => {
      if (child.pid === undefined) return
      signalGroup(child, 'SIGTERM')
      const deadline = Date.now() + 5000
      while (isAlive(child.pid) && Date.now() < deadline) await sleep(100)
      if (isAlive(child.pid)) signalGroup(child, 'SIGKILL')
    },
  }
}
