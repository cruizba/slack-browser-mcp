import assert from 'node:assert/strict'
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import { ArchiveStore } from '../src/archive/store.ts'
import { render, summaryFor, type Envelope } from '../src/forum/envelope.ts'
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
      tag: 'review',
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
  const { openForum } = await import('../src/forum/context.ts')
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
  assert.equal(report.entries[1]?.task.tag, 'review')
  assert.equal(report.hidden, 0)

  const text = renderInbox(context, report)
  assert.match(text, /agent forum as reviewer@laptop/)
  assert.match(text, /private:agent-hub/)
  assert.match(text, /revisar el PR/)

  const narrow = inboxEntries(context, views, { all: true })
  assert.equal(narrow.entries.length, 2)
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
