import assert from 'node:assert/strict'
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import { ArchiveStore } from '../src/archive/store.ts'
import { encode, render, summaryFor, type Envelope } from '../src/forum/envelope.ts'
import { newId } from '../src/forum/id.ts'

const TASK = '01J9Z7K3QX4MB8WN2R5T6Y7Z8A'

function message(ts: string, envelope: Envelope) {
  return {
    ts,
    channel: 'C0HUB',
    channel_name: 'agent-hub',
    text: render(summaryFor(envelope), envelope),
    synced_at: '2026-10-28T09:30:00Z',
  }
}

export async function seed(root: string, args: { taskTs?: string } = {}) {
  const configDir = join(root, 'config')
  const dataDir = join(root, 'data')
  await mkdir(configDir, { recursive: true })

  await writeFile(
    join(configDir, 'forum.json'),
    `${JSON.stringify(
      {
        version: 1,
        identities: {
          pi: {
            name: 'reviewer',
            host: 'laptop',
            capabilities: ['review'],
            registered_at: '2026-10-28T09:00:00Z',
          },
        },
        default_identity: 'pi',
        channels: [
          {
            workspace: 'T0TEST',
            channel: 'C0HUB',
            name: 'agent-hub',
            private: true,
            since_ts: '0',
            added_at: '2026-10-28T09:00:00Z',
            sign: true,
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
  await archive.append('T0TEST', 'C0HUB', [
    message('1793145600.000100', {
      v: 1,
      kind: 'register',
      id: newId(),
      from: 'builder@desktop',
      sent: '2026-10-28T09:00:00Z',
      name: 'builder',
      host: 'desktop',
      capabilities: ['code'],
    }),
    message('1793145600.000200', {
      v: 1,
      kind: 'register',
      id: newId(),
      from: 'reviewer@laptop',
      sent: '2026-10-28T09:00:00Z',
      name: 'reviewer',
      host: 'laptop',
      capabilities: ['review'],
    }),
    message(args.taskTs ?? '1793145900.000300', {
      v: 1,
      kind: 'task',
      id: TASK,
      from: 'builder@desktop',
      to: 'reviewer@laptop',
      sent: '2026-10-28T09:05:00Z',
      title: 'revisar el PR',
      body: 'mira el diff de sync.ts',
    }),
    message('1793145960.000400', {
      v: 1,
      kind: 'task',
      id: '01J9Z7K3QX4MB8WN2R5T6Y7Z9C',
      from: 'builder@desktop',
      to: '*',
      sent: '2026-10-28T09:06:00Z',
      title: 'alguien revisa el CHANGELOG',
      body: 'una pasada rápida',
      // An older peer may still tag a broadcast. The tag no longer filters: the
      // task reaches every inbox in the channel, which is what `*` means now.
      tag: 'code',
    }),
  ])

  return { configDir, dataDir, archive }
}

test('the inbox shows what is addressed to me and counts the rest', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'forum-inbox-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  process.env.SLACK_BROWSER_MCP_CONFIG_DIR = join(root, 'config')
  process.env.XDG_DATA_HOME = join(root, 'data')
  process.env.SLACK_BROWSER_MCP_AGENT = 'pi'

  await seed(root)
  const { openForum, reload: reloadFor } = await import('../src/forum/context.ts')
  const { inboxEntries, loadViews } = await import('../src/forum/inbox.ts')
  const { renderInbox } = await import('../src/forum/render.ts')

  const context = await openForum({ now: new Date('2026-10-28T09:30:00Z') })
  assert.equal(context.identity?.name, 'reviewer')

  const views = await loadViews(context)
  const report = inboxEntries(context, views)
  assert.deepEqual(
    report.entries.map((entry) => entry.task.id),
    [TASK, '01J9Z7K3QX4MB8WN2R5T6Y7Z9C'],
  )
  assert.equal(report.entries[0]?.task.to, 'reviewer@laptop')
  assert.equal(report.entries[1]?.task.tag, 'code')
  assert.equal(report.hidden, 0)

  // Reading is idempotent, and a stored cursor from an earlier build must not hide
  // anything: the boundary is `since_ts`, and showing a task moves nothing.
  const forumFile = join(root, 'config', 'forum.json')
  const stored = JSON.parse(await readFile(forumFile, 'utf8')) as Record<string, unknown>
  stored.cursors = { pi: { 'T0TEST:C0HUB': '1793999999.000000' } }
  await writeFile(forumFile, JSON.stringify(stored), 'utf8')
  const again = inboxEntries(await reloadFor(context), await loadViews(await reloadFor(context)))
  assert.deepEqual(
    again.entries.map((entry) => entry.task.id),
    report.entries.map((entry) => entry.task.id),
  )

  const text = renderInbox(context, report)
  assert.match(text, /agent forum as reviewer@laptop/)
  assert.match(text, /private:agent-hub/)
  assert.match(text, /revisar el PR/)

  const narrow = inboxEntries(context, views, { all: true })
  assert.equal(narrow.entries.length, 2)
})

test('a broadcast another agent claimed stays listed with who has it', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'forum-inbox-taken-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  process.env.SLACK_BROWSER_MCP_CONFIG_DIR = join(root, 'config')
  process.env.XDG_DATA_HOME = join(root, 'data')
  process.env.SLACK_BROWSER_MCP_AGENT = 'pi'

  const { archive } = await seed(root)
  await archive.append('T0TEST', 'C0HUB', [
    message('1793145600.000500', {
      v: 1,
      kind: 'register',
      id: newId(),
      from: 'planner@alice-pc',
      sent: '2026-10-28T09:00:00Z',
      name: 'planner',
      host: 'alice-pc',
      capabilities: [],
    }),
    message('1793146020.000600', {
      v: 1,
      kind: 'task',
      id: '01J9Z7K3QX4MB8WN2R5T6Y7Z9F',
      from: 'builder@desktop',
      to: '*',
      sent: '2026-10-28T09:07:00Z',
      title: 'alguien mira el README',
      body: 'una pasada',
    }),
    message('1793146080.000700', {
      v: 1,
      kind: 'accept',
      id: newId(),
      from: 'planner@alice-pc',
      sent: '2026-10-28T09:08:00Z',
      task: '01J9Z7K3QX4MB8WN2R5T6Y7Z9F',
    }),
  ])

  const { openForum } = await import('../src/forum/context.ts')
  const { inboxEntries, loadViews } = await import('../src/forum/inbox.ts')
  const { renderInbox } = await import('../src/forum/render.ts')
  const context = await openForum({ now: new Date('2026-10-28T09:30:00Z') })
  const report = inboxEntries(context, await loadViews(context))

  assert.equal(report.taken, 1, 'it is still counted as somebody else\'s')
  assert.ok(
    report.entries.some((entry) => entry.task.id === '01J9Z7K3QX4MB8WN2R5T6Y7Z9F'),
    'and it stays visible: hiding it makes the next agent think nobody took it',
  )
  assert.match(renderInbox(context, report), /taken:planner@alice-pc/)
})

test('a wait does not wake for a broadcast somebody else already took', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'forum-wait-taken-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  process.env.SLACK_BROWSER_MCP_CONFIG_DIR = join(root, 'config')
  process.env.XDG_DATA_HOME = join(root, 'data')
  process.env.SLACK_BROWSER_MCP_AGENT = 'pi'

  // Only the registers and one claimed broadcast: nothing here is work for me.
  const { archive } = await seed(root)
  const registers = [
    ['1793145600.000500', 'builder@desktop', 'builder', 'desktop'],
    ['1793145600.000510', 'reviewer@laptop', 'reviewer', 'laptop'],
    ['1793145600.000520', 'planner@alice-pc', 'planner', 'alice-pc'],
  ] as const
  const record = (ts: string, envelope: Envelope) => `${JSON.stringify(message(ts, envelope))}\n`
  const lines = registers.map(([ts, from, name, host]) =>
    record(ts, {
      v: 1,
      kind: 'register',
      id: newId(),
      from,
      sent: '2026-10-28T09:00:00Z',
      name,
      host,
      capabilities: [],
    }),
  )
  lines.push(
    record('1793146020.000600', {
      v: 1,
      kind: 'task',
      id: '01J9Z7K3QX4MB8WN2R5T6Y7Z9G',
      from: 'builder@desktop',
      to: '*',
      sent: '2026-10-28T09:07:00Z',
      title: 'alguien mira el README',
      body: 'una pasada',
    }),
  )
  lines.push(
    record('1793146080.000700', {
      v: 1,
      kind: 'accept',
      id: newId(),
      from: 'planner@alice-pc',
      sent: '2026-10-28T09:08:00Z',
      task: '01J9Z7K3QX4MB8WN2R5T6Y7Z9G',
    }),
  )
  await writeFile(archive.channelFile('T0TEST', 'C0HUB'), lines.join(''))

  const { openForum } = await import('../src/forum/context.ts')
  const { waitForWork } = await import('../src/forum/watch.ts')
  const context = await openForum({ now: new Date('2026-10-28T09:30:00Z') })

  const waited = await waitForWork(context, { waitSeconds: 1, intervalMs: 50, offline: true })
  assert.equal(waited.report.taken, 1)
  assert.ok(
    waited.cycles > 1,
    'it kept looking instead of reporting work that is already somebody else\'s',
  )
})

test('a task from an unknown peer is hidden and counted, never shown as work', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'forum-inbox-unknown-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  process.env.SLACK_BROWSER_MCP_CONFIG_DIR = join(root, 'config')
  process.env.XDG_DATA_HOME = join(root, 'data')
  process.env.SLACK_BROWSER_MCP_AGENT = 'pi'

  const { archive } = await seed(root)
  await archive.append('T0TEST', 'C0HUB', [
    message('1793145990.000500', {
      v: 1,
      kind: 'task',
      id: '01J9Z7K3QX4MB8WN2R5T6Y7Z9D',
      from: 'stranger@elsewhere',
      to: 'reviewer@laptop',
      sent: '2026-10-28T09:10:00Z',
      title: 'hazme un favor',
      body: 'sin registro previo',
    }),
  ])

  const { openForum } = await import('../src/forum/context.ts')
  const { inboxEntries, loadViews } = await import('../src/forum/inbox.ts')
  const context = await openForum({ now: new Date('2026-10-28T09:30:00Z') })
  const report = inboxEntries(context, await loadViews(context))
  assert.equal(report.entries.length, 2)
  assert.equal(report.hidden, 1)
})

test('a body bigger than the context prints its head and the file id', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'forum-render-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  process.env.SLACK_BROWSER_MCP_CONFIG_DIR = join(root, 'config')
  process.env.XDG_DATA_HOME = join(root, 'data')
  process.env.SLACK_BROWSER_MCP_AGENT = 'pi'

  await seed(root)
  const { openForum } = await import('../src/forum/context.ts')
  const { loadViews } = await import('../src/forum/inbox.ts')
  const { renderTask } = await import('../src/forum/render.ts')

  const context = await openForum({ now: new Date('2026-10-28T09:30:00Z') })
  const view = (await loadViews(context))[0]!
  const task = view.ledger.tasks.find((entry) => entry.id === TASK)!
  const long = 'x'.repeat(5000)
  const envelope = {
    v: 1,
    kind: 'task',
    id: TASK,
    from: 'builder@desktop',
    to: 'reviewer@laptop',
    sent: '2026-10-28T09:05:00Z',
    title: 'revisar el PR',
    body: long,
  } as Envelope
  assert.ok(encode(envelope).length > long.length)

  const text = renderTask(
    context,
    { ...task, body: long, fileId: 'F0FORUM' },
    view.binding,
    undefined,
    view,
  )
  assert.match(text, /x{4000}… \[truncated: 5000 characters; the whole envelope is `read_file F0FORUM`\]/)
  assert.doesNotMatch(text, /x{5000}/, 'the rest stays out of the context')

  // Without a file — a legacy fenced envelope — it points at the archive instead.
  const legacy = renderTask(context, { ...task, body: long }, view.binding, undefined, view)
  assert.match(legacy, /the whole envelope is in the local archive/)
})

test('a wait that asks for all lists the work addressed to other agents', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'forum-wait-all-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  process.env.SLACK_BROWSER_MCP_CONFIG_DIR = join(root, 'config')
  process.env.XDG_DATA_HOME = join(root, 'data')
  process.env.SLACK_BROWSER_MCP_AGENT = 'pi'

  const { archive } = await seed(root)
  await archive.append('T0TEST', 'C0HUB', [
    message('1793145990.000600', {
      v: 1,
      kind: 'task',
      id: '01J9Z7K3QX4MB8WN2R5T6Y7Z9E',
      from: 'reviewer@laptop',
      to: 'builder@desktop',
      sent: '2026-10-28T09:10:00Z',
      title: 'devuelve el favor',
      body: 'revisa tú',
    }),
  ])

  const { openForum } = await import('../src/forum/context.ts')
  const { waitForWork } = await import('../src/forum/watch.ts')
  const context = await openForum({ now: new Date('2026-10-28T09:30:00Z') })

  const mine = await waitForWork(context, { waitSeconds: 0, offline: true })
  assert.equal(mine.report.entries.length, 2, 'the default view is unchanged')

  const every = await waitForWork(context, { waitSeconds: 0, offline: true, all: true })
  assert.deepEqual(
    every.report.entries.map((entry) => entry.task.id),
    [TASK, '01J9Z7K3QX4MB8WN2R5T6Y7Z9C', '01J9Z7K3QX4MB8WN2R5T6Y7Z9E'],
  )
})
