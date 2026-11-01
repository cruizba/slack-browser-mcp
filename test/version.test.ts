import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { test } from 'node:test'

import { VERSION } from '../src/version.ts'

const pkg = JSON.parse(
  await readFile(new URL('../package.json', import.meta.url), 'utf8'),
) as { version?: string }

test('the version the MCP handshake and the register envelope report is the package version', () => {
  assert.equal(VERSION, pkg.version)
})
