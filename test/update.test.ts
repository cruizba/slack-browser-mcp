import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import {
  compareVersions,
  latestTag,
  parseVersion,
  planUpdate,
  readInstalled,
  update,
  versionLine,
  type Runner,
} from '../src/update.ts'

interface Call {
  command: string
  args: string[]
  inherit: boolean
}

type Answer = string | Error

/**
 * Answers git by the arguments it got, minus `-C <root>`, and records every call
 * so a test can assert the checkout and install.sh never happen by accident.
 */
function fakeRunner(calls: Call[], answers: Record<string, Answer>): Runner {
  return async (command, args, options = {}) => {
    const list = [...args]
    const rest = list[0] === '-C' ? list.slice(2) : list
    calls.push({ command, args: list, inherit: options.inherit === true })
    if (command === 'bash') return { stdout: '', stderr: '' }
    if (command === 'git' && rest[0] === 'checkout') return { stdout: '', stderr: '' }
    const key = `${command} ${rest.join(' ')}`
    const answer = answers[key]
    if (answer === undefined) throw new Error(`unexpected command: ${key}`)
    if (answer instanceof Error) throw answer
    return { stdout: answer, stderr: '' }
  }
}

/** A detached checkout standing exactly on v0.2.0, the latest release. */
function atLatest(overrides: Record<string, Answer> = {}): Record<string, Answer> {
  return {
    'git rev-parse --is-inside-work-tree': 'true\n',
    'git rev-parse --short HEAD': 'abc1234\n',
    'git describe --tags --always': 'v0.2.0\n',
    'git describe --tags --exact-match HEAD': 'v0.2.0\n',
    'git symbolic-ref --short -q HEAD': new Error('detached HEAD'),
    'git status --porcelain': '',
    'git fetch --tags --quiet origin': '',
    'git tag --list': 'v0.1.0\nv0.2.0\n',
    'git rev-list --count v0.2.0..HEAD': '0\n',
    'git rev-list --count HEAD..v0.2.0': '0\n',
    ...overrides,
  }
}

function argsOf(call: Call): string[] {
  return call.args[0] === '-C' ? call.args.slice(2) : call.args
}

async function withCheckout<T>(body: (root: string) => Promise<T>): Promise<T> {
  const root = await mkdtemp(join(tmpdir(), 'slack-update-'))
  try {
    await writeFile(join(root, 'install.sh'), '#!/usr/bin/env bash\n', 'utf8')
    return await body(root)
  } finally {
    await rm(root, { recursive: true, force: true })
  }
}

test('tags order by semver, not by string', () => {
  assert.equal(latestTag(['v1.9.0', 'v1.10.0']), 'v1.10.0')
  assert.equal(latestTag(['v0.10.0', '0.9.0']), 'v0.10.0')
  const rc1 = parseVersion('v2.0.0-rc.1')
  const rc10 = parseVersion('v2.0.0-rc.10')
  const final = parseVersion('v2.0.0')
  assert.ok(rc1 && rc10 && final)
  assert.ok(compareVersions(rc1, rc10) < 0)
  assert.ok(compareVersions(rc10, final) < 0)
  assert.equal(compareVersions(final, final), 0)
})

test('the latest stable tag wins, and prereleases only on request', () => {
  assert.equal(latestTag(['v2.0.0-rc.1', 'v1.4.0', 'nightly']), 'v1.4.0')
  assert.equal(latestTag(['v2.0.0-rc.1', 'v1.4.0'], { prerelease: true }), 'v2.0.0-rc.1')
  assert.equal(latestTag(['nightly', '2026-10-29']), undefined)
})

test('only a checkout strictly behind the release is updated', () => {
  assert.deepEqual(planUpdate({ latest: 'v1.0.0', ahead: 0, behind: 0 }), {
    kind: 'up-to-date',
    target: 'v1.0.0',
  })
  assert.deepEqual(planUpdate({ latest: 'v1.0.0', ahead: 0, behind: 3 }), {
    kind: 'update',
    target: 'v1.0.0',
    ahead: 0,
    behind: 3,
  })
  assert.deepEqual(planUpdate({ latest: 'v1.0.0', ahead: 2, behind: 0 }), {
    kind: 'ahead',
    target: 'v1.0.0',
    ahead: 2,
  })
  assert.deepEqual(planUpdate({ latest: 'v1.0.0', ahead: 2, behind: 1 }), {
    kind: 'diverged',
    target: 'v1.0.0',
    ahead: 2,
    behind: 1,
  })
  assert.deepEqual(planUpdate({ latest: undefined, ahead: 0, behind: 0 }), { kind: 'no-releases' })
  assert.deepEqual(planUpdate({ latest: 'v1.0.0', target: 'v0.9.0', ahead: 1, behind: 4 }), {
    kind: 'update',
    target: 'v0.9.0',
    ahead: 1,
    behind: 4,
  })
})

test('readInstalled names the tag, the branch and local changes', async () => {
  const calls: Call[] = []
  assert.deepEqual(await readInstalled('/repo', fakeRunner(calls, atLatest())), {
    commit: 'abc1234',
    describe: 'v0.2.0',
    tag: 'v0.2.0',
    dirty: false,
  })

  const onMain = atLatest({
    'git describe --tags --always': 'v0.2.0-2-gdef5678\n',
    'git describe --tags --exact-match HEAD': new Error('no tag'),
    'git symbolic-ref --short -q HEAD': 'main\n',
    'git status --porcelain': ' M src/cli.ts\n',
  })
  assert.deepEqual(await readInstalled('/repo', fakeRunner([], onMain)), {
    commit: 'abc1234',
    describe: 'v0.2.0-2-gdef5678',
    branch: 'main',
    dirty: true,
  })

  const outside = atLatest({ 'git rev-parse --is-inside-work-tree': 'false\n' })
  assert.equal(await readInstalled('/repo', fakeRunner([], outside)), undefined)
})

test('update checks the release out and lets install.sh rebuild', async () => {
  await withCheckout(async (root) => {
    const calls: Call[] = []
    const lines: string[] = []
    const answers = atLatest({
      'git describe --tags --always': 'v0.1.0\n',
      'git describe --tags --exact-match HEAD': 'v0.1.0\n',
      'git rev-list --count v0.2.0..HEAD': '0\n',
      'git rev-list --count HEAD..v0.2.0': '3\n',
    })
    const code = await update({
      root,
      run: fakeRunner(calls, answers),
      log: (line) => lines.push(line),
    })

    assert.equal(code, 0)
    const checkout = calls.find((call) => argsOf(call).join(' ') === 'checkout --detach v0.2.0')
    assert.ok(checkout, 'checkout --detach v0.2.0')
    assert.deepEqual(checkout.args.slice(0, 2), ['-C', root])

    const install = calls.find((call) => call.command === 'bash')
    assert.ok(install, 'install.sh runs')
    assert.deepEqual(install.args, [join(root, 'install.sh'), '--no-pull'])
    assert.equal(install.inherit, true)

    assert.ok(lines.some((line) => line.includes('moving to v0.2.0')))
    assert.ok(lines.some((line) => line.includes('updated to ')))
    assert.ok(lines.some((line) => line.includes('restart the agent')))
  })
})

test('a checkout already on the release is left alone', async () => {
  await withCheckout(async (root) => {
    const calls: Call[] = []
    const lines: string[] = []
    const code = await update({ root, run: fakeRunner(calls, atLatest()), log: (line) => lines.push(line) })

    assert.equal(code, 0)
    assert.equal(calls.some((call) => call.command === 'bash'), false)
    assert.equal(calls.some((call) => argsOf(call)[0] === 'checkout'), false)
    assert.ok(lines.some((line) => line.includes('already on v0.2.0')))
  })
})

test('main past the release is not rolled back', async () => {
  await withCheckout(async (root) => {
    const calls: Call[] = []
    const lines: string[] = []
    const answers = atLatest({
      'git describe --tags --always': 'v0.2.0-2-gdef5678\n',
      'git describe --tags --exact-match HEAD': new Error('no tag'),
      'git symbolic-ref --short -q HEAD': 'main\n',
      'git rev-list --count v0.2.0..HEAD': '2\n',
      'git rev-list --count HEAD..v0.2.0': '0\n',
    })
    const code = await update({ root, run: fakeRunner(calls, answers), log: (line) => lines.push(line) })

    assert.equal(code, 0)
    assert.equal(calls.some((call) => argsOf(call)[0] === 'checkout'), false)
    assert.ok(lines.some((line) => line.includes('past v0.2.0')))
    assert.ok(lines.some((line) => line.includes('--to v0.2.0')))
  })
})

test('--check reports without touching the checkout', async () => {
  await withCheckout(async (root) => {
    const calls: Call[] = []
    const lines: string[] = []
    const answers = atLatest({
      'git describe --tags --always': 'v0.1.0\n',
      'git describe --tags --exact-match HEAD': 'v0.1.0\n',
      'git rev-list --count v0.2.0..HEAD': '0\n',
      'git rev-list --count HEAD..v0.2.0': '3\n',
    })
    const code = await update({
      root,
      check: true,
      run: fakeRunner(calls, answers),
      log: (line) => lines.push(line),
    })

    assert.equal(code, 0)
    assert.equal(calls.some((call) => argsOf(call)[0] === 'checkout'), false)
    assert.equal(calls.some((call) => call.command === 'bash'), false)
    assert.ok(lines.some((line) => line.includes('v0.2.0 available (3 commits behind)')))
  })
})

test('--to pins an older release', async () => {
  await withCheckout(async (root) => {
    const calls: Call[] = []
    const lines: string[] = []
    const answers = atLatest({
      'git rev-list --count v0.1.0..HEAD': '4\n',
      'git rev-list --count HEAD..v0.1.0': '0\n',
    })
    const code = await update({
      root,
      target: '0.1.0',
      run: fakeRunner(calls, answers),
      log: (line) => lines.push(line),
    })

    assert.equal(code, 0)
    const checkout = calls.find((call) => argsOf(call).join(' ') === 'checkout --detach v0.1.0')
    assert.ok(checkout, 'checkout --detach v0.1.0')
  })
})

test('a dirty checkout refuses to move', async () => {
  await withCheckout(async (root) => {
    const answers = atLatest({
      'git describe --tags --always': 'v0.1.0\n',
      'git describe --tags --exact-match HEAD': 'v0.1.0\n',
      'git rev-list --count v0.2.0..HEAD': '0\n',
      'git rev-list --count HEAD..v0.2.0': '3\n',
      'git status --porcelain': ' M src/cli.ts\n',
    })
    await assert.rejects(() => update({ root, run: fakeRunner([], answers), log: () => {} }), /local changes/)
  })
})

test('a checkout with no releases says so instead of guessing', async () => {
  await withCheckout(async (root) => {
    const calls: Call[] = []
    const lines: string[] = []
    const answers = atLatest({ 'git tag --list': '', 'git describe --tags --always': 'abc1234\n' })
    const code = await update({ root, run: fakeRunner(calls, answers), log: (line) => lines.push(line) })

    assert.equal(code, 0)
    assert.equal(calls.some((call) => argsOf(call)[0] === 'checkout'), false)
    assert.ok(lines.some((line) => line.includes('no release tags yet')))
  })
})

test('a copy that is not a git checkout is refused', async () => {
  await withCheckout(async (root) => {
    const answers = atLatest({ 'git rev-parse --is-inside-work-tree': new Error('not a repo') })
    await assert.rejects(
      () => update({ root, run: fakeRunner([], answers), log: () => {} }),
      /not a git checkout/,
    )
  })
})

test('a release checkout without install.sh is refused before moving', async () => {
  const root = await mkdtemp(join(tmpdir(), 'slack-update-'))
  try {
    const answers = atLatest({
      'git describe --tags --always': 'v0.1.0\n',
      'git describe --tags --exact-match HEAD': 'v0.1.0\n',
      'git rev-list --count v0.2.0..HEAD': '0\n',
      'git rev-list --count HEAD..v0.2.0': '3\n',
    })
    await assert.rejects(
      () => update({ root, run: fakeRunner([], answers), log: () => {} }),
      /install\.sh is missing/,
    )
  } finally {
    await rm(root, { recursive: true, force: true })
  }
})

test('versionLine pairs the package version with the tag', async () => {
  await withCheckout(async (root) => {
    await writeFile(join(root, 'package.json'), '{"version":"0.2.0"}', 'utf8')
    assert.equal(await versionLine(root, fakeRunner([], atLatest())), '0.2.0 (v0.2.0)')
  })
})
