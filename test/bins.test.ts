import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { pathToFileURL } from 'node:url'

import { isEntrypoint } from '../src/mcp/entrypoint.ts'

/**
 * A bin whose file does not open with a shebang is not a program: the shell reads
 * the JavaScript and fails with something about `/bin` being a directory. The build
 * keeps the line, so checking the source is enough and needs no compile step.
 */
test('every bin starts with a node shebang', async () => {
  const pkg = JSON.parse(
    await readFile(new URL('../package.json', import.meta.url), 'utf8'),
  ) as { bin?: Record<string, string> }
  const bins = Object.entries(pkg.bin ?? {})
  assert.ok(bins.length > 0, 'package.json declares no bins')

  for (const [name, built] of bins) {
    const source = new URL(`../${built.replace(/^dist\//, 'src/').replace(/\.js$/, '.ts')}`, import.meta.url)
    const first = (await readFile(source, 'utf8')).split('\n')[0]
    assert.equal(first, '#!/usr/bin/env node', `${name} (${built}) needs a shebang in its source`)
  }
})

test('an entrypoint recognises itself when run through a bin symlink', async (t) => {
  const dir = await mkdtemp(join(tmpdir(), 'entrypoint-'))
  t.after(() => rm(dir, { recursive: true, force: true }))
  const real = join(dir, 'main.mjs')
  const link = join(dir, 'bin')
  await writeFile(real, '')
  await symlink(real, link)
  const moduleUrl = pathToFileURL(real).href

  // The symlink is what `process.argv[1]` holds under `npm link`, and comparing it
  // raw is what made the relay exit without ever serving.
  assert.equal(isEntrypoint(link, moduleUrl), true)
  assert.equal(isEntrypoint(real, moduleUrl), true)
  assert.equal(isEntrypoint(join(dir, 'elsewhere'), moduleUrl), false)
  assert.equal(isEntrypoint(undefined, moduleUrl), false)
})
