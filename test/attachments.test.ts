import assert from 'node:assert/strict'
import { mkdir, mkdtemp, open, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

import {
  MAX_ATTACHMENT_BYTES,
  MAX_ATTACHMENTS,
  planAttachments,
  readAttachments,
} from '../src/slack/attachments.ts'

test('plans a local file with its name, size and a guessed type', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'slack-attach-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const report = join(root, 'informe.md')
  await writeFile(report, '# hola')

  const plans = await planAttachments([report])
  assert.deepEqual(plans, [
    { path: report, name: 'informe.md', bytes: 6, contentType: 'text/markdown' },
  ])
  assert.deepEqual(await readAttachments(plans), [
    { name: 'informe.md', bytes: new Uint8Array([35, 32, 104, 111, 108, 97]) },
  ])
})

test('refuses a missing path, a directory and an empty file', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'slack-attach-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  await mkdir(join(root, 'carpeta'))
  await writeFile(join(root, 'vacio.txt'), '')

  await assert.rejects(() => planAttachments([join(root, 'nada.md')]), /No file at/)
  await assert.rejects(() => planAttachments([join(root, 'carpeta')]), /not a regular file/)
  await assert.rejects(() => planAttachments([join(root, 'vacio.txt')]), /zero-byte/)
})

test('refuses more files than one message carries', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'slack-attach-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const paths = Array.from({ length: MAX_ATTACHMENTS + 1 }, (_, index) => join(root, `${index}.md`))

  await assert.rejects(() => planAttachments(paths), new RegExp(`at most ${MAX_ATTACHMENTS}`))
})

test('refuses a file over the per-file cap without reading it', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'slack-attach-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const big = join(root, 'grande.bin')
  // Sparse on disk, so the test does not write 100 MB to prove a size check.
  const handle = await open(big, 'w')
  await handle.truncate(MAX_ATTACHMENT_BYTES + 1)
  await handle.close()

  await assert.rejects(() => planAttachments([big]), /uploads at most/)
})
