import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import { ArchiveStore } from '../src/archive/store.ts'
import { signingFor } from '../src/config/assist.ts'
import { encode, render, summaryFor, type Envelope } from '../src/forum/envelope.ts'
import { newId } from '../src/forum/id.ts'
import type { SlackAccount } from '../src/auth/store.ts'
import type { SlackClient } from '../src/slack/client.ts'

process.env.SLACK_BROWSER_MCP_AGENT = 'pi'
process.env.SLACK_BROWSER_MCP_NO_PRESENCE = '1'

const TASK = '01J9Z7K3QX4MB8WN2R5T6Y7Z8A'
const NOW = new Date('2026-11-01T10:00:00Z')
const ME = 'U0ME'
const PEER = 'U0PEER'

const { openForum } = await import('../src/forum/context.ts')
const { inboxEntries, loadViews } = await import('../src/forum/inbox.ts')
const { deletePosting, editTaskBody } = await import('../src/forum/remove.ts')
const { ForumError } = await import('../src/errors.ts')

function register(name: string, host: string, sent: string): Envelope {
  return { v: 1, kind: 'register', id: newId(), from: `${name}@${host}`, sent, name, host, capabilities: [] }
}

function taskEnvelope(from: string, to: string, body = 'Lee el diff de sync.ts'): Envelope {
  return {
    v: 1,
    kind: 'task',
    id: TASK,
    from,
    to,
    sent: '2026-11-01T09:00:00.000Z',
    title: 'Revisa el PR 12',
    body,
  }
}

function acceptEnvelope(from: string): Envelope {
  return {
    v: 1,
    kind: 'accept',
    id: newId(),
    from,
    task: TASK,
    sent: '2026-11-01T09:05:00.000Z',
  }
}

async function seed(
  root: string,
  options: { mine: boolean; replies?: Envelope[] },
): Promise<{ archive: ArchiveStore }> {
  const configDir = join(root, 'config')
  const dataDir = join(root, 'data')
  await mkdir(configDir, { recursive: true })
  await writeFile(
    join(configDir, 'forum.json'),
    `${JSON.stringify(
      {
        version: 1,
        identities: {
          pi: { name: 'reviewer', host: 'laptop', registered_at: '2026-11-01T08:00:00Z' },
        },
        default_identity: 'pi',
        channels: [
          {
            workspace: 'T0TEST',
            channel: 'C0HUB',
            name: 'agent-hub',
            private: true,
            since_ts: '0',
            added_at: '2026-11-01T08:00:00Z',
            sign: false,
          },
        ],
        limits: {},
        cursors: {},
        ignored: [],
        posting: {},
      },
      null,
      2,
    )}\n`,
    'utf8',
  )

  const archive = new ArchiveStore(join(dataDir, 'slack-browser-mcp', 'archive'))
  const record = (ts: string, envelope: Envelope, user: string) => ({
    ts,
    channel: 'C0HUB',
    channel_name: 'agent-hub',
    text: render(summaryFor(envelope), envelope),
    user,
    synced_at: '2026-11-01T09:30:00.000Z',
  })

  const mine = options.mine
  const task = mine
    ? taskEnvelope('reviewer@laptop', 'builder@desktop')
    : taskEnvelope('builder@desktop', 'reviewer@laptop')
  const replies = options.replies ?? []

  await archive.append('T0TEST', 'C0HUB', [
    record('1793600000.000001', register('builder', 'desktop', '2026-11-01T08:00:00.000Z'), PEER),
    record('1793600000.000002', register('reviewer', 'laptop', '2026-11-01T08:00:00.000Z'), ME),
    record('1793600100.000100', task, mine ? ME : PEER),
    ...replies.map((reply, index) =>
      record(`1793600200.000${String(100 + index)}`, reply, mine ? PEER : ME),
    ),
  ])
  return { archive }
}

function fakeTransport(user = ME) {
  const deletions: string[] = []
  const uploads: Array<{ text?: string; thread_ts?: string }> = []
  let counter = 0
  const client = {
    async deleteMessage(params: { channel: string; ts: string }) {
      deletions.push(params.ts)
    },
    async uploadFiles(params: { text?: string; thread_ts?: string }) {
      counter += 1
      uploads.push({
        ...(params.text ? { text: params.text } : {}),
        ...(params.thread_ts ? { thread_ts: params.thread_ts } : {}),
      })
      return { ts: `1793600300.000${String(100 + counter)}`, fileIds: [`F${counter}`] }
    },
  } as unknown as SlackClient
  const account = {
    team_id: 'T0TEST',
    team_name: 'Test',
    user_id: user,
    token: 'xoxc-test',
    d_cookie: 'cookie',
  } as unknown as SlackAccount
  return {
    deletions,
    uploads,
    resolve: async () => ({ account, client, signing: signingFor({}) }),
  }
}

async function setup(options: { mine: boolean; replies?: Envelope[] }) {
  const root = await mkdtemp(join(tmpdir(), 'forum-remove-'))
  process.env.SLACK_BROWSER_MCP_CONFIG_DIR = join(root, 'config')
  process.env.XDG_DATA_HOME = join(root, 'data')
  const { archive } = await seed(root, options)
  const context = await openForum({ now: NOW })
  return { root, archive, context, cleanup: () => rm(root, { recursive: true, force: true }) }
}

test('a deleted task keeps its thread and stops showing up in the inbox', async (t) => {
  const { root, archive, context, cleanup } = await setup({
    mine: false,
    replies: [acceptEnvelope('reviewer@laptop')],
  })
  t.after(cleanup)

  const before = await loadViews(context)
  assert.equal(before[0]!.ledger.tasks[0]!.state, 'accepted')
  assert.equal(inboxEntries(context, before).entries.length, 1)

  await archive.append('T0TEST', 'C0HUB', [
    {
      ts: '1793600100.000100',
      channel: 'C0HUB',
      deleted_at: NOW.toISOString(),
      synced_at: NOW.toISOString(),
    },
  ])

  const after = await loadViews(await openForum({ now: NOW }))
  const task = after[0]!.ledger.tasks[0]!
  assert.equal(task.state, 'deleted')
  assert.equal(task.closed, true)
  assert.equal(task.events.length, 1, 'the accept stays: the task did not lose its thread')
  assert.equal(after[0]!.ledger.orphans, 0, 'replies to a deleted task are not orphans')
  assert.deepEqual(inboxEntries(await openForum({ now: NOW }), after).entries, [])
  assert.equal(inboxEntries(await openForum({ now: NOW }), after, { all: true }).entries.length, 1)
  assert.ok(existsSync(join(root, 'config', 'forum.json')))
})

test('delete is a dry run until --post, then it removes the message', async (t) => {
  const { archive, context, cleanup } = await setup({ mine: true })
  t.after(cleanup)
  const transport = fakeTransport()

  const preview = await deletePosting(context, transport.resolve, { ref: TASK, force: false, confirm: false })
  assert.equal(preview.kind, 'preview')
  assert.match(preview.text, /would delete task 01J9Z7K3QX4MB8WN2R5T6Y7Z8A "Revisa el PR 12"/)
  assert.deepEqual(transport.deletions, [])

  const done = await deletePosting(context, transport.resolve, { ref: TASK, force: false, confirm: true })
  assert.equal(done.kind, 'done')
  assert.deepEqual(transport.deletions, ['1793600100.000100'])

  const views = await loadViews(await openForum({ now: NOW }))
  assert.equal(views[0]!.ledger.tasks[0]!.state, 'deleted')
  assert.ok(archive)
})

test('deleting a root with replies needs --force', async (t) => {
  const { context, cleanup } = await setup({
    mine: true,
    replies: [acceptEnvelope('builder@desktop')],
  })
  t.after(cleanup)
  const transport = fakeTransport()

  await assert.rejects(
    () => deletePosting(context, transport.resolve, { ref: TASK, force: false, confirm: true }),
    (error: unknown) => error instanceof ForumError && /1 reply/.test(error.message),
  )
  assert.deepEqual(transport.deletions, [])

  const forced = await deletePosting(context, transport.resolve, { ref: TASK, force: true, confirm: false })
  assert.match(forced.text, /leaving 1 reply without a parent/)
})

test('a message posted by another account is refused', async (t) => {
  const { context, cleanup } = await setup({ mine: false })
  t.after(cleanup)
  const transport = fakeTransport(ME)

  await assert.rejects(
    () => deletePosting(context, transport.resolve, { ref: TASK, force: true, confirm: true }),
    (error: unknown) => error instanceof ForumError && /another account/.test(error.message),
  )
  assert.deepEqual(transport.deletions, [])
})

test('edit re-posts the body, deletes the old message and reads as edited', async (t) => {
  const { root, context, cleanup } = await setup({ mine: true })
  t.after(cleanup)
  const transport = fakeTransport()

  const preview = await editTaskBody(context, transport.resolve, {
    id: TASK,
    body: 'Lee el diff de sync.ts y el test',
    confirm: false,
  })
  assert.equal(preview.kind, 'preview')
  assert.match(preview.text, /would replace the body of 01J9Z7K3QX4MB8WN2R5T6Y7Z8A/)
  assert.deepEqual(transport.uploads, [])

  const done = await editTaskBody(context, transport.resolve, {
    id: TASK,
    body: 'Lee el diff de sync.ts y el test',
    confirm: true,
  })
  assert.equal(done.kind, 'done')
  assert.equal(transport.uploads.length, 1, 'the replacement is one new envelope')
  assert.deepEqual(transport.deletions, ['1793600100.000100'])

  const views = await loadViews(await openForum({ now: NOW }))
  const task = views[0]!.ledger.tasks[0]!
  assert.equal(task.body, 'Lee el diff de sync.ts y el test')
  assert.equal(task.state, 'open')
  assert.ok(task.edited_at !== undefined)
  assert.notEqual(task.ts, '1793600100.000100', 'the header points at the replacement')

  const audit = await readFile(join(root, 'data', 'slack-browser-mcp', 'audit.jsonl'), 'utf8')
  const line = JSON.parse(audit.trim().split('\n').at(-1)!) as { action: string; preview: string }
  assert.equal(line.action, 'edit')
  assert.match(line.preview, /Lee el diff de sync\.ts/)
})

test('edit is refused once the thread has replies', async (t) => {
  const { context, cleanup } = await setup({
    mine: true,
    replies: [acceptEnvelope('builder@desktop')],
  })
  t.after(cleanup)
  const transport = fakeTransport()

  await assert.rejects(
    () =>
      editTaskBody(context, transport.resolve, {
        id: TASK,
        body: 'otra cosa',
        confirm: true,
      }),
    (error: unknown) => error instanceof ForumError && /already has 1 reply/.test(error.message),
  )
  assert.deepEqual(transport.uploads, [])
  assert.deepEqual(transport.deletions, [])
})

test('an envelope encoded from an edited body still round-trips', () => {
  const envelope = taskEnvelope('reviewer@laptop', 'builder@desktop', 'con `fences` y <brackets>')
  assert.match(encode(envelope), /con `fences` y <brackets>/)
})
