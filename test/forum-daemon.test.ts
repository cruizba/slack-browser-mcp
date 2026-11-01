import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { after, test } from 'node:test'

import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js'

const root = await mkdtemp(join(tmpdir(), 'forum-daemon-'))
process.env.SLACK_BROWSER_MCP_CONFIG_DIR = join(root, 'config')
process.env.XDG_DATA_HOME = join(root, 'data')
process.env.SLACK_BROWSER_MCP_DAEMON_DIR = join(root, 'run')
process.env.SLACK_BROWSER_MCP_NO_PRESENCE = '1'
// The daemon serves several agents, so the agent's name must come from each
// connection's client info. A variable in this process would win over all of them.
delete process.env.SLACK_BROWSER_MCP_AGENT

// Force the source entrypoint: a stale build in dist/ would serve the old CLI.
process.env.SLACK_BROWSER_MCP_CLI = new URL('../src/cli.ts', import.meta.url).pathname

const {
  callDaemon,
  daemonPaths,
  daemonStatus,
  DEFAULT_IDLE_SECONDS,
  ensureDaemon,
  idleSeconds,
  INTERNAL_CLIENT,
  serveDaemon,
  stopDaemon,
} = await import('../src/forum/daemon.ts')
const { buildServer } = await import('../src/mcp/server.ts')
const { ForumStore } = await import('../src/forum/binding.ts')
const { singleFlight } = await import('../src/forum/refresh.ts')
const { connectSocket, SocketTransport, socketAnswers } = await import('../src/forum/socket.ts')

after(() => rm(root, { recursive: true, force: true }))

const HOST = 'testbox'

async function seedIdentities(): Promise<void> {
  const forum = new ForumStore()
  for (const name of ['builder', 'reviewer']) {
    await forum.register(name, {
      name,
      host: HOST,
      capabilities: [],
      registered_at: new Date(2026, 0, 1).toISOString(),
    })
  }
}

function textOf(result: unknown): string {
  const content = (result as { content?: Array<{ type: string; text?: string }> }).content ?? []
  return content.map((part) => part.text ?? '').join('\n')
}

async function waitFor(check: () => Promise<boolean>, timeoutMs = 5_000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (await check()) return
    await new Promise((done) => setTimeout(done, 25))
  }
  throw new Error('condition did not become true in time')
}

async function inMemory(surface: 'slack' | 'forum'): Promise<Client> {
  const server = buildServer({ surface, ...(surface === 'forum' ? { daemon: true } : {}) })
  const client = new Client({ name: 'surface-test', version: '0.0.0' })
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair()
  await Promise.all([server.connect(serverTransport), client.connect(clientTransport)])
  return client
}

let seq = 0

async function runDaemon(idle = 30): Promise<{
  socketPath: string
  stop: () => Promise<void>
}> {
  const socketPath = join(root, 'run', `forum-${seq++}.sock`)
  const controller = new AbortController()
  const done = serveDaemon({
    socketPath,
    idleSecondsOverride: idle,
    handleSignals: false,
    exitProcess: false,
    signal: controller.signal,
    build: (hooks) =>
      buildServer({
        surface: 'forum',
        daemon: true,
        onIdentity: hooks.onIdentity,
        localMessages: hooks.localMessages,
      }),
  })
  await waitFor(() => socketAnswers(socketPath, 200))
  return {
    socketPath,
    stop: async () => {
      controller.abort()
      await done
    },
  }
}

async function connectAs(socketPath: string, name: string): Promise<Client> {
  const raw = await connectSocket(socketPath)
  const client = new Client({ name, version: '0.0.0' })
  await client.connect(new SocketTransport(raw))
  return client
}

test('the two surfaces are disjoint, and the daemon adds its own tools', async () => {
  const [slack, forum] = await Promise.all([inMemory('slack'), inMemory('forum')])
  const [slackTools, forumTools] = await Promise.all([slack.listTools(), forum.listTools()])
  const slackNames = slackTools.tools.map((tool) => tool.name)
  const forumNames = forumTools.tools.map((tool) => tool.name)

  assert.ok(slackNames.includes('post_message'))
  assert.ok(slackNames.includes('read_messages'))
  assert.ok(!slackNames.some((name) => name.startsWith('forum_')))
  assert.ok(!slackNames.includes('archive_refresh'))

  assert.ok(forumNames.includes('forum_inbox'))
  assert.ok(forumNames.includes('forum_post'))
  assert.ok(!forumNames.includes('post_message'))
  assert.ok(!forumNames.includes('read_messages'))

  // Only a daemon can deliver locally or own the refresh.
  assert.ok(forumNames.includes('forum_say'))
  assert.ok(forumNames.includes('archive_refresh'))
  assert.ok(forumNames.includes('forum_daemon'))

  const plain = await inMemory('slack')
  await plain.close()
  await Promise.all([slack.close(), forum.close()])
})

test('one daemon serves two peers, each under its own identity', async (t) => {
  await seedIdentities()
  const daemon = await runDaemon()
  t.after(() => daemon.stop())

  const builder = await connectAs(daemon.socketPath, 'builder')
  const reviewer = await connectAs(daemon.socketPath, 'reviewer')
  t.after(async () => {
    await builder.close()
    await reviewer.close()
  })

  const builderStatus = textOf(await builder.callTool({ name: 'forum_status', arguments: {} }))
  const reviewerStatus = textOf(await reviewer.callTool({ name: 'forum_status', arguments: {} }))

  assert.match(builderStatus, new RegExp(`builder@${HOST}`))
  assert.doesNotMatch(builderStatus, new RegExp(`reviewer@${HOST}`))
  assert.match(reviewerStatus, new RegExp(`reviewer@${HOST}`))
  assert.doesNotMatch(reviewerStatus, new RegExp(`builder@${HOST}`))
  assert.match(builderStatus, /daemon: serving this session/)
})

test('forum say reaches a connected peer and refuses an absent one', async (t) => {
  await seedIdentities()
  const daemon = await runDaemon()
  t.after(() => daemon.stop())

  const builder = await connectAs(daemon.socketPath, 'builder')
  const reviewer = await connectAs(daemon.socketPath, 'reviewer')
  t.after(async () => {
    await builder.close()
    await reviewer.close()
  })

  const refused = (await builder.callTool({
    name: 'forum_say',
    arguments: { to: `ghost@${HOST}`, text: 'are you there?' },
  })) as { isError?: boolean }
  assert.equal(refused.isError, true)

  const sent = textOf(
    await builder.callTool({
      name: 'forum_say',
      arguments: { to: `reviewer@${HOST}`, text: 'look at the retry loop' },
    }),
  )
  assert.match(sent, /Delivered to reviewer@testbox locally/)

  // The message is local, so it arrives in the inbox rather than in a thread.
  const inbox = textOf(
    await reviewer.callTool({ name: 'forum_inbox', arguments: { offline: true } }),
  )
  assert.match(inbox, /local, not on Slack/)
  assert.match(inbox, /look at the retry loop/)

  // Draining is what acknowledges it: the next read does not repeat it.
  const again = textOf(
    await reviewer.callTool({ name: 'forum_inbox', arguments: { offline: true } }),
  )
  assert.doesNotMatch(again, /look at the retry loop/)
})

test('the daemon leaves after its idle window with no clients', async () => {
  await seedIdentities()
  const daemon = await runDaemon(0.5)
  const client = await connectAs(daemon.socketPath, 'builder')
  assert.equal(
    await client.callTool({ name: 'forum_status', arguments: {} }).then(() => true),
    true,
  )
  await client.close()
  // Probing with a connection would reset the idle clock, so the check is the
  // socket file, which the daemon removes when it goes.
  await waitFor(async () => !existsSync(daemon.socketPath), 10_000)
})

test('a socket file left behind by a dead daemon is replaced, not trusted', async (t) => {
  await seedIdentities()
  const socketPath = join(root, 'run', 'stale.sock')
  await mkdir(join(root, 'run'), { recursive: true })
  await writeFile(socketPath, 'not a socket\n')

  const controller = new AbortController()
  const done = serveDaemon({
    socketPath,
    handleSignals: false,
    exitProcess: false,
    signal: controller.signal,
    build: (hooks) =>
      buildServer({
        surface: 'forum',
        daemon: true,
        onIdentity: hooks.onIdentity,
        localMessages: hooks.localMessages,
      }),
  })
  t.after(async () => {
    controller.abort()
    await done
  })

  await waitFor(() => socketAnswers(socketPath, 200))
  const client = await connectAs(socketPath, 'builder')
  assert.match(
    textOf(await client.callTool({ name: 'forum_status', arguments: {} })),
    /daemon: serving this session/,
  )
  await client.close()
})

test('concurrent refreshes of one channel share a single run', async () => {
  const inFlight = new Map<string, Promise<number>>()
  let calls = 0
  const run = (): Promise<number> => {
    calls += 1
    return new Promise<number>((done) => setTimeout(() => done(calls), 10))
  }

  const first = singleFlight(inFlight, 'T1:C1', run)
  const joined = singleFlight(inFlight, 'T1:C1', run)
  assert.equal(first, joined)
  assert.equal(await first, 1)
  assert.equal(calls, 1)

  // A different channel is a different run, and a settled one is not reused.
  const other = singleFlight(inFlight, 'T1:C2', run)
  assert.equal(await other, 2)
  const later = singleFlight(inFlight, 'T1:C1', run)
  assert.notEqual(later, first)
  assert.equal(await later, 3)
  assert.equal(calls, 3)
})

test('the daemon reports its own path and peers', async (t) => {
  await seedIdentities()
  const daemon = await runDaemon()
  t.after(() => daemon.stop())
  assert.equal(daemon.socketPath, join(root, 'run', `forum-${seq - 1}.sock`))

  const client = await connectAs(daemon.socketPath, 'builder')
  t.after(() => client.close())
  const raw = textOf(await client.callTool({ name: 'forum_daemon', arguments: {} }))
  const info = JSON.parse(raw) as { socket: string; peers: Array<{ label?: string }>; idle_seconds: number }
  assert.equal(info.socket, daemon.socketPath)
  assert.equal(typeof info.idle_seconds, 'number')
  assert.ok(info.peers.some((peer) => peer.label === `builder@${HOST}`))
  assert.equal(daemonPaths().socket, join(root, 'run', 'forum.sock'))
})

test('a client with no daemon starts one process and can stop it', async () => {
  await seedIdentities()
  const paths = daemonPaths()

  const started = await ensureDaemon({ timeoutMs: 20_000 })
  assert.equal(started.spawned, true)
  assert.ok(await socketAnswers(paths.socket, 2_000))

  const status = await daemonStatus()
  assert.equal(status.running, true)
  assert.ok(status.pid !== undefined)

  const stopped = await stopDaemon()
  assert.equal(stopped.stopped, true)
  await waitFor(async () => !existsSync(paths.socket), 10_000)
})

test('the idle window defaults when the variable is unset or empty', () => {
  assert.equal(idleSeconds({}), DEFAULT_IDLE_SECONDS)
  assert.equal(idleSeconds({ SLACK_BROWSER_MCP_DAEMON_IDLE_SECONDS: '  ' }), DEFAULT_IDLE_SECONDS)
  assert.equal(idleSeconds({ SLACK_BROWSER_MCP_DAEMON_IDLE_SECONDS: '45' }), 45)
  assert.equal(idleSeconds({ SLACK_BROWSER_MCP_DAEMON_IDLE_SECONDS: '0' }), 0)
  assert.equal(idleSeconds({ SLACK_BROWSER_MCP_DAEMON_IDLE_SECONDS: 'nope' }), DEFAULT_IDLE_SECONDS)
})

test('a diagnostics call does not count itself as a peer', async (t) => {
  await seedIdentities()
  const daemon = await runDaemon()
  t.after(() => daemon.stop())

  const builder = await connectAs(daemon.socketPath, 'builder')
  t.after(() => builder.close())
  await builder.callTool({ name: 'forum_status', arguments: {} }).catch(() => undefined)

  const reported = await callDaemon('forum_daemon', {}, { socketPath: daemon.socketPath })
  assert.ok(reported.peers.every((peer) => peer.client !== INTERNAL_CLIENT))
  assert.deepEqual(
    reported.peers.map((peer) => peer.label),
    [`builder@${HOST}`],
  )
})

test('a named identity keys itself, even as the second agent on the machine', async () => {
  const forum = new ForumStore()
  const file = await forum.read()
  assert.deepEqual(Object.keys(file.identities).sort(), ['builder', 'reviewer'])

  // The bug this covers: with one identity stored and an explicit name for a key
  // that does not exist yet, borrowing the only entry made `register` overwrite it.
  process.env.SLACK_BROWSER_MCP_AGENT = 'newcomer'
  try {
    const resolved = forum.resolveIdentity(await forum.read())
    assert.equal(resolved.key, 'newcomer')
    assert.equal(resolved.identity, undefined)
  } finally {
    delete process.env.SLACK_BROWSER_MCP_AGENT
  }

  const anonymous = forum.resolveIdentity(await forum.read())
  assert.ok(anonymous.identity !== undefined)
})
