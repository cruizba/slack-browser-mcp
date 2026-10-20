import assert from 'node:assert/strict'
import { chmod, mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { after, before, describe, it } from 'node:test'

import { chromeArgs, findChromeBinary, freePort } from '../src/auth/chrome.ts'

let root: string

before(async () => {
  root = await mkdtemp(join(tmpdir(), 'slack-browser-mcp-chrome-'))
})

after(async () => {
  await rm(root, { recursive: true, force: true })
})

describe('findChromeBinary', () => {
  it('skips candidates that are missing or not executable', async () => {
    const shellScript = join(root, 'chrome.sh')
    await writeFile(shellScript, '#!/bin/sh\nexit 0\n')
    await chmod(shellScript, 0o755)

    const notExecutable = join(root, 'chrome-noexec')
    await writeFile(notExecutable, '#!/bin/sh\n')
    await chmod(notExecutable, 0o644)

    const resolved = await findChromeBinary([
      undefined,
      notExecutable,
      join(root, 'does-not-exist'),
      shellScript,
    ])
    assert.equal(resolved, shellScript)
  })

  it('names CHROME_PATH when no candidate works', async () => {
    await assert.rejects(
      () => findChromeBinary(['/definitely/not/chrome']),
      /CHROME_PATH/,
    )
  })
})

describe('chromeArgs', () => {
  it('disables sync and extensions, and never enables automation', () => {
    const args = chromeArgs({ profileDir: '/tmp/p', port: 9222 })
    assert.ok(args.includes('--disable-sync'))
    assert.ok(args.includes('--disable-extensions'))
    assert.ok(args.includes('--user-data-dir=/tmp/p'))
    assert.ok(args.includes('--remote-debugging-port=9222'))
    assert.equal(args.some((arg) => arg.includes('enable-automation')), false)
  })

  it('only adds the headless flag when asked', () => {
    assert.equal(chromeArgs({ profileDir: '/p', port: 1 }).some((a) => a.startsWith('--headless')), false)
    assert.ok(chromeArgs({ profileDir: '/p', port: 1, headless: true }).includes('--headless=new'))
  })
})

describe('freePort', () => {
  it('returns a port that can be bound immediately', async () => {
    const port = await freePort()
    assert.ok(port > 1024 && port < 65536, `unexpected port ${port}`)
  })
})
