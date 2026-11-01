/**
 * Prints the notes for one range, so `release.sh` can hand them to
 * `gh release create --notes-file`.
 *
 *   git log --no-merges --format='%H%x1f%s%x1f%b%x1e' v0.3.2..v0.4.0 \
 *     | node --experimental-strip-types src/release/notes-cli.ts v0.3.2 v0.4.0 --slug owner/repo
 *
 * It reads the log rather than running git so a failure in one half is visible as
 * a failure in that half, and so the tests can feed a fixture.
 */

import { readFileSync } from 'node:fs'

import { parseLog, renderNotes } from './notes.ts'

const args = process.argv.slice(2)
const positional: string[] = []
let slug: string | undefined
for (let index = 0; index < args.length; index += 1) {
  const arg = args[index]!
  if (arg === '--slug') {
    slug = args[index + 1]
    index += 1
    continue
  }
  positional.push(arg)
}

const [from, to] = positional
process.stdout.write(
  renderNotes(parseLog(readFileSync(0, 'utf8')), {
    ...(from ? { from } : {}),
    ...(to ? { to } : {}),
    ...(slug ? { slug } : {}),
  }),
)
