import assert from 'node:assert/strict'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js'

test('exposes the read-only Slack tools and survives an empty store', async (t) => {
  const configDir = await mkdtemp(join(tmpdir(), 'slack-browser-mcp-mcp-'))
  process.env.SLACK_BROWSER_MCP_CONFIG_DIR = configDir
  t.after(() => rm(configDir, { recursive: true, force: true }))

  const { buildServer } = await import('../src/mcp/server.ts')
  const server = buildServer()
  const client = new Client({ name: 'test-client', version: '0.0.0' })
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair()
  await Promise.all([server.connect(serverTransport), client.connect(clientTransport)])
  t.after(async () => {
    await client.close()
    await server.close()
  })

  const { tools } = await client.listTools()
  assert.deepEqual(
    tools.map((tool) => tool.name).sort(),
    ['archive_status', 'auth_status', 'get_thread', 'list_conversations', 'post_message', 'read_messages', 'search_messages'],
  )
  for (const tool of tools) {
    assert.ok((tool.description ?? '').length > 30, `${tool.name} needs a description`)
  }
  for (const tool of tools.filter((tool) => tool.name !== 'post_message')) {
    assert.equal(tool.annotations?.readOnlyHint, true, `${tool.name} must be read-only`)
  }
  const post = tools.find((tool) => tool.name === 'post_message')
  assert.equal(post?.annotations?.readOnlyHint, false)
  assert.equal(post?.annotations?.destructiveHint, true)

  const status = await client.callTool({ name: 'auth_status', arguments: {} })
  const text = (status.content as Array<{ type: string; text?: string }>)[0]?.text ?? ''
  assert.match(text, /No Slack session stored/)
  assert.match(text, /slack-browser login/)
})
