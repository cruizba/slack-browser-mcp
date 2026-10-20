/**
 * Paths are resolved lazily so tests and users can point the tool elsewhere
 * through the environment.
 */

import { homedir } from 'node:os'
import { join } from 'node:path'

const HOME = homedir()

function envDir(name: string, fallback: string): string {
  const value = process.env[name]?.trim()
  return value ? value : fallback
}

export function xdgConfigHome(): string {
  return envDir('XDG_CONFIG_HOME', join(HOME, '.config'))
}

export function xdgDataHome(): string {
  return envDir('XDG_DATA_HOME', join(HOME, '.local', 'share'))
}

export function configDir(): string {
  return envDir('SLACK_BROWSER_MCP_CONFIG_DIR', join(xdgConfigHome(), 'slack-browser-mcp'))
}

export function accountsDir(): string {
  return join(configDir(), 'accounts')
}

/**
 * User preferences (the default workspace). No secrets, but it lives next to
 * the accounts so a single backup or copy carries the whole setup.
 */
export function settingsPath(): string {
  return envDir('SLACK_BROWSER_MCP_CONFIG_FILE', join(configDir(), 'config.json'))
}

/**
 * Chromium user data directory for the one interactive login. It holds the
 * Slack session cookies, so it is as sensitive as the account files.
 */
export function dataDir(): string {
  return join(xdgDataHome(), 'slack-browser-mcp')
}

export function profileDir(): string {
  return join(dataDir(), 'profile')
}

/** Guard that stops two interactive logins from running at once. */
export function loginLockPath(): string {
  return join(dataDir(), 'login.lock')
}

/** stderr of the login the agent starts, so a failure is readable afterwards. */
export function loginLogPath(): string {
  return join(dataDir(), 'login.log')
}

/** Append-only record of what this tool posted, next to the browser profile. */
export function auditLogPath(): string {
  return join(dataDir(), 'audit.jsonl')
}

/** Plain-text mirror of the conversations, one JSONL file per channel. */
export function archiveDir(): string {
  return join(dataDir(), 'archive')
}

/** Rebuildable SQLite index over the archive, derived data only. */
export function archiveIndexPath(): string {
  return join(dataDir(), 'archive.sqlite')
}
