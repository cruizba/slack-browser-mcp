/**
 * The forum MCP entrypoint: a stdio server that is really a relay.
 *
 * The client speaks MCP on stdin/stdout, the daemon speaks it on a socket, and
 * this process only carries the bytes between them. Keeping it dumb is the point:
 * there is one implementation of the forum, in the daemon, so the two surfaces
 * cannot drift.
 *
 * The one thing it rewrites is `clientInfo.name` on `initialize`, because the
 * agent's name is resolved from the environment of the process the client spawned
 * (`SLACK_BROWSER_MCP_AGENT`) and that environment belongs to this process, not to
 * the shared daemon.
 */

import { mkdirSync } from 'node:fs'
import { dirname, } from 'node:path'

import { daemonPaths, ensureDaemon } from '../forum/daemon.ts'
import { connectSocket } from '../forum/socket.ts'

function fail(message: string): never {
  process.stderr.write(`slack-browser forum: ${message}\n`)
  process.exit(1)
}

/**
 * The daemon's own diagnostics are not the agent's business: they would show up in
 * every tools/list and invite a model to call them. They stay reachable from the
 * CLI, which speaks to the socket directly.
 */
const HIDDEN_TOOLS = new Set(['forum_daemon'])

function rewriteInitialize(line: string): string {
  let message: unknown
  try {
    message = JSON.parse(line)
  } catch {
    return line
  }
  if (typeof message !== 'object' || message === null) return line
  const envelope = message as { method?: unknown; params?: Record<string, unknown> }
  if (envelope.method !== 'initialize' || typeof envelope.params !== 'object') return line
  const name = process.env.SLACK_BROWSER_MCP_AGENT?.trim()
  if (!name) return line
  const clientInfo = envelope.params.clientInfo
  envelope.params.clientInfo = {
    ...(typeof clientInfo === 'object' && clientInfo !== null ? clientInfo : {}),
    name,
  }
  return JSON.stringify(envelope)
}

export async function main(): Promise<void> {
  const paths = daemonPaths()
  try {
    mkdirSync(dirname(paths.socket), { recursive: true, mode: 0o700 })
  } catch {
    // The daemon reports the real problem with a better message.
  }
  await ensureDaemon().catch((error: unknown) => {
    fail(error instanceof Error ? error.message : String(error))
  })

  const socket = await connectSocket(paths.socket).catch((error: unknown) => {
    fail(error instanceof Error ? error.message : String(error))
  })

  socket.setEncoding('utf8')
  let inbound = ''
  const flush = (line: string, end: boolean): void => {
    process.stdout.write(filterToolsList(line) + (end ? '\n' : ''))
  }
  socket.on('data', (chunk: string) => {
    inbound += chunk
    let index = inbound.indexOf('\n')
    while (index >= 0) {
      const line = inbound.slice(0, index)
      inbound = inbound.slice(index + 1)
      index = inbound.indexOf('\n')
      flush(line, true)
    }
  })
  socket.on('close', () => {
    if (inbound !== '') flush(inbound, false)
    process.exit(0)
  })
  socket.on('error', (error: Error) => fail(error.message))

  process.stdin.setEncoding('utf8')
  let pending = ''
  process.stdin.on('data', (chunk: string) => {
    pending += chunk
    let index = pending.indexOf('\n')
    while (index >= 0) {
      const line = pending.slice(0, index)
      pending = pending.slice(index + 1)
      index = pending.indexOf('\n')
      if (line.trim() === '') continue
      socket.write(`${rewriteInitialize(line)}\n`)
    }
  })
  process.stdin.on('end', () => {
    socket.end()
  })
}

function filterToolsList(line: string): string {
  if (!line.includes('"tools"')) return line
  let parsed: unknown
  try {
    parsed = JSON.parse(line)
  } catch {
    return line
  }
  const result = (parsed as { result?: { tools?: Array<{ name?: string }> } }).result
  if (!Array.isArray(result?.tools)) return line
  const kept = result.tools.filter((tool) => !HIDDEN_TOOLS.has(tool.name ?? ''))
  if (kept.length === result.tools.length) return line
  result.tools = kept
  return JSON.stringify(parsed)
}

if (process.argv[1] && import.meta.url === new URL(`file://${process.argv[1]}`).href) {
  main().catch((error: unknown) => {
    fail(error instanceof Error ? error.message : String(error))
  })
}
