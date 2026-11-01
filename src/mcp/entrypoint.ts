/**
 * Whether this module is the program the user ran.
 *
 * `npm link` and every package manager put a symlink in the bin directory, so
 * `process.argv[1]` is the link while `import.meta.url` is the file it points at.
 * Comparing them raw is false on every real invocation, and the entrypoint silently
 * never runs: the process exits and its client reports a closed connection.
 */

import { realpathSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

export function isEntrypoint(argv1: string | undefined, moduleUrl: string): boolean {
  if (!argv1) return false
  try {
    return realpathSync(argv1) === fileURLToPath(moduleUrl)
  } catch {
    return false
  }
}
