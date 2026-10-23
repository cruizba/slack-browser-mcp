import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js'

import { ASSISTED_RULE } from '../src/slack/assist.ts'

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  })
}

function textOf(result: unknown): string {
  const content = (result as { content?: Array<{ type: string; text?: string }> }).content ?? []
  return content.map((part) => part.text ?? '').join('\n')
}

test('post_message refuses without confirm and posts exactly once with it', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'slack-write-'))
  process.env.SLACK_BROWSER_MCP_CONFIG_DIR = join(root, 'config')
  process.env.XDG_DATA_HOME = join(root, 'data')
  t.after(() => rm(root, { recursive: true, force: true }))

  const { FileCredentialStore } = await import('../src/auth/store.ts')
  await new FileCredentialStore(join(root, 'config', 'accounts')).save({
    team_id: 'T0TEST',
    team_name: 'Globex',
    team_domain: 'globex',
    user: 'ana.lopez',
    user_id: 'U0AUTHOR',
    token: 'xoxc-test-token',
    d_cookie: 'xoxd-test%2Bcookie',
    d_cookie_name: 'd',
    saved_at: new Date().toISOString(),
  })

  const calls: Array<{ method: string; body: URLSearchParams }> = []
  const originalFetch = globalThis.fetch
  ;(globalThis as { fetch: typeof fetch }).fetch = (async (
    input: string | URL | Request,
    init?: RequestInit,
  ) => {
    const method = String(input).split('/api/')[1] ?? ''
    const body = new URLSearchParams(String(init?.body ?? ''))
    calls.push({ method, body })
    if (method === 'users.conversations') {
      return json({
        ok: true,
        channels: [{ id: 'C0CINOTIF', name: 'ci-notifications', is_private: false }],
      })
    }
    if (method === 'chat.postMessage') {
      return json({
        ok: true,
        channel: body.get('channel'),
        ts: '1792500000.000100',
        message: { text: body.get('text') },
      })
    }
    return json({ ok: false, error: 'unexpected_method' })
  }) as typeof fetch
  t.after(() => {
    ;(globalThis as { fetch: typeof fetch }).fetch = originalFetch
  })

  const { buildServer } = await import('../src/mcp/server.ts')
  const server = buildServer()
  const client = new Client({ name: 'test-client', version: '0.0.0' })
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair()
  await Promise.all([server.connect(serverTransport), client.connect(clientTransport)])
  t.after(async () => {
    await client.close()
    await server.close()
  })

  const dry = await client.callTool({
    name: 'post_message',
    arguments: { channel: 'ci-notifications', text: 'hola desde el test' },
  })
  const dryText = (dry.content as Array<{ type: string; text?: string }>)[0]?.text ?? ''
  assert.match(dryText, /Nothing sent/)
  assert.match(dryText, /confirm=true/)
  assert.deepEqual(
    calls.map((call) => call.method),
    ['users.conversations'],
    'a dry run may resolve the channel, but must never post',
  )

  const sent = await client.callTool({
    name: 'post_message',
    arguments: {
      channel: 'ci-notifications',
      text: 'hola desde el test',
      confirm: true,
    },
  })
  const sentText = (sent.content as Array<{ type: string; text?: string }>)[0]?.text ?? ''
  assert.match(sentText, /Sent to Globex · #ci-notifications \(C0CINOTIF\) at ts 1792500000\.000100/)

  const post = calls.find((call) => call.method === 'chat.postMessage')
  assert.ok(post, 'chat.postMessage was not called')
  assert.equal(post.body.get('channel'), 'C0CINOTIF')
  assert.equal(
    post.body.get('text'),
    `\u200b\n${ASSISTED_RULE}\n\n*Message Sent by AI Agent 'test-client' of ana.lopez*\n\nhola desde el test\n\n${ASSISTED_RULE}`,
    'with no nickname configured the generated default signs the message',
  )
  assert.equal(post.body.get('token'), 'xoxc-test-token')
  assert.equal(
    calls.filter((call) => call.method === 'chat.postMessage').length,
    1,
    'confirm must post exactly once',
  )

  const audit = await readFile(join(root, 'data', 'slack-browser-mcp', 'audit.jsonl'), 'utf8')
  const record = JSON.parse(audit.trim().split('\n')[0]!)
  assert.equal(record.channel_id, 'C0CINOTIF')
  assert.equal(record.message_ts, '1792500000.000100')
  assert.equal(record.text, undefined, 'the audit log must not store the full text')

  // The dry run must already show the header: it promises the exact text.
  const signed = await client.callTool({
    name: 'set_assisted_nickname',
    arguments: { nickname: 'Qwen3.8 27B' },
  })
  assert.match(textOf(signed), /assisted nickname: Qwen3\.8 27B/)

  const preview = await client.callTool({
    name: 'post_message',
    arguments: { channel: 'ci-notifications', text: 'hola con mote' },
  })
  assert.match(
    textOf(preview),
    /\*Message Sent by AI Qwen3\.8 27B\*\n\nhola con mote/,
  )

  await client.callTool({
    name: 'post_message',
    arguments: { channel: 'ci-notifications', text: 'hola con mote', confirm: true },
  })
  const signedPost = calls.filter((call) => call.method === 'chat.postMessage').at(-1)
  assert.equal(
    signedPost?.body.get('text'),
    `\u200b\n${ASSISTED_RULE}\n\n*Message Sent by AI Qwen3.8 27B*\n\nhola con mote\n\n${ASSISTED_RULE}`,
  )

  await client.callTool({
    name: 'post_message',
    arguments: { channel: 'ci-notifications', text: 'sin cabecera', confirm: true, assist: false },
  })
  const barePost = calls.filter((call) => call.method === 'chat.postMessage').at(-1)
  assert.equal(barePost?.body.get('text'), 'sin cabecera')

  const cleared = await client.callTool({
    name: 'set_assisted_nickname',
    arguments: { clear: true },
  })
  assert.match(textOf(cleared), /Agent 'test-client' of <the sending account>/)
})
