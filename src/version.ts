/**
 * One version for everything that reports one: the MCP handshake and the `register`
 * envelope. It comes from `package.json`, which `release.sh` bumps, so there is no
 * second copy to keep in step.
 */

import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

function fromPackageFile(): string | undefined {
  try {
    const root = join(dirname(fileURLToPath(import.meta.url)), '..')
    const parsed = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8')) as {
      version?: unknown
    }
    return typeof parsed.version === 'string' && parsed.version.length > 0
      ? parsed.version
      : undefined
  } catch {
    return undefined
  }
}

/** `0.0.0` only when the file is gone, which this install (a checkout behind `npm link`) never does. */
export const VERSION = fromPackageFile() ?? '0.0.0'
