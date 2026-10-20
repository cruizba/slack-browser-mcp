import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm, stat } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import { recordSend } from '../src/slack/audit.ts'

test('recordSend appends one 0600 line with a redacted, bounded preview', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'slack-audit-'))
  process.env.XDG_DATA_HOME = root
  t.after(() => rm(root, { recursive: true, force: true }))

  const long = `token=xoxc-supersecret ${'x'.repeat(300)}`
  const file = await recordSend(
    {
      workspace: 'Globex',
      channel: '#ci-notifications',
      channel_id: 'C0CINOTIF',
      message_ts: '1792500000.000100',
      text: long,
    },
    new Date('2026-10-20T15:40:00.000Z'),
  )

  const mode = (await stat(file)).mode & 0o777
  assert.equal(mode, 0o600)

  const lines = (await readFile(file, 'utf8')).trim().split('\n')
  assert.equal(lines.length, 1)
  const record = JSON.parse(lines[0]!)
  assert.equal(record.at, '2026-10-20T15:40:00.000Z')
  assert.equal(record.workspace, 'Globex')
  assert.equal(record.channel_id, 'C0CINOTIF')
  assert.equal(record.message_ts, '1792500000.000100')
  assert.equal(record.chars, long.length)
  assert.ok(record.preview.length <= 120, `preview too long: ${record.preview.length}`)
  assert.ok(!record.preview.includes('supersecret'), 'preview leaked the token')
})
