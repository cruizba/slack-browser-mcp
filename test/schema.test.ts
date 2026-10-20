import assert from 'node:assert/strict'
import { mkdir, mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises'
import { hostname, tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { after, describe, it } from 'node:test'

const dataHome = await mkdtemp(join(tmpdir(), 'slack-browser-mcp-schema-'))
process.env.XDG_DATA_HOME = dataHome

const { ArchiveIndex } = await import('../src/archive/index.ts')
const { ArchiveStore } = await import('../src/archive/store.ts')
const { ARCHIVE_SCHEMA_VERSION, ensureArchiveSchema } = await import('../src/archive/schema.ts')
const { SchemaError } = await import('../src/errors.ts')
const { archiveIndexPath } = await import('../src/config/paths.ts')

let roots: string[] = []

async function archiveRoot(files: Record<string, string> = {}): Promise<string> {
  const root = await mkdtemp(join(tmpdir(), 'slack-browser-mcp-archive-'))
  roots.push(root)
  for (const [relative, contents] of Object.entries(files)) {
    const file = join(root, relative)
    await mkdir(dirname(file), { recursive: true, mode: 0o700 })
    await writeFile(file, contents, { encoding: 'utf8', mode: 0o600 })
  }
  return root
}

const legacy = [
  '{"ts":"1700000000.000100","channel":"C01CI","user":"U01ANA","text":"hola","files":["holiday.jpg"],"thread_ts":null,"synced_at":"2026-10-20T10:00:00.000Z"}',
  'not json at all',
  '{"ts":"1700000000.000200","channel":"C01CI","text":"con id","files":[{"id":"F0ABC","name":"holiday.jpg"}],"synced_at":"2026-10-20T10:00:00.000Z"}',
].join('\n')

after(async () => {
  for (const root of roots) await rm(root, { recursive: true, force: true })
  await rm(dataHome, { recursive: true, force: true })
})

describe('ensureArchiveSchema', () => {
  it('marks an empty archive with the current version', async () => {
    const root = await archiveRoot()
    const status = await ensureArchiveSchema({ root })
    assert.deepEqual(status, { from: ARCHIVE_SCHEMA_VERSION, to: ARCHIVE_SCHEMA_VERSION, applied: [] })
    const marker = JSON.parse(await readFile(join(root, 'schema.json'), 'utf8'))
    assert.equal(marker.version, ARCHIVE_SCHEMA_VERSION)
    assert.equal((await stat(join(root, 'schema.json'))).mode & 0o777, 0o600)
  })

  it('upgrades legacy records in place and quarantines unreadable lines', async () => {
    const root = await archiveRoot({ 'T01ACME/C01CI.jsonl': `${legacy}\n` })
    const status = await ensureArchiveSchema({ root })
    assert.deepEqual(status, { from: 1, to: ARCHIVE_SCHEMA_VERSION, applied: [2] })

    const file = join(root, 'T01ACME', 'C01CI.jsonl')
    const lines = (await readFile(file, 'utf8')).trim().split('\n').map((line) => JSON.parse(line))
    assert.deepEqual(lines[0].files, [{ name: 'holiday.jpg' }])
    assert.equal('thread_ts' in lines[0], false, 'a null field becomes absent')
    assert.deepEqual(lines[1].files, [{ id: 'F0ABC', name: 'holiday.jpg' }])
    assert.equal(lines.length, 2, 'the unreadable line is not kept in the log')

    const corrupt = await readFile(`${file}.corrupt`, 'utf8')
    assert.match(corrupt, /not json at all/)
    assert.equal(JSON.parse(await readFile(join(root, 'schema.json'), 'utf8')).version, ARCHIVE_SCHEMA_VERSION)
  })

  it('rewrites nothing the second time', async () => {
    const root = await archiveRoot({ 'T01ACME/C01CI.jsonl': `${legacy}\n` })
    await ensureArchiveSchema({ root })
    const file = join(root, 'T01ACME', 'C01CI.jsonl')
    const before = { contents: await readFile(file, 'utf8'), mtime: (await stat(file)).mtimeMs }

    const again = await ensureArchiveSchema({ root })
    assert.deepEqual(again, { from: 2, to: 2, applied: [] })
    assert.equal(await readFile(file, 'utf8'), before.contents)
    assert.equal((await stat(file)).mtimeMs, before.mtime, 'a converged migration must not touch the file')
  })

  it('rebuilds the derived index instead of trusting the old one', async () => {
    const root = await archiveRoot({ 'T01ACME/C01CI.jsonl': `${legacy}\n` })
    await mkdir(join(archiveIndexPath(), '..'), { recursive: true, mode: 0o700 })
    await writeFile(archiveIndexPath(), 'stale', { mode: 0o600 })

    await ensureArchiveSchema({ root })
    const index = new ArchiveIndex()
    try {
      const hits = index.search({ query: 'hola' })
      assert.equal(hits.length, 1)
      assert.equal(hits[0]?.ts, '1700000000.000100')
    } finally {
      index.close()
    }
  })

  it('refuses an archive written by a newer build', async () => {
    const root = await archiveRoot({
      'schema.json': '{"version":99,"updated_at":"2026-10-20T10:00:00.000Z"}\n',
      'T01ACME/C01CI.jsonl': `${legacy}\n`,
    })
    await assert.rejects(
      () => ensureArchiveSchema({ root }),
      (error: unknown) => error instanceof SchemaError && /newer/.test(error.message),
    )
    assert.match(await readFile(join(root, 'T01ACME', 'C01CI.jsonl'), 'utf8'), /"files":\["/)
  })

  it('leaves the archive alone while a sync holds the lock', async () => {
    const root = await archiveRoot({ 'T01ACME/C01CI.jsonl': `${legacy}\n` })
    const lock = join(root, '.sync.lock')
    await mkdir(lock, { recursive: true, mode: 0o700 })
    await writeFile(
      join(lock, 'owner.json'),
      JSON.stringify({ pid: process.pid, host: hostname(), started_at: new Date().toISOString() }),
    )

    const status = await ensureArchiveSchema({ root })
    assert.equal(status.locked, true)
    assert.deepEqual(status.applied, [])
    assert.match(await readFile(join(root, 'T01ACME', 'C01CI.jsonl'), 'utf8'), /"files":\["/)
    await rm(lock, { recursive: true, force: true })
  })

  it('does not mistake lock or cache directories for teams', async () => {
    const root = await archiveRoot({ 'T01ACME/C01CI.jsonl': '' })
    await mkdir(join(root, '.sync.lock'), { recursive: true, mode: 0o700 })
    await mkdir(join(root, '.cache'), { recursive: true, mode: 0o700 })
    assert.deepEqual(await new ArchiveStore(root).teams(), ['T01ACME'])
  })
})
