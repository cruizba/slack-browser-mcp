/**
 * Thin Slack Web API client. It authenticates with the xoxc token from the
 * browser session plus its `d` cookie, which is the pair Slack's own web client
 * sends. Nothing here touches the DOM.
 */

import type { SlackAccount } from '../auth/store.ts'
import { AuthError, SlackApiError } from '../errors.ts'
import { redact } from '../log/redact.ts'
import { parsePrintedReference } from './labels.ts'

const API_BASE = 'https://slack.com/api/'

const REAUTH_ERRORS = new Set([
  'invalid_auth',
  'not_authed',
  'token_revoked',
  'token_expired',
  'account_inactive',
])

const RETRYABLE_ERRORS = new Set([
  'ratelimited',
  'internal_error',
  'fatal_error',
  'service_unavailable',
  'request_timeout',
])

interface SlackEnvelope {
  ok: boolean
  error?: string
  detail?: string
  [key: string]: unknown
}

export interface AuthInfo {
  team: string
  team_id: string
  user: string
  user_id: string
  url?: string
}

export interface SlackChannel {
  id: string
  name?: string
  is_private?: boolean
  is_im?: boolean
  is_mpim?: boolean
  is_archived?: boolean
  user?: string
  num_members?: number
  topic?: { value?: string }
}

export interface ResolveChannelOptions {
  /**
   * Maps a person reference (`@ana`, `dm:Ana Ruiz`) to a user id, so a DM can be
   * addressed the way a person names it.
   */
  person?: (reference: string) => string | undefined | Promise<string | undefined>
}

export interface SlackFile {
  id?: string
  name?: string
  title?: string
  mimetype?: string
  size?: number
  url_private?: string
  url_private_download?: string
  shares?: SlackShares
}

export interface SlackUser {
  id: string
  name?: string
  real_name?: string
  display_name?: string
  deleted?: boolean
  is_bot?: boolean
}

export interface SlackUsergroup {
  id: string
  handle?: string
  name?: string
}

export interface SlackMessage {
  ts: string
  user?: string
  bot_id?: string
  username?: string
  text?: string
  subtype?: string
  thread_ts?: string
  reply_count?: number
  latest_reply?: string
  edited?: { ts?: string; user?: string }
  files?: SlackFile[]
}

export interface HistoryPage {
  messages: SlackMessage[]
  has_more: boolean
  next_cursor?: string
}

export interface SlackSearchMatch {
  ts: string
  text?: string
  user?: string
  username?: string
  thread_ts?: string
  permalink?: string
  channel?: { id?: string; name?: string }
}

export interface SearchPage {
  total: number
  matches: SlackSearchMatch[]
}

export interface PostedMessage {
  ts: string
  channel: string
  text?: string
}

export interface UploadAttachment {
  name: string
  /** Backed by a plain `ArrayBuffer`: a `FormData` part cannot wrap a shared one. */
  bytes: Uint8Array<ArrayBuffer>
}

/**
 * An upload is a message like any other, except that Slack reports its `ts` inside
 * the file it created. It is not promised: an upload that landed somewhere Slack
 * does not describe comes back without one instead of failing after the fact.
 */
export interface UploadedMessage {
  ts?: string
  fileIds: string[]
}

export interface ApiClientOptions {
  fetch?: typeof fetch
  sleep?: (ms: number) => Promise<void>
  maxRetries?: number
  /**
   * Sustained gap between requests. The first `burst` calls go out immediately and
   * one token comes back every `paceMs`, so a bulk sync spreads over a minute
   * instead of emptying itself in seconds. 0 disables pacing.
   */
  paceMs?: number
  /** Calls allowed back-to-back before `paceMs` applies, so interactive tools stay fast. */
  burst?: number
}

const DEFAULT_PACE_MS = 500
const DEFAULT_BURST = 4
const MAX_NAME_LOOKUPS = 25

function defaultPaceMs(): number {
  const raw = process.env.SLACK_BROWSER_PACE_MS
  if (raw === undefined || raw === '') return DEFAULT_PACE_MS
  const parsed = Number(raw)
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : DEFAULT_PACE_MS
}

/** ±20%, so two machines syncing on the same schedule do not stay in lockstep. */
function paceJitter(intervalMs: number): number {
  return Math.round((Math.random() * 2 - 1) * intervalMs * 0.2)
}

function backoffMs(attempt: number): number {
  return Math.min(4000, 400 * 2 ** attempt)
}

function retryAfterMs(response: Response): number | undefined {
  const header = response.headers.get('retry-after')
  if (!header) return undefined
  const seconds = Number(header)
  return Number.isFinite(seconds) ? seconds * 1000 : undefined
}

/** Slack files the share under `public`, `private` or `channel`, keyed by channel id. */
type SlackShares = Record<string, Record<string, Array<{ ts?: string }>> | undefined>

function sharedTs(shares: SlackShares | undefined): string | undefined {
  for (const scope of Object.values(shares ?? {})) {
    for (const entries of Object.values(scope ?? {})) {
      const ts = entries?.[0]?.ts
      if (ts) return ts
    }
  }
  return undefined
}

export class SlackClient {
  private readonly account: SlackAccount
  private readonly fetchImpl: typeof fetch
  private readonly sleep: (ms: number) => Promise<void>
  private readonly maxRetries: number
  private readonly paceMs: number
  private readonly burst: number
  private tokens: number
  private refilledAt = Date.now()
  private readonly userNames = new Map<string, string>()
  private channels?: SlackChannel[]

  constructor(account: SlackAccount, options: ApiClientOptions = {}) {
    this.account = account
    this.fetchImpl = options.fetch ?? globalThis.fetch
    this.sleep = options.sleep ?? ((ms) => new Promise((resolve) => setTimeout(resolve, ms)))
    this.maxRetries = options.maxRetries ?? 3
    this.paceMs = options.paceMs ?? defaultPaceMs()
    this.burst = Math.max(1, options.burst ?? DEFAULT_BURST)
    this.tokens = this.burst
  }

  /**
   * Token bucket in front of every request. Slack documents its read limits as
   * "50+ per minute", so a bulk sync pacing at one call every 500 ms stays under
   * that while the bucket's burst keeps single-call tools instant.
   */
  private async pace(): Promise<void> {
    if (this.paceMs <= 0) return
    const now = Date.now()
    const gained = Math.floor((now - this.refilledAt) / this.paceMs)
    if (gained > 0) {
      this.tokens = Math.min(this.burst, this.tokens + gained)
      this.refilledAt = this.tokens >= this.burst ? now : this.refilledAt + gained * this.paceMs
    }
    if (this.tokens > 0) {
      this.tokens -= 1
      return
    }
    const waitMs = Math.max(0, this.refilledAt + this.paceMs - now) + paceJitter(this.paceMs)
    // Clamped: a backward clock jump would otherwise turn the wait into a hang.
    await this.sleep(Math.max(0, Math.min(waitMs, this.paceMs * 4)))
    this.refilledAt = Date.now()
    this.tokens = 0
  }

  get workspaceName(): string {
    return this.account.team_name
  }

  async call<T extends SlackEnvelope>(
    method: string,
    params: Record<string, string | number | boolean | undefined> = {},
  ): Promise<T> {
    const form = new URLSearchParams({ token: this.account.token })
    for (const [key, value] of Object.entries(params)) {
      if (value !== undefined) form.set(key, String(value))
    }
    const cookieName = this.account.d_cookie_name ?? 'd'

    for (let attempt = 0; ; attempt += 1) {
      await this.pace()
      const response = await this.fetchImpl(`${API_BASE}${method}`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/x-www-form-urlencoded',
          Accept: 'application/json',
          Cookie: `${cookieName}=${this.account.d_cookie}`,
        },
        body: form.toString(),
      })

      if (response.status === 429 || response.status === 503) {
        const waitMs = retryAfterMs(response) ?? backoffMs(attempt)
        if (attempt >= this.maxRetries) {
          throw new SlackApiError(method, `http_${response.status}`, 'gave up after retries')
        }
        await this.sleep(waitMs)
        continue
      }

      const body = await response.text()
      let payload: SlackEnvelope
      try {
        payload = JSON.parse(body) as SlackEnvelope
      } catch {
        throw new SlackApiError(
          method,
          `invalid_json_${response.status}`,
          redact(body.slice(0, 160)),
        )
      }

      if (payload.ok) return payload as T

      const code = payload.error ?? 'unknown_error'
      if (REAUTH_ERRORS.has(code)) {
        throw new AuthError(
          `Slack rejected the stored session for ${this.account.team_name} (${code}).`,
        )
      }
      if (RETRYABLE_ERRORS.has(code) && attempt < this.maxRetries) {
        await this.sleep(backoffMs(attempt))
        continue
      }
      throw new SlackApiError(method, code, payload.detail)
    }
  }

  async authTest(): Promise<AuthInfo> {
    const payload = await this.call<SlackEnvelope & AuthInfo>('auth.test')
    return {
      team: payload.team,
      team_id: payload.team_id,
      user: payload.user,
      user_id: payload.user_id,
      ...(typeof payload.url === 'string' ? { url: payload.url } : {}),
    }
  }

  async listMyChannels(): Promise<SlackChannel[]> {
    if (this.channels) return this.channels

    const collected: SlackChannel[] = []
    let cursor: string | undefined
    for (let page = 0; page < 20; page += 1) {
      const payload = await this.call<
        SlackEnvelope & { channels?: SlackChannel[]; response_metadata?: { next_cursor?: string } }
      >('users.conversations', {
        types: 'public_channel,private_channel,mpim,im',
        exclude_archived: true,
        limit: 200,
        ...(cursor ? { cursor } : {}),
      })
      collected.push(...(payload.channels ?? []))
      cursor = payload.response_metadata?.next_cursor || undefined
      if (!cursor) break
    }

    this.channels = collected
    return collected
  }

  /** IMs and group DMs, which a channel listing never returns. */
  async listDmChannels(): Promise<SlackChannel[]> {
    const channels = await this.listMyChannels()
    return channels.filter((channel) => channel.is_im || channel.is_mpim)
  }

  async resolveChannel(
    reference: string,
    options: ResolveChannelOptions = {},
  ): Promise<SlackChannel> {
    const printed = parsePrintedReference(reference)
    // No name: a DM has none, and filing one would put an id in channel_name.
    if (printed.id) return { id: printed.id }
    const raw = printed.reference.replace(/^#/, '')
    if (/^[CGD][A-Z0-9]{6,}$/.test(raw)) return { id: raw }
    if (['me', '@me', 'self', 'dm:self'].includes(raw.toLowerCase())) return this.openSelfDm()

    const channels = await this.listMyChannels()
    const lowered = raw.toLowerCase()
    const named = channels.filter((channel) => channel.name)
    const exact = named.filter((channel) => channel.name!.toLowerCase() === lowered)
    const partial = exact.length
      ? exact
      : named.filter((channel) => channel.name!.toLowerCase().includes(lowered))

    if (partial.length === 1) return partial[0]!
    if (partial.length === 0) {
      const person = await this.resolvePerson(reference, channels, options)
      if (person) return person
      throw new Error(
        `No conversation named "${raw}" in ${this.account.team_name}. ` +
          'Use list_conversations to see the available names.',
      )
    }
    const candidates = partial
      .slice(0, 8)
      .map((channel) => `${channel.is_private ? 'private:' : '#'}${channel.name} (${channel.id})`)
      .join(', ')
    throw new Error(`"${raw}" matches several conversations: ${candidates}.`)
  }

  /**
   * A DM has no name, so the only way to name one is the person it is with. The
   * client holds no directory, so the caller supplies the lookup; without it a
   * person reference fails like any unknown channel.
   */
  private async resolvePerson(
    reference: string,
    channels: SlackChannel[],
    options: ResolveChannelOptions,
  ): Promise<SlackChannel | undefined> {
    const lookup = options.person
    if (!lookup) return undefined
    const name = reference.trim().replace(/^#/, '').replace(/^(dm:|@)/, '')
    if (!name) return undefined
    const userId = await lookup(name)
    if (!userId) return undefined
    const direct = channels.find((channel) => channel.is_im && channel.user === userId)
    if (direct) return direct
    // Closed DMs can be missing from the listing; `conversations.open` would
    // recreate one, and reading must not write.
    throw new Error(
      `No open direct message with ${name} in ${this.account.team_name}. ` +
        'list_conversations kind=dm shows the ones you have.',
    )
  }

  async history(params: {
    channel: string
    limit?: number
    oldest?: string
    latest?: string
    cursor?: string
  }): Promise<HistoryPage> {
    const payload = await this.call<
      SlackEnvelope & {
        messages?: SlackMessage[]
        has_more?: boolean
        response_metadata?: { next_cursor?: string }
      }
    >('conversations.history', {
      channel: params.channel,
      limit: params.limit ?? 20,
      ...(params.oldest ? { oldest: params.oldest } : {}),
      ...(params.latest ? { latest: params.latest } : {}),
      ...(params.cursor ? { cursor: params.cursor } : {}),
    })
    const next = payload.response_metadata?.next_cursor || undefined
    return {
      messages: payload.messages ?? [],
      has_more: Boolean(payload.has_more),
      ...(next ? { next_cursor: next } : {}),
    }
  }

  async replies(params: {
    channel: string
    thread_ts: string
    limit?: number
  }): Promise<SlackMessage[]> {
    const payload = await this.call<SlackEnvelope & { messages?: SlackMessage[] }>(
      'conversations.replies',
      {
        channel: params.channel,
        ts: params.thread_ts,
        limit: params.limit ?? 100,
      },
    )
    return payload.messages ?? []
  }

  /**
   * Slack's own search, for what the local index cannot hold: a message older
   * than the backfill window, or a channel this machine never synced.
   */
  async searchMessages(params: {
    query: string
    count?: number
    page?: number
  }): Promise<SearchPage> {
    const payload = await this.call<
      SlackEnvelope & { messages?: { total?: number; matches?: SlackSearchMatch[] } }
    >('search.messages', {
      query: params.query,
      count: params.count ?? 20,
      ...(params.page && params.page > 1 ? { page: params.page } : {}),
    })
    return {
      total: payload.messages?.total ?? 0,
      matches: payload.messages?.matches ?? [],
    }
  }

  async postMessage(params: {
    channel: string
    text: string
    thread_ts?: string
    blocks?: readonly unknown[] | undefined
  }): Promise<PostedMessage> {
    const payload = await this.call<
      SlackEnvelope & { ts: string; channel: string; message?: { text?: string } }
    >('chat.postMessage', {
      channel: params.channel,
      text: params.text,
      ...(params.thread_ts ? { thread_ts: params.thread_ts } : {}),
      ...(params.blocks ? { blocks: JSON.stringify(params.blocks) } : {}),
    })
    return {
      ts: payload.ts,
      channel: payload.channel,
      ...(payload.message?.text ? { text: payload.message.text } : {}),
    }
  }

  /**
   * Files travel the documented three-step upload: a pre-signed URL, the bytes
   * straight to it, and one call that turns them into a message. Slack builds the
   * message from the upload, so the comment and the files share one `ts`; posting
   * first and uploading after would be two messages.
   *
   * The pre-signed URL is posted to without credentials: Slack signs it, so the
   * `xoxc` token and the `d` cookie never leave `slack.com/api`.
   */
  async uploadFiles(params: {
    channel: string
    files: readonly UploadAttachment[]
    text?: string
    thread_ts?: string
  }): Promise<UploadedMessage> {
    const ids: Array<{ id: string; title: string }> = []
    for (const file of params.files) {
      const target = await this.call<SlackEnvelope & { upload_url: string; file_id: string }>(
        'files.getUploadURLExternal',
        { filename: file.name, length: file.bytes.byteLength },
      )
      await this.putBytes(target.upload_url, file)
      ids.push({ id: target.file_id, title: file.name })
    }

    const payload = await this.call<
      SlackEnvelope & { files?: Array<{ id?: string; shares?: SlackShares }> }
    >('files.completeUploadExternal', {
      files: JSON.stringify(ids),
      channel_id: params.channel,
      ...(params.text ? { initial_comment: params.text } : {}),
      ...(params.thread_ts ? { thread_ts: params.thread_ts } : {}),
    })

    const uploaded = payload.files ?? []
    const fileIds = uploaded
      .map((file) => file.id)
      .filter((id): id is string => typeof id === 'string')
    // `completeUploadExternal` describes where the upload landed but is not obliged
    // to: when the shares come back empty, `files.info` still reports the message
    // the upload created, and a caller that needs the `ts` (a reply in the same
    // thread) would otherwise have to guess it.
    const ts =
      uploaded.map((file) => sharedTs(file.shares)).find((value) => value !== undefined) ??
      (await this.sharedTsFromInfo(fileIds))
    return { ...(ts ? { ts } : {}), fileIds }
  }

  /**
   * Best effort, and worth waiting for: Slack files the share a beat after the upload
   * completes, so a single `files.info` right away can answer "no shares yet" for a
   * message that is already in the channel. Three looks with a short wait cover it
   * (about a second in total); an upload that never reports one is still a sent
   * message, so this returns nothing rather than throwing.
   */
  private async sharedTsFromInfo(fileIds: readonly string[]): Promise<string | undefined> {
    const waits = [120, 350, 800]
    for (let attempt = 0; ; attempt += 1) {
      for (const fileId of fileIds) {
        try {
          const ts = sharedTs((await this.fileInfo(fileId)).shares)
          if (ts) return ts
        } catch {
          // A `files.info` that refuses will refuse again in a moment; the caller
          // already has the file ids and handles a missing ts.
        }
      }
      if (attempt >= waits.length) return undefined
      await this.sleep(waits[attempt]!)
    }
  }

  private async putBytes(url: string, file: UploadAttachment): Promise<void> {
    const form = new FormData()
    form.append('filename', file.name)
    form.append('file', new Blob([file.bytes]), file.name)
    const response = await this.fetchImpl(url, { method: 'POST', body: form })
    if (!response.ok) {
      throw new SlackApiError('files.upload', `http_${response.status}`, file.name)
    }
  }

  private async openSelfDm(): Promise<SlackChannel> {
    const userId = this.account.user_id
    if (!userId) {
      throw new Error(
        `The stored session for ${this.account.team_name} has no user id; re-run \`slack-browser login\`.`,
      )
    }
    const payload = await this.call<SlackEnvelope & { channel?: { id?: string } }>(
      'conversations.open',
      { users: userId },
    )
    const id = payload.channel?.id
    if (!id) throw new Error('conversations.open returned no channel id')
    return { id, name: 'self-dm', is_im: true, user: userId }
  }

  async listUsers(): Promise<SlackUser[]> {
    const collected: SlackUser[] = []
    let cursor: string | undefined
    for (let page = 0; page < 20; page += 1) {
      const payload = await this.call<
        SlackEnvelope & {
          members?: Array<SlackUser & { profile?: { display_name?: string } }>
          response_metadata?: { next_cursor?: string }
        }
      >('users.list', { limit: 200, ...(cursor ? { cursor } : {}) })
      for (const member of payload.members ?? []) {
        collected.push({
          id: member.id,
          ...(member.name ? { name: member.name } : {}),
          ...(member.real_name ? { real_name: member.real_name } : {}),
          ...(member.profile?.display_name
            ? { display_name: member.profile.display_name }
            : {}),
          ...(member.deleted !== undefined ? { deleted: member.deleted } : {}),
          ...(member.is_bot !== undefined ? { is_bot: member.is_bot } : {}),
        })
      }
      cursor = payload.response_metadata?.next_cursor || undefined
      if (!cursor) break
    }
    return collected
  }

  async listAllChannels(): Promise<SlackChannel[]> {
    const collected: SlackChannel[] = []
    let cursor: string | undefined
    for (let page = 0; page < 20; page += 1) {
      const payload = await this.call<
        SlackEnvelope & { channels?: SlackChannel[]; response_metadata?: { next_cursor?: string } }
      >('conversations.list', {
        types: 'public_channel,private_channel',
        exclude_archived: true,
        limit: 200,
        ...(cursor ? { cursor } : {}),
      })
      collected.push(...(payload.channels ?? []))
      cursor = payload.response_metadata?.next_cursor || undefined
      if (!cursor) break
    }
    return collected
  }

  async listUsergroups(): Promise<SlackUsergroup[]> {
    const payload = await this.call<SlackEnvelope & { usergroups?: SlackUsergroup[] }>(
      'usergroups.list',
    )
    return payload.usergroups ?? []
  }

  async fileInfo(fileId: string): Promise<SlackFile> {
    const payload = await this.call<SlackEnvelope & { file?: SlackFile }>('files.info', {
      file: fileId,
    })
    if (!payload.file) throw new Error(`files.info returned no file for ${fileId}`)
    return payload.file
  }

  /**
   * The bytes of a file a message carries. History gives the URL in the common case;
   * when it does not, `files.info` does, which is why the id is enough to ask.
   */
  async downloadFile(file: SlackFile): Promise<Uint8Array> {
    const resolved =
      file.url_private ?? file.url_private_download
        ? file
        : await this.fileInfo(file.id ?? '')
    const url = resolved.url_private ?? resolved.url_private_download
    if (!url) throw new Error(`No download URL for file ${file.id ?? resolved.name ?? 'unknown'}`)
    return (await this.fetchAttachment(url)).bytes
  }

  /**
   * Attachment bytes need the same credentials, so they are fetched here rather
   * than by a caller holding only a URL. Slack redirects between its own hosts,
   * which keeps the `Authorization` header on the request.
   */
  async fetchAttachment(url: string): Promise<{ bytes: Uint8Array; contentType?: string }> {
    const cookieName = this.account.d_cookie_name ?? 'd'
    const response = await this.fetchImpl(url, {
      headers: {
        Authorization: `Bearer ${this.account.token}`,
        Cookie: `${cookieName}=${this.account.d_cookie}`,
      },
    })
    if (!response.ok) {
      throw new SlackApiError('files.download', `http_${response.status}`)
    }
    const bytes = new Uint8Array(await response.arrayBuffer())
    const contentType = response.headers.get('content-type') ?? undefined
    return contentType ? { bytes, contentType } : { bytes }
  }

  async namesFor(
    messages: SlackMessage[],
    known?: (userId: string) => string | undefined,
  ): Promise<Map<string, string>> {
    const names = new Map<string, string>()
    let lookups = 0
    for (const message of messages) {
      if (!message.user || names.has(message.user)) continue
      const resolved = known?.(message.user)
      if (resolved) {
        names.set(message.user, resolved)
        continue
      }
      // Without a directory every author costs a `users.info`, and a 200-message
      // page would turn one read into 200 requests. Past the cap the id is shown.
      if (lookups >= MAX_NAME_LOOKUPS) continue
      lookups += 1
      names.set(message.user, await this.displayName(message.user))
    }
    return names
  }

  async displayName(userId: string | undefined): Promise<string> {
    if (!userId) return 'unknown'
    const cached = this.userNames.get(userId)
    if (cached) return cached
    try {
      const payload = await this.call<
        SlackEnvelope & { user?: { real_name?: string; name?: string; profile?: { display_name?: string } } }
      >('users.info', { user: userId })
      const user = payload.user
      const name = user?.profile?.display_name || user?.real_name || user?.name || userId
      this.userNames.set(userId, name)
      return name
    } catch {
      return userId
    }
  }
}
