/**
 * Thin Slack Web API client. It authenticates with the xoxc token from the
 * browser session plus its `d` cookie, which is the pair Slack's own web client
 * sends. Nothing here touches the DOM.
 */

import type { SlackAccount } from '../auth/store.ts'
import { AuthError, SlackApiError } from '../errors.ts'
import { redact } from '../log/redact.ts'

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

export interface PostedMessage {
  ts: string
  channel: string
  text?: string
}

export interface ApiClientOptions {
  fetch?: typeof fetch
  sleep?: (ms: number) => Promise<void>
  maxRetries?: number
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

export class SlackClient {
  private readonly account: SlackAccount
  private readonly fetchImpl: typeof fetch
  private readonly sleep: (ms: number) => Promise<void>
  private readonly maxRetries: number
  private readonly userNames = new Map<string, string>()
  private channels?: SlackChannel[]

  constructor(account: SlackAccount, options: ApiClientOptions = {}) {
    this.account = account
    this.fetchImpl = options.fetch ?? globalThis.fetch
    this.sleep = options.sleep ?? ((ms) => new Promise((resolve) => setTimeout(resolve, ms)))
    this.maxRetries = options.maxRetries ?? 3
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
    const raw = reference.trim().replace(/^#/, '')
    if (/^[CGD][A-Z0-9]{6,}$/.test(raw)) return { id: raw, name: raw }
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

  async postMessage(params: {
    channel: string
    text: string
    thread_ts?: string
  }): Promise<PostedMessage> {
    const payload = await this.call<
      SlackEnvelope & { ts: string; channel: string; message?: { text?: string } }
    >('chat.postMessage', {
      channel: params.channel,
      text: params.text,
      ...(params.thread_ts ? { thread_ts: params.thread_ts } : {}),
    })
    return {
      ts: payload.ts,
      channel: payload.channel,
      ...(payload.message?.text ? { text: payload.message.text } : {}),
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
   * Attachment bytes need the same credentials, so they are fetched here rather
   * than by a caller holding only a URL. Slack redirects between its own hosts,
   * which keeps the `Authorization` header on the request.
   */
  async downloadFile(url: string): Promise<{ bytes: Uint8Array; contentType?: string }> {
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
    for (const message of messages) {
      if (!message.user || names.has(message.user)) continue
      names.set(message.user, known?.(message.user) ?? (await this.displayName(message.user)))
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
