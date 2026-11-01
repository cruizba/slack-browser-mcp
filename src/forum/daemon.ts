/**
 * The forum daemon: one process per machine that owns the archive's writes and
 * serves every local agent over a Unix socket.
 *
 * Why a process instead of the per-session polling the forum used to do: every
 * agent that needed a fresh thread paid for its own sync, and the archive's lock
 * decided which of them won. One process means one writer, one cursor per peer and
 * a delivery path that survives an agent being asleep.
 *
 * Nothing starts it at boot. The first client that needs it starts it, and it
 * leaves after a window with no connections, so a machine that stops using the
 * forum stops talking to Slack on its own.
 */

import { spawn } from 'node:child_process'
import { closeSync, existsSync, openSync } from 'node:fs'
import { mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import type { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js'
import type { Socket } from 'node:net'

import { connectSocket, listenOnSocket, SocketTransport, socketAnswers } from './socket.ts'
import { openForum } from './context.ts'
import { AGENT_ENV, labelOf } from './identity.ts'

export const DAEMON_DIR_ENV = 'SLACK_BROWSER_MCP_DAEMON_DIR'
export const DAEMON_IDLE_ENV = 'SLACK_BROWSER_MCP_DAEMON_IDLE_SECONDS'
export const DAEMON_SOCKET_ENV = 'SLACK_BROWSER_MCP_DAEMON_SOCKET'
export const DEFAULT_IDLE_SECONDS = 600

export interface DaemonPaths {
  dir: string
  socket: string
  pid: string
  /** Where a spawned daemon's stderr goes; a lazy start has no terminal to fail in. */
  log: string
}

export interface LocalMessage {
  from: string
  text: string
  at: string
}

export interface ConnectionHooks {
  /** Called once the connection has resolved a forum identity, with `name@host`. */
  onIdentity(label: string): void
  /** Drains what `forum say` queued for this connection while it was busy. */
  localMessages(): string[]
}

export interface DaemonStatus {
  running: boolean
  socket: string
  pid?: number
  idle_seconds: number
  peers: Array<{ label?: string; client?: string }>
}

export function daemonPaths(env: NodeJS.ProcessEnv = process.env): DaemonPaths {
  const explicit = env[DAEMON_SOCKET_ENV]?.trim()
  const runtime = env.XDG_RUNTIME_DIR?.trim()
  const dir =
    env[DAEMON_DIR_ENV]?.trim() ||
    (runtime ? join(runtime, 'slack-browser-mcp') : join(tmpdir(), `slack-browser-mcp-${process.getuid?.() ?? 0}`))
  return {
    dir,
    socket: explicit || join(dir, 'forum.sock'),
    pid: join(dir, 'forum.pid'),
    log: join(dir, 'daemon.log'),
  }
}

export function idleSeconds(env: NodeJS.ProcessEnv = process.env): number {
  const raw = env[DAEMON_IDLE_ENV]?.trim()
  // `Number('')` is 0, which would turn an unset variable into "exit at once".
  if (!raw) return DEFAULT_IDLE_SECONDS
  const value = Number(raw)
  return Number.isFinite(value) && value >= 0 ? value : DEFAULT_IDLE_SECONDS
}

/** Live connections, keyed by the id the acceptor assigned. Only a serving process fills this. */
const connections = new Map<number, { label?: string; client?: string; queue: LocalMessage[] }>()
let lastActivity = Date.now()
let servingSocket: string | undefined

/** The socket this process listens on, or the one clients would use. */
export function daemonSocketPath(): string {
  return servingSocket ?? daemonPaths().socket
}

async function resolvePeer(clientName: string | undefined): Promise<string | undefined> {
  const context = await openForum(clientName ? { clientName } : {})
  return context.identity ? labelOf(context.identity) : undefined
}

export function noteActivity(): void {
  lastActivity = Date.now()
}

/** The name `callDaemon` connects with; diagnostics sessions are not peers. */
export const INTERNAL_CLIENT = 'slack-browser-daemon-client'

export function connectedPeers(): Array<{ label?: string; client?: string }> {
  return [...connections.values()]
    .filter((state) => state.client !== INTERNAL_CLIENT)
    .map((state) => ({
      ...(state.label ? { label: state.label } : {}),
      ...(state.client ? { client: state.client } : {}),
    }))
}

/** Fills in the labels the connect-time resolution has not finished writing yet. */
export async function settlePeerLabels(): Promise<void> {
  await Promise.all(
    [...connections.values()]
      .filter((state) => state.label === undefined && state.client !== undefined)
      .map(async (state) => {
        const label = await resolvePeer(state.client)
        if (label) state.label = label
      }),
  ).catch(() => undefined)
}

export function idleForMs(): number {
  return Date.now() - lastActivity
}

/**
 * `forum say` reaches only a peer that is already connected to this daemon. A
 * queued message that nobody drains is a promise the tool cannot keep, so refusing
 * is the honest answer: the user then posts in Slack, where the record is real.
 */
export function deliverSay(to: string, from: string, text: string): boolean {
  const at = new Date().toISOString()
  let delivered = false
  for (const state of connections.values()) {
    if (state.label !== to) continue
    state.queue.push({ from, text, at })
    delivered = true
  }
  noteActivity()
  return delivered
}

export async function readPid(paths: DaemonPaths = daemonPaths()): Promise<number | undefined> {
  const raw = await readFile(paths.pid, 'utf8').catch(() => '')
  const value = Number(raw.trim())
  return Number.isInteger(value) && value > 0 ? value : undefined
}

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

export async function daemonStatus(): Promise<DaemonStatus> {
  const paths = daemonPaths()
  const running = await socketAnswers(paths.socket)
  const pid = await readPid(paths)
  const reported = running ? await callDaemon('forum_daemon', {}, { timeoutMs: 2_000 }).catch(() => undefined) : undefined
  return {
    running,
    socket: paths.socket,
    ...(pid !== undefined && alive(pid) ? { pid } : {}),
    idle_seconds: idleSeconds(),
    peers: reported?.peers ?? [],
  }
}

function cliEntrypoint(): string | undefined {
  const here = dirname(fileURLToPath(import.meta.url))
  const bundled = join(here, '..', 'cli.js')
  const source = join(here, '..', 'cli.ts')
  const explicit = process.env.SLACK_BROWSER_MCP_CLI?.trim()
  if (explicit && existsSync(explicit)) return explicit
  if (existsSync(bundled)) return bundled
  if (existsSync(source)) return source
  return undefined
}

/** Flags that belong to the parent's job (a test runner, a watcher) not to the daemon's. */
function childArgs(entry: string): string[] {
  const inherited = process.execArgv.filter(
    (arg) => !arg.startsWith('--test') && arg !== '--watch' && !arg.startsWith('--watch-'),
  )
  const needsStrip = entry.endsWith('.ts') && !inherited.includes('--experimental-strip-types')
  return [...inherited, ...(needsStrip ? ['--experimental-strip-types'] : []), entry, 'agent-forum', 'daemon', 'serve']
}

async function waitUntilRunning(socket: string, timeoutMs: number): Promise<boolean> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (await socketAnswers(socket, 250)) return true
    await new Promise((done) => setTimeout(done, 50))
  }
  return false
}

export async function ensureDaemon(options: { timeoutMs?: number } = {}): Promise<{ spawned: boolean }> {
  const paths = daemonPaths()
  if (await socketAnswers(paths.socket)) return { spawned: false }

  const entry = cliEntrypoint()
  if (!entry) throw new Error('cannot find the slack-browser CLI to start the forum daemon')

  await mkdir(paths.dir, { recursive: true, mode: 0o700 })
  const log = openSync(paths.log, 'a', 0o600)
  try {
    const child = spawn(process.execPath, childArgs(entry), {
      detached: true,
      stdio: ['ignore', log, log],
      // The agent's name belongs to the client that spawned us, not to the daemon,
      // which serves several of them. Inheriting it would make every connection
      // resolve to the first agent's identity.
      env: withoutAgentName(process.env),
    })
    child.unref()
  } finally {
    closeSync(log)
  }

  if (await waitUntilRunning(paths.socket, options.timeoutMs ?? 8_000)) return { spawned: true }
  throw new Error(
    `the forum daemon did not come up; run \`slack-browser agent-forum daemon serve\` in a terminal, or read ${paths.log}`,
  )
}

function withoutAgentName(env: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  const copy = { ...env }
  delete copy[AGENT_ENV]
  return copy
}

export interface ServeOptions {
  /** Builds the MCP server for one connection. The daemon never assumes a tool set. */
  build(hooks: ConnectionHooks): McpServer
  idleSecondsOverride?: number
  socketPath?: string
  /** Posted once, when the daemon starts, so peers see the machine come up. */
  onStart?: () => Promise<void> | void
  /** Called once the socket is listening, before any connection is served. */
  onListening?: () => Promise<void> | void
  /** Stops the daemon when aborted; the tests use it instead of a signal. */
  signal?: AbortSignal
  /** False keeps the process-wide SIGTERM/SIGINT handlers out of a host process. */
  handleSignals?: boolean
  /** False leaves the process running after shutdown, which is what a test wants. */
  exitProcess?: boolean
}

export async function serveDaemon(options: ServeOptions): Promise<void> {
  // See `withoutAgentName`: identity belongs to the connection, never to the daemon.
  delete process.env[AGENT_ENV]
  const paths = daemonPaths()
  const socketPath = options.socketPath ?? paths.socket
  const idleMs = (options.idleSecondsOverride ?? idleSeconds()) * 1_000

  if (await socketAnswers(socketPath)) {
    throw new Error(`a forum daemon is already serving ${socketPath}`)
  }

  servingSocket = socketPath
  let nextId = 1
  let shuttingDown = false
  const servers = new Set<McpServer>()
  const sockets = new Set<Socket>()

  const server = await listenOnSocket(socketPath, (socket) => {
    const id = nextId++
    const state: { label?: string; client?: string; queue: LocalMessage[] } = { queue: [] }
    connections.set(id, state)
    sockets.add(socket)
    noteActivity()

    const hooks: ConnectionHooks = {
      onIdentity: (label) => {
        state.label = label
        noteActivity()
      },
      localMessages: () => {
        const queued = state.queue.map(
          (message) => `${message.at} ${message.from} (local, not on Slack): ${message.text}`,
        )
        state.queue.length = 0
        return queued
      },
    }

    const mcp = options.build(hooks)
    servers.add(mcp)
    let client: string | undefined
    // The transport's own onclose is taken by the protocol, so cleanup hangs off
    // the socket instead. A peer that closes only its write side leaves this one
    // half-open, which is why `end` ends it rather than waiting for `close`.
    const cleanup = (): void => {
      connections.delete(id)
      servers.delete(mcp)
      sockets.delete(socket)
      noteActivity()
    }
    socket.on('end', () => socket.end())
    socket.on('close', cleanup)
    socket.on('error', cleanup)
    const transport = new SocketTransport(socket, {
      onFirstMessage: (message) => {
        const name = (message as { params?: { clientInfo?: { name?: unknown } } }).params
          ?.clientInfo?.name
        if (typeof name !== 'string') return
        client = name
        state.client = name
        // Addressable from the moment it says who it is, so `forum say` can reach a
        // peer that is connected and has not called a tool yet.
        void resolvePeer(name)
          .then((label) => {
            if (label) state.label = label
          })
          .catch(() => undefined)
      },
    })
    mcp.connect(transport).catch(() => {
      connections.delete(id)
      servers.delete(mcp)
      socket.destroy()
    })
  })

  await mkdir(paths.dir, { recursive: true, mode: 0o700 })
  await writeFile(paths.pid, `${process.pid}\n`, { mode: 0o600 })

  const shutdown = async (reason: string): Promise<void> => {
    if (shuttingDown) return
    shuttingDown = true
    clearInterval(ticker)
    for (const socket of sockets) socket.destroy()
    await Promise.all([...servers].map((mcp) => mcp.close().catch(() => undefined)))
    await new Promise<void>((done) => server.close(() => done()))
    await rm(socketPath, { force: true })
    await rm(paths.pid, { force: true })
    servingSocket = undefined
    if (reason) process.stderr.write(`forum daemon: ${reason}\n`)
  }

  const ticker = setInterval(() => {
    if (connections.size > 0 || Date.now() - lastActivity < idleMs) return
    void shutdown(`no clients for ${Math.round(idleMs / 1000)}s`).then(finish)
  }, 1_000)

  const finish = (): void => {
    if (options.exitProcess === false) return
    process.exit(0)
  }

  if (options.handleSignals !== false) {
    process.once('SIGTERM', () => void shutdown('SIGTERM').then(finish))
    process.once('SIGINT', () => void shutdown('SIGINT').then(finish))
  }
  options.signal?.addEventListener('abort', () => void shutdown('aborted'))

  await options.onStart?.()
  await options.onListening?.()

  // The daemon stays alive on the interval; this promise settles when it stops.
  await new Promise<void>((resolve) => {
    const check = setInterval(() => {
      if (!shuttingDown) return
      clearInterval(check)
      resolve()
    }, 100)
  })
}

export interface DaemonCallResult {
  text: string
  isError: boolean
  peers: Array<{ label?: string; client?: string }>
}

export async function callDaemon(
  tool: string,
  args: Record<string, unknown>,
  options: { socketPath?: string; timeoutMs?: number } = {},
): Promise<DaemonCallResult> {
  const socketPath = options.socketPath ?? daemonPaths().socket
  const socket = await connectSocket(socketPath, options.timeoutMs ?? 2_000)
  const client = new Client({ name: INTERNAL_CLIENT, version: '1' })
  const close = async (): Promise<void> => {
    await client.close().catch(() => undefined)
    socket.destroy()
  }
  try {
    await client.connect(new SocketTransport(socket))
    const result = (await client.callTool({ name: tool, arguments: args })) as {
      content?: Array<{ type: string; text?: string }>
      isError?: boolean
    }
    const text = (result.content ?? []).map((part) => part.text ?? '').join('\n')
    const parsed = text.trim().startsWith('{')
      ? (JSON.parse(text) as { peers?: Array<{ label?: string; client?: string }> })
      : {}
    return { text, isError: result.isError === true, peers: parsed.peers ?? [] }
  } finally {
    await close()
  }
}

export async function startDaemon(): Promise<{ spawned: boolean; pid?: number }> {
  const paths = daemonPaths()
  const { spawned } = await ensureDaemon()
  const pid = await readPid(paths)
  return { spawned, ...(pid !== undefined ? { pid } : {}) }
}

export async function stopDaemon(): Promise<{ stopped: boolean; pid?: number }> {
  const paths = daemonPaths()
  const pid = await readPid(paths)
  if (pid === undefined || !alive(pid)) {
    await rm(paths.socket, { force: true })
    await rm(paths.pid, { force: true })
    return { stopped: false }
  }
  process.kill(pid, 'SIGTERM')
  const deadline = Date.now() + 5_000
  while (Date.now() < deadline) {
    if (!(await socketAnswers(paths.socket, 200))) return { stopped: true, pid }
    await new Promise((done) => setTimeout(done, 50))
  }
  process.kill(pid, 'SIGKILL')
  await rm(paths.socket, { force: true })
  await rm(paths.pid, { force: true })
  return { stopped: true, pid }
}
