/**
 * Preferences that are not credentials: today, which workspace answers a call
 * that carries no explicit reference.
 *
 * The file is plain JSON so a user can edit it by hand; the shape is still
 * validated on read, because a default pointing at a workspace that no longer
 * exists is an error worth naming.
 */

import { chmod, mkdir, open, readFile, rename } from 'node:fs/promises'
import { dirname } from 'node:path'
import { z } from 'zod'

import { StoreError } from '../errors.ts'
import { settingsPath } from './paths.ts'

const SettingsSchema = z
  .object({
    default_workspace: z.string().trim().min(1).max(128).optional(),
  })
  .strict()

export type Settings = z.infer<typeof SettingsSchema>

export interface SettingsStore {
  read(): Promise<Settings>
  write(settings: Settings): Promise<void>
  /** Stores a team id, or removes the default when given null. */
  setDefaultWorkspace(reference: string | null): Promise<Settings>
}

export class FileSettingsStore implements SettingsStore {
  private readonly file: string

  constructor(file: string = settingsPath()) {
    this.file = file
  }

  async read(): Promise<Settings> {
    let raw: string
    try {
      raw = await readFile(this.file, 'utf8')
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return {}
      throw error
    }
    if (raw.trim() === '') return {}

    let parsed: unknown
    try {
      parsed = JSON.parse(raw)
    } catch {
      throw new StoreError(`${this.file} is not valid JSON. Fix it or delete it.`)
    }

    const result = SettingsSchema.safeParse(parsed)
    if (!result.success) {
      const issues = result.error.issues.map((issue) => `${issue.path.join('.')}: ${issue.message}`)
      throw new StoreError(`${this.file} is not a valid settings file (${issues.join('; ')}).`)
    }
    return result.data
  }

  async write(settings: Settings): Promise<void> {
    const parsed = SettingsSchema.parse(settings)
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

  async setDefaultWorkspace(reference: string | null): Promise<Settings> {
    const next: Settings = { ...(await this.read()) }
    if (reference) {
      next.default_workspace = reference
    } else {
      delete next.default_workspace
    }
    await this.write(next)
    return next
  }
}

/**
 * First login wins: a default the user chose stays, and a missing one is filled
 * with the workspace that was just stored.
 */
export async function ensureDefaultWorkspace(
  settings: SettingsStore,
  teamId: string,
): Promise<boolean> {
  if ((await settings.read()).default_workspace) return false
  await settings.setDefaultWorkspace(teamId)
  return true
}
