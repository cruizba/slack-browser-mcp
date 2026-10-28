/**
 * What the user decided about a task, on this machine, kept next to the channel
 * instead of inside it.
 *
 * The channel already says what was posted; this file says who approved it. The
 * two are deliberately separate, because they can disagree — approved locally and
 * never announced, or announced and never approved — and that disagreement is the
 * single most useful thing a human auditing a forum can see.
 *
 * Append-only, last line wins, a corrupt final line ignored: the same contract as
 * `audit.jsonl`, for the same reason.
 */

import { appendFile, chmod, mkdir, readFile } from 'node:fs/promises'
import { join } from 'node:path'

import { forumDir } from '../config/paths.ts'

export type DecisionName = 'posted' | 'approved' | 'rejected' | 'reported' | 'acknowledged'
export type DecidedBy = 'cli' | 'agent'

export interface Decision {
  at: string
  task: string
  decision: DecisionName
  by: DecidedBy
  self_approved?: boolean
  note?: string
}

export interface DecisionInput {
  task: string
  decision: DecisionName
  by: DecidedBy
  self_approved?: boolean
  note?: string
}

/**
 * `self_approved` records that an agent, not a terminal, took the decision. It
 * prevents nothing: it makes the pattern visible in `agent-forum inbox` and in the
 * audit, which is all a local gate can honestly promise.
 */
export function selfApproved(input: DecisionInput): boolean {
  return input.by === 'agent'
}

export class DecisionLog {
  private readonly root: string

  constructor(root: string = forumDir()) {
    this.root = root
  }

  file(teamId: string, channelId: string): string {
    if (!/^[A-Za-z0-9_-]{1,64}$/.test(teamId)) throw new Error(`Invalid team id: ${teamId}`)
    if (!/^[A-Za-z0-9_-]{1,64}$/.test(channelId)) throw new Error(`Invalid channel id: ${channelId}`)
    return join(this.root, teamId, `${channelId}.jsonl`)
  }

  async record(teamId: string, channelId: string, input: DecisionInput): Promise<string> {
    const file = this.file(teamId, channelId)
    await mkdir(join(this.root, teamId), { recursive: true, mode: 0o700 })
    const entry: Decision = {
      at: new Date().toISOString(),
      task: input.task,
      decision: input.decision,
      by: input.by,
      ...(input.by === 'agent' ? { self_approved: true } : {}),
      ...(input.note ? { note: input.note } : {}),
    }
    await appendFile(file, `${JSON.stringify(entry)}\n`, { encoding: 'utf8', mode: 0o600 })
    await chmod(file, 0o600)
    return file
  }

  async read(teamId: string, channelId: string): Promise<Map<string, Decision>> {
    let raw: string
    try {
      raw = await readFile(this.file(teamId, channelId), 'utf8')
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return new Map()
      throw error
    }
    const byTask = new Map<string, Decision>()
    for (const line of raw.split('\n')) {
      if (line.trim() === '') continue
      try {
        const parsed = JSON.parse(line) as Decision
        if (parsed.task && parsed.decision) byTask.set(parsed.task, parsed)
      } catch {
        // A half-written last line is not worth failing a read over.
      }
    }
    return byTask
  }
}
