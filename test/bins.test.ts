import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { test } from 'node:test'

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
