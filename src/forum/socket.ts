/**
 * MCP over a Unix domain socket.
 *
 * A socket has the same problem as a pipe: it carries bytes, not messages, so the
 * transport has to invent boundaries. Using the framing the stdio transport
 * already uses — one JSON object per line — keeps the proxy between an agent and
 * the daemon a line relay instead of a second protocol with its own rules about
 * what a message is.
 *
 * The socket is the daemon's only door, so its permissions are the whole access
 * policy: a directory only the user can enter and a socket file only the user can
 * connect to. Anything in this process that can write the archive is reachable
 * through it, which is why there is no token on top.
 */

import { chmod, mkdir, rm } from 'node:fs/promises'
import { connect, createServer, type Server, type Socket } from 'node:net'
import { dirname } from 'node:path'

import type { Transport, TransportSendOptions } from '@modelcontextprotocol/sdk/shared/transport.js'
import type { JSONRPCMessage } from '@modelcontextprotocol/sdk/types.js'

/**
 * A single message may carry a whole task body, and the forum refuses anything
 * over 1 MiB. The ceiling here is generous on purpose: it exists to stop a broken
 * peer from growing this buffer without bound, not to enforce the forum's limits.
 */
const MAX_BUFFER_BYTES = 16 * 1024 * 1024

export class SocketTransport implements Transport {
  onmessage?: (<T extends JSONRPCMessage>(message: T) => void) | undefined
  onclose?: (() => void) | undefined
  onerror?: ((error: Error) => void) | undefined
  sessionId?: string | undefined

  private buffer = ''
  private started = false
  private firstMessage = true
  private readonly socket: Socket
  private readonly onFirstMessage: ((message: JSONRPCMessage) => void) | undefined

  constructor(socket: Socket, options: { onFirstMessage?: (message: JSONRPCMessage) => void } = {}) {
    this.socket = socket
    this.onFirstMessage = options.onFirstMessage
  }

  async start(): Promise<void> {
    if (this.started) return
    this.started = true
    this.socket.setEncoding('utf8')
    this.socket.on('data', (chunk: string) => this.onData(chunk))
    this.socket.on('error', (error: Error) => this.onerror?.(error))
    this.socket.on('close', () => this.onclose?.())
  }

  async send(message: JSONRPCMessage, _options?: TransportSendOptions): Promise<void> {
    if (this.socket.destroyed) throw new Error('the forum socket is closed')
    await new Promise<void>((resolve, reject) => {
      this.socket.write(`${JSON.stringify(message)}\n`, (error) => {
        if (error) reject(error)
        else resolve()
      })
    })
  }

  async close(): Promise<void> {
    this.socket.end()
  }

  private onData(chunk: string): void {
    this.buffer += chunk
    if (this.buffer.length > MAX_BUFFER_BYTES) {
      this.onerror?.(new Error('a forum socket message exceeded the buffer limit'))
      this.socket.destroy()
      return
    }
    let index = this.buffer.indexOf('\n')
    while (index >= 0) {
      const line = this.buffer.slice(0, index)
      this.buffer = this.buffer.slice(index + 1)
      index = this.buffer.indexOf('\n')
      if (line.trim() === '') continue
      try {
        const message = JSON.parse(line) as JSONRPCMessage
        // `initialize` is always the first message, and it carries the client's
        // name. Reading it here is what makes a peer addressable before its first
        // tool call, instead of one notification round-trip later.
        if (this.firstMessage) {
          this.firstMessage = false
          this.onFirstMessage?.(message)
        }
        this.onmessage?.(message)
      } catch (error) {
        this.onerror?.(error instanceof Error ? error : new Error(String(error)))
      }
    }
  }
}

/**
 * Unix sockets live in a fixed-size address field, and a deep temporary directory
 * can overflow it with an error that says nothing about why.
 */
export function checkSocketPath(path: string): void {
  if (path.length > 100) {
    throw new Error(
      `the forum socket path is too long for a Unix address (${path.length} bytes): ${path}. ` +
        'Set SLACK_BROWSER_MCP_DAEMON_DIR to a shorter directory.',
    )
  }
}

export async function listenOnSocket(
  path: string,
  onConnection: (socket: Socket) => void,
): Promise<Server> {
  checkSocketPath(path)
  await mkdir(dirname(path), { recursive: true, mode: 0o700 })
  // A file left behind by a daemon that was killed is not a listener, and bind
  // refuses to reuse it. A live one must not be removed, which is why this asks
  // the path rather than trusting the caller's check from a moment ago: two
  // clients starting a daemon at once race here.
  if (await socketAnswers(path, 250)) {
    throw new Error(`something is already listening on ${path}`)
  }
  await rm(path, { force: true })

  const server = createServer((socket) => {
    socket.setNoDelay(true)
    onConnection(socket)
  })

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(path, () => {
      server.off('error', reject)
      resolve()
    })
  })
  await chmod(path, 0o600)
  return server
}

export async function connectSocket(path: string, timeoutMs = 2_000): Promise<Socket> {
  checkSocketPath(path)
  return new Promise<Socket>((resolve, reject) => {
    const socket = connect(path)
    const timer = setTimeout(() => {
      socket.destroy()
      reject(new Error(`timed out connecting to the forum socket at ${path}`))
    }, timeoutMs)
    socket.once('connect', () => {
      clearTimeout(timer)
      socket.off('error', onError)
      socket.setNoDelay(true)
      resolve(socket)
    })
    const onError = (error: Error): void => {
      clearTimeout(timer)
      socket.destroy()
      reject(error)
    }
    socket.once('error', onError)
  })
}

export async function socketAnswers(path: string, timeoutMs = 1_000): Promise<boolean> {
  try {
    const socket = await connectSocket(path, timeoutMs)
    socket.destroy()
    return true
  } catch {
    return false
  }
}
