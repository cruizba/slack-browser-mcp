import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
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

test('post_message uploads attachments as one message and audits them', async (t) => {
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

  const report = join(root, 'informe.md')
  const body = '# informe\n\ncontenido'
  await writeFile(report, body)

  const calls: Array<{ url: string; body: URLSearchParams }> = []
  const originalFetch = globalThis.fetch
  ;(globalThis as { fetch: typeof fetch }).fetch = (async (
    input: string | URL | Request,
    init?: RequestInit,
  ) => {
    const url = String(input)
    if (url.startsWith('https://files.slack.com/upload/')) return new Response('OK', { status: 200 })
    const method = url.split('/api/')[1] ?? ''
    const form = new URLSearchParams(String(init?.body ?? ''))
    calls.push({ url, body: form })
    if (method === 'users.conversations') {
      return json({
        ok: true,
        channels: [{ id: 'C0CINOTIF', name: 'ci-notifications', is_private: false }],
      })
    }
    if (method === 'files.getUploadURLExternal') {
      return json({ ok: true, upload_url: 'https://files.slack.com/upload/v1/abc', file_id: 'F0ATT' })
    }
    if (method === 'files.completeUploadExternal') {
      return json({
        ok: true,
        files: [
          { id: 'F0ATT', shares: { private: { C0CINOTIF: [{ ts: '1792500000.000300' }] } } },
        ],
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
    arguments: { channel: 'ci-notifications', text: 'el informe', attachments: [report] },
  })
  const dryText = textOf(dry)
  assert.match(dryText, /Nothing sent/)
  assert.match(dryText, /informe\.md \(text\/markdown, \d+ B\)/)
  assert.equal(
    calls.some((call) => call.url.endsWith('files.getUploadURLExternal')),
    false,
    'a dry run must not ask for an upload URL',
  )

  const sent = await client.callTool({
    name: 'post_message',
    arguments: {
      channel: 'ci-notifications',
      text: 'el informe',
      attachments: [report],
      confirm: true,
    },
  })
  assert.match(
    textOf(sent),
    /Sent to Globex · #ci-notifications \(C0CINOTIF\) at ts 1792500000\.000300 with 1 attachment\(s\) \(F0ATT\)/,
  )

  const completion = calls.find((call) => call.url.endsWith('files.completeUploadExternal'))
  assert.ok(completion, 'files.completeUploadExternal was not called')
  assert.equal(completion.body.get('channel_id'), 'C0CINOTIF')
  assert.equal(
    completion.body.get('initial_comment'),
    `${ASSISTED_RULE}\n\n*Message Sent by AI Agent 'test-client' of ana.lopez*\n\nel informe\n\n${ASSISTED_RULE}`,
    'an upload cannot carry blocks, so the plain signed fallback is the comment',
  )
  assert.deepEqual(JSON.parse(String(completion.body.get('files'))), [
    { id: 'F0ATT', title: 'informe.md' },
  ])
  assert.equal(
    calls.some((call) => call.url.endsWith('chat.postMessage')),
    false,
    'files and their comment are one message, not a post plus an upload',
  )

  const audit = await readFile(join(root, 'data', 'slack-browser-mcp', 'audit.jsonl'), 'utf8')
  const record = JSON.parse(audit.trim().split('\n').at(-1)!)
  assert.equal(record.message_ts, '1792500000.000300')
  assert.deepEqual(record.files, [`informe.md (${body.length} B)`])
  assert.equal(record.text, undefined, 'the audit log must not store the full text')
})

test('post_message names a missing attachment before it talks to Slack', async (t) => {
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

  const calls: string[] = []
  const originalFetch = globalThis.fetch
  ;(globalThis as { fetch: typeof fetch }).fetch = (async (input: string | URL | Request) => {
    calls.push(String(input))
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

  const failed = await client.callTool({
    name: 'post_message',
    arguments: {
      channel: 'ci-notifications',
      text: 'el informe',
      attachments: [join(root, 'no-existe.md')],
      confirm: true,
    },
  })
  assert.match(textOf(failed), /No file at .*no-existe\.md/)
  assert.deepEqual(calls, [], 'a bad path must fail before the first Slack call')
})
