/**
 * `forum.json`: which channels this machine talks on, who each local agent is,
 * how big the fences are, and how far each identity has read.
 *
 * It is plain JSON next to `config.json` so a user can edit it, but the shape is
 * validated on read, because a binding pointing at a channel that no longer
 * exists is an error worth naming instead of an empty inbox forever.
 *
 * Nothing here is a credential. Losing the file costs a `register` and an `add`,
 * not a login.
 */

import { chmod, mkdir, open, readFile, rename } from 'node:fs/promises'
import { dirname } from 'node:path'
import { z } from 'zod'

import { forumPath } from '../config/paths.ts'
import { StoreError } from '../errors.ts'
import { agentName, type Identity } from './identity.ts'
import { DEFAULT_LIMITS, LimitsSchema, type ForumLimits } from './limits.ts'

export const FORUM_VERSION = 1

const BindingSchema = z
  .object({
    workspace: z.string().min(1),
    channel: z.string().min(1),
    name: z.string().min(1),
    private: z.boolean().default(false),
    since_ts: z.string().min(1),
    added_at: z.string().min(1),
    sign: z.boolean().default(true),
  })
  .strict()

export type Binding = z.infer<typeof BindingSchema>

const IdentitySchema = z
  .object({
    name: z.string().min(1).max(32),
    host: z.string().min(1).max(32),
    capabilities: z.array(z.string().min(1).max(24)).max(8).default([]),
    registered_at: z.string().min(1),
  })
  .strict()

export interface StoredIdentity extends Identity {
  registered_at: string
}

const CounterSchema = z.object({ hour: z.string(), count: z.number().int().min(0) })

const ForumFileSchema = z
  .object({
    version: z.literal(FORUM_VERSION),
    identities: z.record(z.string(), IdentitySchema).default({}),
    default_identity: z.string().optional(),
    channels: z.array(BindingSchema).default([]),
    limits: LimitsSchema.partial().default({}),
    cursors: z.record(z.string(), z.record(z.string(), z.string())).default({}),
    ignored: z.array(z.string()).default([]),
    posting: z.record(z.string(), CounterSchema).default({}),
  })
  .strict()

export type ForumFile = z.output<typeof ForumFileSchema>

export function emptyForum(): ForumFile {
  return {
    version: FORUM_VERSION,
    identities: {},
    channels: [],
    limits: {},
    cursors: {},
    ignored: [],
    posting: {},
  }
}

export function bindingKey(binding: Pick<Binding, 'workspace' | 'channel'>): string {
  return `${binding.workspace}:${binding.channel}`
}

export function limitsOf(file: ForumFile): ForumLimits {
  const defined = Object.fromEntries(
    Object.entries(file.limits).filter(([, value]) => value !== undefined),
  ) as Partial<ForumLimits>
  return { ...DEFAULT_LIMITS, ...defined }
}

function hourOf(when: Date): string {
  return when.toISOString().slice(0, 13)
}

export class ForumStore {
  private readonly file: string

  constructor(file: string = forumPath()) {
    this.file = file
  }

  get path(): string {
    return this.file
  }

  async read(): Promise<ForumFile> {
    let raw: string
    try {
      raw = await readFile(this.file, 'utf8')
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return emptyForum()
      throw error
    }
    if (raw.trim() === '') return emptyForum()

    let parsed: unknown
    try {
      parsed = JSON.parse(raw)
    } catch {
      throw new StoreError(`${this.file} is not valid JSON. Fix it or delete it.`)
    }
    const result = ForumFileSchema.safeParse(parsed)
    if (!result.success) {
      const issues = result.error.issues.map((issue) => `${issue.path.join('.')}: ${issue.message}`)
      throw new StoreError(`${this.file} is not a valid forum file (${issues.join('; ')}).`)
    }
    return result.data
  }

  async write(next: ForumFile): Promise<void> {
    const parsed = ForumFileSchema.parse(next)
    await mkdir(dirname(this.file), { recursive: true, mode: 0o700 })
    const temporary = `${this.file}.${process.pid}.tmp`
    const handle = await open(temporary, 'w', 0o600)
    try {
      await handle.writeFile(`${JSON.stringify(parsed, null, 2)}\n`, 'utf8')
      await handle.sync()
    } finally {
      await handle.close()
    }
    await chmod(temporary, 0o600)
    await rename(temporary, this.file)
  }

  /**
   * Which of the stored identities this process is. The key is the MCP client
   * name, so `pi` and `claude` on one machine keep separate names and cursors.
   */
  resolveIdentity(file: ForumFile, clientName?: string): { key: string; identity?: StoredIdentity } {
    const key = agentName({
      env: process.env,
      client: clientName,
      fallback: file.default_identity,
    })
    const identity = file.identities[key]
    if (identity) return { key, identity }
    // One registered identity on the machine is the answer for any client: the
    // usual way to register is the CLI, which has no client name to key on.
    const only = Object.keys(file.identities)
    if (only.length === 1) {
      const stored = file.identities[only[0]!]!
      return { key: only[0]!, identity: stored }
    }
    return { key }
  }

  async register(key: string, identity: StoredIdentity): Promise<ForumFile> {
    const file = await this.read()
    file.identities[key] = identity
    file.default_identity ??= key
    await this.write(file)
    return file
  }

  async bind(binding: Binding): Promise<{ file: ForumFile; added: boolean }> {
    const file = await this.read()
    const key = bindingKey(binding)
    const exists = file.channels.some((entry) => bindingKey(entry) === key)
    if (!exists) {
      file.channels.push(binding)
      await this.write(file)
    }
    return { file, added: !exists }
  }

  async unbind(binding: Pick<Binding, 'workspace' | 'channel'>): Promise<boolean> {
    const file = await this.read()
    const key = bindingKey(binding)
    const kept = file.channels.filter((entry) => bindingKey(entry) !== key)
    if (kept.length === file.channels.length) return false
    file.channels = kept
    await this.write(file)
    return true
  }

  async cursor(identityKey: string, binding: string): Promise<string | undefined> {
    return (await this.read()).cursors[identityKey]?.[binding]
  }

  async setCursor(identityKey: string, binding: string, ts: string): Promise<void> {
    const file = await this.read()
    file.cursors[identityKey] ??= {}
    file.cursors[identityKey][binding] = ts
    await this.write(file)
  }

  async ignore(label: string): Promise<boolean> {
    const file = await this.read()
    if (file.ignored.includes(label)) return false
    file.ignored.push(label)
    await this.write(file)
    return true
  }

  async unignore(label: string): Promise<boolean> {
    const file = await this.read()
    const kept = file.ignored.filter((entry) => entry !== label)
    if (kept.length === file.ignored.length) return false
    file.ignored = kept
    await this.write(file)
    return true
  }

  async postingThisHour(binding: string, when: Date = new Date()): Promise<number> {
    const counter = (await this.read()).posting[binding]
    return counter && counter.hour === hourOf(when) ? counter.count : 0
  }

  async notePost(binding: string, when: Date = new Date()): Promise<number> {
    const file = await this.read()
    const hour = hourOf(when)
    const counter = file.posting[binding]
    const next = counter && counter.hour === hour ? counter.count + 1 : 1
    file.posting[binding] = { hour, count: next }
    await this.write(file)
    return next
  }
}
