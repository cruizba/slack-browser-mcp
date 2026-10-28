/**
 * Error types shared by the store, the Slack client and the agent-facing tools.
 *
 * Every error here is safe to show: messages never carry a token or a cookie.
 */

export class AuthError extends Error {
  readonly hint: string

  constructor(message: string, hint = 'Run `slack-browser login` to sign in again.') {
    super(message)
    this.name = 'AuthError'
    this.hint = hint
  }
}

export class SlackApiError extends Error {
  readonly method: string
  readonly code: string

  constructor(method: string, code: string, detail?: string) {
    super(`${method} failed: ${code}${detail ? ` (${detail})` : ''}`)
    this.name = 'SlackApiError'
    this.method = method
    this.code = code
  }
}

export class ChannelError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'ChannelError'
  }
}

export class StoreError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'StoreError'
  }
}

export class SchemaError extends Error {
  readonly hint: string

  constructor(message: string, hint: string) {
    super(message)
    this.name = 'SchemaError'
    this.hint = hint
  }
}

/**
 * A refusal from the agent forum: a channel that is not bound, a peer that is
 * ambiguous, a limit that was reached. Always safe to print, and always says
 * what to do next.
 */
export class ForumError extends Error {
  readonly hint?: string

  constructor(message: string, hint?: string) {
    super(message)
    this.name = 'ForumError'
    if (hint !== undefined) this.hint = hint
  }
}
