/**
 * Opt-in background polling.
 *
 * The timer is not installed by `install.sh`: a user who only reads should never
 * end up with a process watching a channel they forgot about. `agent-forum auto
 * on` writes it, `auto off` takes it away, and `auto status` says which state the
 * machine is in, including "systemd is not running here".
 *
 * Polling is a `oneshot` per tick, not a daemon: nothing to supervise, nothing to
 * restart, and no state to get stuck in.
 */

import { execFile } from 'node:child_process'
import { access, mkdir, rm, writeFile } from 'node:fs/promises'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { promisify } from 'node:util'

const run = promisify(execFile)

export const SERVICE_NAME = 'slack-browser-forum.service'
export const TIMER_NAME = 'slack-browser-forum.timer'

export function unitDir(): string {
  return join(homedir(), '.config', 'systemd', 'user')
}

export function renderUnits(args: { node?: string; entry?: string } = {}): {
  service: string
  timer: string
} {
  const node = args.node ?? process.execPath
  const entry = args.entry ?? process.argv[1] ?? ''
  return {
    service: `[Unit]
Description=Slack agent forum poll
Documentation=https://github.com/cruizba/slack-browser-mcp

[Service]
Type=oneshot
ExecStart=${node} ${entry} agent-forum poll --quiet
`,
    timer: `[Unit]
Description=Poll the bound agent forum channels

[Timer]
OnBootSec=2min
OnUnitActiveSec=60s
Unit=${SERVICE_NAME}

[Install]
WantedBy=timers.target
`,
  }
}

async function systemctl(...args: string[]): Promise<{ ok: boolean; output: string }> {
  try {
    const { stdout, stderr } = await run('systemctl', ['--user', ...args], { timeout: 15_000 })
    return { ok: true, output: `${stdout}${stderr}`.trim() }
  } catch (error) {
    const detail = error instanceof Error ? error.message : String(error)
    return { ok: false, output: detail }
  }
}

async function hasSystemd(): Promise<boolean> {
  try {
    await access('/run/systemd/system')
    return true
  } catch {
    return false
  }
}

export async function autoStatus(): Promise<{ installed: boolean; enabled: boolean; detail: string }> {
  if (!(await hasSystemd())) {
    return { installed: false, enabled: false, detail: 'systemd is not running on this machine' }
  }
  const active = await systemctl('is-active', TIMER_NAME)
  const enabled = await systemctl('is-enabled', TIMER_NAME)
  if (!active.ok && !enabled.ok) {
    return {
      installed: false,
      enabled: false,
      detail: 'no timer installed (or no systemd user session)',
    }
  }
  const state = firstLine(active.output) || 'unknown'
  return {
    installed: true,
    enabled: firstLine(enabled.output) === 'enabled',
    detail: `timer ${state}`,
  }
}

function firstLine(output: string): string {
  const line = (output.split('\n')[0] ?? '').replace(/^systemctl: /, '').trim()
  return line.startsWith('Command failed') ? '' : line
}

export async function autoOn(): Promise<string> {
  if (!(await hasSystemd())) {
    throw new Error('systemd is not running here; `agent-forum poll` can be scheduled by other means')
  }
  const units = renderUnits()
  const dir = unitDir()
  await mkdir(dir, { recursive: true })
  await writeFile(join(dir, SERVICE_NAME), units.service, { encoding: 'utf8', mode: 0o644 })
  await writeFile(join(dir, TIMER_NAME), units.timer, { encoding: 'utf8', mode: 0o644 })
  const reload = await systemctl('daemon-reload')
  if (!reload.ok) throw new Error(`systemctl --user daemon-reload failed: ${reload.output}`)
  const enable = await systemctl('enable', '--now', TIMER_NAME)
  if (!enable.ok) throw new Error(`systemctl --user enable --now ${TIMER_NAME} failed: ${enable.output}`)
  return `enabled ${TIMER_NAME}: polls the bound channels every 60 s`
}

export async function autoOff(): Promise<string> {
  const stop = await systemctl('disable', '--now', TIMER_NAME)
  await rm(join(unitDir(), TIMER_NAME), { force: true })
  await rm(join(unitDir(), SERVICE_NAME), { force: true })
  await systemctl('daemon-reload')
  return stop.ok ? `disabled ${TIMER_NAME}; the units are gone` : 'the units are gone (nothing was enabled)'
}
