/**
 * Moving a checkout to a release.
 *
 * The version is a semver tag (`v1.4.0`), not the tip of `main`: `main` is where
 * work lands, and a release is the commit a machine is allowed to stand on. The
 * update command fetches the tags, compares the one at `HEAD` with the latest
 * stable one, checks it out detached and then hands the build to `install.sh
 * --no-pull`, so linking the agents and rewriting the units keeps a single
 * implementation.
 *
 * A checkout that sits on `main` past the latest tag is left alone. Moving it
 * back to the tag is a pin, not an update, and `--to` is how a user asks for it.
 */

import { execFile, spawn } from 'node:child_process'
import { access, readFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { promisify } from 'node:util'

import { StoreError } from './errors.ts'

const execFileAsync = promisify(execFile)

const TAG = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$/

export interface Version {
  major: number
  minor: number
  patch: number
  prerelease: string[]
}

/** The checkout a running build belongs to: `src/` and `dist/` are one level deep. */
export function checkoutRoot(moduleFile: string): string {
  return dirname(dirname(moduleFile))
}

export function parseVersion(tag: string): Version | undefined {
  const match = TAG.exec(tag.trim())
  if (!match) return undefined
  return {
    major: Number(match[1]),
    minor: Number(match[2]),
    patch: Number(match[3]),
    prerelease: match[4] ? match[4].split('.') : [],
  }
}

function compareIdentifiers(a: string, b: string): number {
  const numericA = /^\d+$/.test(a)
  const numericB = /^\d+$/.test(b)
  if (numericA && numericB) return Number(a) - Number(b)
  if (numericA) return -1
  if (numericB) return 1
  return a < b ? -1 : a > b ? 1 : 0
}

/** Semver precedence: 1.9.0 < 1.10.0, 2.0.0-rc.1 < 2.0.0. */
export function compareVersions(a: Version, b: Version): number {
  for (const key of ['major', 'minor', 'patch'] as const) {
    const difference = a[key] - b[key]
    if (difference !== 0) return difference
  }
  if (a.prerelease.length === 0 && b.prerelease.length === 0) return 0
  if (a.prerelease.length === 0) return 1
  if (b.prerelease.length === 0) return -1
  const length = Math.max(a.prerelease.length, b.prerelease.length)
  for (let index = 0; index < length; index += 1) {
    const left = a.prerelease[index]
    const right = b.prerelease[index]
    if (left === undefined) return -1
    if (right === undefined) return 1
    const difference = compareIdentifiers(left, right)
    if (difference !== 0) return difference
  }
  return 0
}

/**
 * The tag an update would install. Anything that is not semver is ignored (a
 * date tag, a branch saved as a tag), and a prerelease only wins when the user
 * asked for one, so `v2.0.0-rc.1` never becomes the default.
 */
export function latestTag(
  tags: readonly string[],
  options: { prerelease?: boolean } = {},
): string | undefined {
  let best: { tag: string; version: Version } | undefined
  for (const tag of tags) {
    const version = parseVersion(tag)
    if (!version) continue
    if (!options.prerelease && version.prerelease.length > 0) continue
    if (!best || compareVersions(version, best.version) > 0) best = { tag, version }
  }
  return best?.tag
}

export interface Installed {
  commit: string
  /** `git describe --tags --always`: a tag, `v0.1.0-3-gabc1234`, or a sha. */
  describe: string
  /** The tag `HEAD` stands on exactly, when it stands on one. */
  tag?: string
  /** Absent on a detached checkout, which is where a release lives. */
  branch?: string
  dirty: boolean
}

export interface CommandOutput {
  stdout: string
  stderr: string
}

export type Runner = (
  command: string,
  args: readonly string[],
  options?: { cwd?: string; inherit?: boolean },
) => Promise<CommandOutput>

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

export const defaultRunner: Runner = async (command, args, options = {}) => {
  if (options.inherit) {
    await new Promise<void>((resolve, reject) => {
      const child = spawn(command, [...args], {
        ...(options.cwd ? { cwd: options.cwd } : {}),
        stdio: 'inherit',
      })
      child.once('error', reject)
      child.once('close', (code) => {
        if (code === 0) resolve()
        else reject(new StoreError(`${command} exited with code ${code ?? 'null'}.`))
      })
    })
    return { stdout: '', stderr: '' }
  }
  try {
    const { stdout, stderr } = await execFileAsync(command, [...args], {
      ...(options.cwd ? { cwd: options.cwd } : {}),
      maxBuffer: 8 * 1024 * 1024,
    })
    return { stdout: String(stdout), stderr: String(stderr) }
  } catch (error) {
    const failed = error as { stderr?: string; message?: string }
    throw new StoreError(failed.stderr?.trim() || failed.message || `Could not run ${command}.`)
  }
}

async function git(root: string, run: Runner, args: readonly string[]): Promise<string> {
  const result = await run('git', ['-C', root, ...args])
  return result.stdout.trim()
}

/** `undefined` when the running copy is not a git checkout, which is not an error. */
export async function readInstalled(
  root: string,
  run: Runner = defaultRunner,
): Promise<Installed | undefined> {
  try {
    if ((await git(root, run, ['rev-parse', '--is-inside-work-tree'])) !== 'true') return undefined
  } catch {
    return undefined
  }

  const commit = await git(root, run, ['rev-parse', '--short', 'HEAD'])
  const describe = await git(root, run, ['describe', '--tags', '--always'])

  let tag: string | undefined
  try {
    tag = (await git(root, run, ['describe', '--tags', '--exact-match', 'HEAD'])) || undefined
  } catch {
    // On a branch, or between releases: no tag names this commit.
  }

  let branch: string | undefined
  try {
    branch = (await git(root, run, ['symbolic-ref', '--short', '-q', 'HEAD'])) || undefined
  } catch {
    // Detached: the state a release checkout is in.
  }

  const dirty = (await git(root, run, ['status', '--porcelain'])).length > 0
  return {
    commit,
    describe: describe || commit,
    ...(tag !== undefined ? { tag } : {}),
    ...(branch !== undefined ? { branch } : {}),
    dirty,
  }
}

/** Tags as the remote has them now; a local tag the remote dropped survives here. */
export async function fetchTags(root: string, run: Runner = defaultRunner): Promise<string[]> {
  try {
    await run('git', ['-C', root, 'fetch', '--tags', '--quiet', 'origin'])
  } catch (error) {
    throw new StoreError(
      `Could not fetch the release tags from origin: ${messageOf(error)}. ` +
        'Updating compares releases, so it needs the checkout and the network.',
    )
  }
  const listed = await git(root, run, ['tag', '--list'])
  return listed.length > 0 ? listed.split('\n').map((tag) => tag.trim()).filter(Boolean) : []
}

async function countCommits(
  root: string,
  run: Runner,
  from: string,
  to: string,
): Promise<number> {
  const count = Number(await git(root, run, ['rev-list', '--count', `${from}..${to}`]))
  return Number.isFinite(count) ? count : 0
}

export type Plan =
  | { kind: 'no-releases' }
  | { kind: 'up-to-date'; target: string }
  | { kind: 'update'; target: string; ahead: number; behind: number }
  | { kind: 'ahead'; target: string; ahead: number }
  | { kind: 'diverged'; target: string; ahead: number; behind: number }

/**
 * What an update would do, with no side effects. `behind` counts the commits the
 * release has that `HEAD` does not: only a checkout that is strictly behind can
 * move to the tag without rewriting anything.
 */
export function planUpdate(input: {
  latest: string | undefined
  target?: string | undefined
  ahead: number
  behind: number
}): Plan {
  const target = input.target ?? input.latest
  if (target === undefined) return { kind: 'no-releases' }
  if (input.target !== undefined) {
    if (input.ahead === 0 && input.behind === 0) return { kind: 'up-to-date', target }
    return { kind: 'update', target, ahead: input.ahead, behind: input.behind }
  }
  if (input.ahead === 0 && input.behind === 0) return { kind: 'up-to-date', target }
  if (input.ahead === 0) return { kind: 'update', target, ahead: 0, behind: input.behind }
  if (input.behind === 0) return { kind: 'ahead', target, ahead: input.ahead }
  return { kind: 'diverged', target, ahead: input.ahead, behind: input.behind }
}

export function describeInstalled(installed: Installed): string {
  if (installed.tag) return `${installed.tag} (${installed.commit})`
  if (installed.branch) return `${installed.branch} @ ${installed.commit}`
  return installed.commit
}

/** The tag as the user typed it: `0.2.0` and `v0.2.0` both resolve. */
function resolveTarget(target: string, tags: readonly string[]): string {
  const wanted = target.trim()
  if (tags.includes(wanted)) return wanted
  if (!wanted.startsWith('v') && tags.includes(`v${wanted}`)) return `v${wanted}`
  throw new StoreError(
    `No release tag called ${wanted}. Available: ${tags.join(', ') || 'none'}.`,
  )
}

export interface UpdateOptions {
  root: string
  check?: boolean
  target?: string
  prerelease?: boolean
  /** `false` adds install.sh's --no-systemd. */
  systemd?: boolean
  run?: Runner
  log?: (line: string) => void
}

function countLabel(count: number): string {
  return `${count} commit${count === 1 ? '' : 's'}`
}

/** It never force-moves a dirty tree, and never treats a branch ahead of the release as an update. */
export async function update(options: UpdateOptions): Promise<number> {
  const run = options.run ?? defaultRunner
  const log = options.log ?? ((line: string) => console.log(line))
  const { root } = options

  const installed = await readInstalled(root, run)
  if (!installed) {
    throw new StoreError(
      `This copy at ${root} is not a git checkout, so there is no release to move to. ` +
        'Reinstall it with install.sh, or run the update from the checkout that is linked.',
    )
  }

  const tags = await fetchTags(root, run)
  const latest = latestTag(tags, options.prerelease === true ? { prerelease: true } : {})
  const target = options.target !== undefined ? resolveTarget(options.target, tags) : undefined

  // With --to the counts belong to that tag, not to the latest release: the
  // message has to describe the move the user asked for.
  const reference = target ?? latest
  const ahead = reference === undefined ? 0 : await countCommits(root, run, reference, 'HEAD')
  const behind = reference === undefined ? 0 : await countCommits(root, run, 'HEAD', reference)
  const plan = planUpdate({
    latest,
    ...(target !== undefined ? { target } : {}),
    ahead,
    behind,
  })

  log(`installed ${describeInstalled(installed)}${installed.dirty ? ' · local changes' : ''}`)
  if (latest !== undefined) log(`latest release ${latest}`)

  if (plan.kind === 'no-releases') {
    log('no release tags yet: nothing to update to. install.sh tracks main until one exists.')
    return 0
  }

  if (options.check === true) {
    if (plan.kind === 'up-to-date') log(`up to date with ${plan.target}`)
    else if (plan.kind === 'update') {
      if (target !== undefined) log(`${plan.target} is the requested target`)
      else log(`${plan.target} available (${countLabel(plan.behind)} behind)`)
    } else if (plan.kind === 'ahead') {
      log(`this checkout is ${countLabel(plan.ahead)} past ${plan.target}; update only moves between releases`)
    } else {
      log(`HEAD has diverged from ${plan.target} (${countLabel(plan.ahead)} ahead, ${countLabel(plan.behind)} behind)`)
    }
    return 0
  }

  if (plan.kind === 'up-to-date') {
    log(`already on ${plan.target} (${installed.commit})`)
    return 0
  }
  if (plan.kind === 'ahead') {
    log(
      `nothing to do: this checkout is ${countLabel(plan.ahead)} past ${plan.target}. ` +
        `Use --to ${plan.target} to pin the release.`,
    )
    return 0
  }
  if (plan.kind === 'diverged') {
    log(
      `nothing to do: HEAD has diverged from ${plan.target} ` +
        `(${countLabel(plan.ahead)} ahead, ${countLabel(plan.behind)} behind). Use --to <tag>.`,
    )
    return 0
  }

  if (installed.dirty) {
    throw new StoreError(
      `The checkout at ${root} has local changes; commit or stash them before updating, ` +
        'or re-run install.sh --no-pull to rebuild what is already there.',
    )
  }

  const install = join(root, 'install.sh')
  try {
    await access(install)
  } catch {
    throw new StoreError(
      `install.sh is missing from ${root}, so the build cannot be refreshed. Reinstall the checkout.`,
    )
  }

  log(`moving to ${plan.target}`)
  await run('git', ['-C', root, 'checkout', '--detach', plan.target], { cwd: root })
  log('rebuilding with install.sh --no-pull')
  try {
    await run('bash', [install, '--no-pull', ...(options.systemd === false ? ['--no-systemd'] : [])], {
      cwd: root,
      inherit: true,
    })
  } catch (error) {
    throw new StoreError(
      `The checkout is now at ${plan.target}, but install.sh failed: ${messageOf(error)} ` +
        'Fix what it reported and run install.sh --no-pull.',
    )
  }

  const after = await readInstalled(root, run)
  log(`updated to ${after ? describeInstalled(after) : plan.target}`)
  log(
    'restart the agent so its MCP server loads the new build; the sync and forum timers ' +
      'run the new code on their next tick.',
  )
  return 0
}

export async function versionLine(root: string, run: Runner = defaultRunner): Promise<string> {
  let version = 'unknown'
  try {
    const parsed = JSON.parse(await readFile(join(root, 'package.json'), 'utf8')) as {
      version?: string
    }
    if (typeof parsed.version === 'string' && parsed.version.length > 0) version = parsed.version
  } catch {
    // A copy without package.json still has a commit; that is worth printing.
  }

  const installed = await readInstalled(root, run)
  if (!installed) return version
  const where = installed.tag ?? installed.describe
  return installed.dirty ? `${version} (${where}, local changes)` : `${version} (${where})`
}
