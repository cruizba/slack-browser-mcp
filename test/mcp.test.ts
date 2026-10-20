import assert from 'node:assert/strict'
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { after, test } from 'node:test'

import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js'

const configDir = await mkdtemp(join(tmpdir(), 'slack-browser-mcp-mcp-'))
const dataDir = await mkdtemp(join(tmpdir(), 'slack-browser-mcp-mcp-data-'))
process.env.SLACK_BROWSER_MCP_CONFIG_DIR = configDir
process.env.XDG_DATA_HOME = dataDir

const { buildServer } = await import('../src/mcp/server.ts')

after(() => rm(configDir, { recursive: true, force: true }))
after(() => rm(dataDir, { recursive: true, force: true }))

async function connect(t: { after(fn: () => Promise<void>): void }) {
  const server = buildServer()
  const client = new Client({ name: 'test-client', version: '0.0.0' })
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair()
  await Promise.all([server.connect(serverTransport), client.connect(clientTransport)])
  t.after(async () => {
    await client.close()
    await server.close()
  })
  return client
}

function textOf(result: unknown): string {
  const content = (result as { content?: Array<{ type: string; text?: string }> }).content ?? []
  return content.map((part) => part.text ?? '').join('\n')
}

test('exposes the Slack tools and survives an empty store', async (t) => {
  const client = await connect(t)

  const { tools } = await client.listTools()
  assert.deepEqual(
    tools.map((tool) => tool.name).sort(),
    [
      'archive_status',
      'auth_status',
      'get_thread',
      'list_conversations',
      'list_workspaces',
      'login_workspace',
      'post_message',
      'read_file',
      'read_messages',
      'search_messages',
      'set_default_workspace',
    ],
  )
  for (const tool of tools) {
    assert.ok((tool.description ?? '').length > 30, `${tool.name} needs a description`)
  }
  const writes = new Set(['login_workspace', 'post_message', 'set_default_workspace'])
  for (const tool of tools.filter((tool) => !writes.has(tool.name))) {
    assert.equal(tool.annotations?.readOnlyHint, true, `${tool.name} must be read-only`)
  }
  for (const name of ['login_workspace', 'set_default_workspace']) {
    assert.equal(tools.find((tool) => tool.name === name)?.annotations?.readOnlyHint, false)
  }
  const post = tools.find((tool) => tool.name === 'post_message')
  assert.equal(post?.annotations?.readOnlyHint, false)
  assert.equal(post?.annotations?.destructiveHint, true)

  const status = await client.callTool({ name: 'auth_status', arguments: {} })
  const text = textOf(status)
  assert.match(text, /No Slack session stored/)
  assert.match(text, /login_workspace/)
  assert.match(text, /login: idle/)

  const workspaces = await client.callTool({ name: 'list_workspaces', arguments: {} })
  assert.match(textOf(workspaces), /No Slack workspace stored yet/)
  assert.match(textOf(workspaces), /login: idle/)

  const missing = await client.callTool({ name: 'set_default_workspace', arguments: {} })
  assert.match(textOf(missing), /Pass workspace/)
  const nothing = await client.callTool({
    name: 'set_default_workspace',
    arguments: { workspace: 'Acme' },
  })
  assert.match(textOf(nothing), /No Slack session stored yet/)
})

test('a running sign-in is reported instead of starting another browser', async (t) => {
  const lock = join(dataDir, 'slack-browser-mcp', 'login.lock')
  await mkdir(lock, { recursive: true, mode: 0o700 })
  await writeFile(
    join(lock, 'owner.json'),
    JSON.stringify({ pid: process.pid, host: 'test-host', started_at: new Date().toISOString() }),
    'utf8',
  )
  t.after(() => rm(lock, { recursive: true, force: true }))

  const client = await connect(t)
  const result = await client.callTool({ name: 'login_workspace', arguments: {} })
  assert.match(textOf(result), /already running/)

  const listed = await client.callTool({ name: 'list_workspaces', arguments: {} })
  assert.match(textOf(listed), /login: running since/)
})
