/**
 * Files that travel with a normal message. The MCP server runs on the machine the
 * agent runs on, so a path is the whole interface: the caller says which file, this
 * module says whether it may go and what Slack will call it.
 *
 * A send is a dry run until it is confirmed, so the two steps are separate on
 * purpose: `planAttachments` only looks at metadata, and the bytes are read at the
 * last moment. A preview that fails because a path is wrong costs a `stat`, and
 * `read_file`-style downloads never happen by accident.
 *
 * The caps are this tool's own. Slack's plan limit can be lower; when it is, Slack
 * refuses the upload and the caller sees that error instead of a silent truncation.
 */

import { readFile, stat } from 'node:fs/promises'
import { basename, extname, resolve } from 'node:path'

export const MAX_ATTACHMENTS = 10
export const MAX_ATTACHMENT_BYTES = 100 * 1024 * 1024

const MIME_BY_EXTENSION: Record<string, string> = {
  md: 'text/markdown',
  markdown: 'text/markdown',
  txt: 'text/plain',
  log: 'text/plain',
  csv: 'text/csv',
  tsv: 'text/tab-separated-values',
  json: 'application/json',
  jsonl: 'application/x-ndjson',
  yaml: 'application/yaml',
  yml: 'application/yaml',
  toml: 'application/toml',
  xml: 'application/xml',
  html: 'text/html',
  pdf: 'application/pdf',
  png: 'image/png',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  gif: 'image/gif',
  webp: 'image/webp',
  svg: 'image/svg+xml',
  zip: 'application/zip',
  gz: 'application/gzip',
  tar: 'application/x-tar',
}

export function humanSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

/** Only a label: Slack sniffs the type itself, so a wrong guess here misleads nobody. */
export function contentTypeOf(name: string): string {
  const extension = extname(name).slice(1).toLowerCase()
  return MIME_BY_EXTENSION[extension] ?? 'application/octet-stream'
}

export interface AttachmentPlan {
  /** As the caller wrote it, so an error names the path they know. */
  path: string
  /** Slack shows the basename; the directory it came from is not the channel's business. */
  name: string
  bytes: number
  contentType: string
}

export async function planAttachments(
  paths: readonly string[],
): Promise<AttachmentPlan[]> {
  if (paths.length > MAX_ATTACHMENTS) {
    throw new Error(
      `A message carries at most ${MAX_ATTACHMENTS} attachments; ${paths.length} were given.`,
    )
  }

  const plans: AttachmentPlan[] = []
  for (const path of paths) {
    const info = await stat(resolve(path)).catch(() => undefined)
    if (!info) {
      throw new Error(
        `No file at ${path}. Attachments are local paths on the machine that runs this tool.`,
      )
    }
    if (!info.isFile()) throw new Error(`${path} is not a regular file.`)
    if (info.size === 0) throw new Error(`${path} is empty; Slack refuses a zero-byte upload.`)
    if (info.size > MAX_ATTACHMENT_BYTES) {
      throw new Error(
        `${path} is ${humanSize(info.size)}; this tool uploads at most ${humanSize(MAX_ATTACHMENT_BYTES)} per file.`,
      )
    }
    const name = basename(resolve(path))
    plans.push({ path, name, bytes: info.size, contentType: contentTypeOf(name) })
  }
  return plans
}

export interface Attachment {
  name: string
  bytes: Uint8Array<ArrayBuffer>
}

/** Reads the bytes only for a confirmed send, so a preview never loads a 90 MB log. */
export async function readAttachments(
  plans: readonly AttachmentPlan[],
): Promise<Attachment[]> {
  const files: Attachment[] = []
  for (const plan of plans) {
    files.push({ name: plan.name, bytes: new Uint8Array(await readFile(resolve(plan.path))) })
  }
  return files
}

export function describeAttachment(plan: AttachmentPlan): string {
  return `${plan.name} (${plan.contentType}, ${humanSize(plan.bytes)})`
}
