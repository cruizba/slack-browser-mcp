/**
 * Sending is the only side effect this tool has on a shared surface, so every
 * successful post leaves a line here: what, where, when, and under which
 * workspace. Only a redacted preview of the text is stored, never the whole
 * message.
 */

import { appendFile, chmod, mkdir } from 'node:fs/promises'
import { dirname } from 'node:path'

import { auditLogPath } from '../config/paths.ts'
import { redact } from '../log/redact.ts'

const PREVIEW_CHARS = 120

export interface SendRecord {
  workspace: string
  channel: string
  channel_id: string
  thread_ts?: string
  /** Absent when Slack reported no ts for an upload, which is rare and not worth a failure. */
  message_ts?: string
  text: string
  /** Names and sizes: the bytes stay where the caller left them. */
  files?: string[]
}

export async function recordSend(record: SendRecord, when: Date = new Date()): Promise<string> {
  const file = auditLogPath()
  await mkdir(dirname(file), { recursive: true, mode: 0o700 })
  const line = JSON.stringify({
    at: when.toISOString(),
    workspace: record.workspace,
    channel: record.channel,
    channel_id: record.channel_id,
    ...(record.thread_ts ? { thread_ts: record.thread_ts } : {}),
    ...(record.message_ts ? { message_ts: record.message_ts } : {}),
    ...(record.files?.length ? { files: record.files } : {}),
    chars: record.text.length,
    preview: redact(record.text).slice(0, PREVIEW_CHARS),
  })
  await appendFile(file, `${line}\n`, { encoding: 'utf8', mode: 0o600 })
  await chmod(file, 0o600)
  return file
}
