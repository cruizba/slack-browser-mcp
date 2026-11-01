import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { mkdir, mkdtemp, readFile, rm, stat, utimes, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, it } from 'node:test'

import { ArchiveFileStore, safeFileName } from '../src/archive/files.ts'

describe('safeFileName', () => {
  it('keeps the id and a readable name', () => {
    assert.equal(safeFileName({ name: 'Screenshot 2026-10-20.png' }, 'F0ABC'), 'F0ABC-Screenshot 2026-10-20.png')
  })

  it('cannot escape the team directory', () => {
    const name = safeFileName({ name: '../../etc/passwd' }, 'F0ABC')
    assert.equal(name, 'F0ABC-_.._etc_passwd')
    assert.ok(!name.includes('/'))
  })

  it('falls back to the mimetype and then to a literal name', () => {
    assert.equal(safeFileName({ mimetype: 'image/png' }, 'F1'), 'F1-image_png')
    assert.equal(safeFileName({}, 'F2'), 'F2-file')
  })
})

describe('ArchiveFileStore', () => {
  it('stores a file 0600 and finds it by id', async () => {
    const root = await mkdtemp(join(tmpdir(), 'slack-browser-mcp-files-'))
    try {
      const store = new ArchiveFileStore(root)
      const saved = await store.save(
        'T01ACME',
        { id: 'F0ABC', name: 'foto.png', mimetype: 'image/png' },
        new Uint8Array([1, 2, 3, 4]),
      )
      assert.equal((await stat(saved.path)).mode & 0o777, 0o600)
      assert.equal((await stat(join(root, 'T01ACME', 'files'))).mode & 0o777, 0o700)
      assert.deepEqual([...(await readFile(saved.path))], [1, 2, 3, 4])

      const found = await store.find('T01ACME', 'F0ABC')
      assert.equal(found?.path, saved.path)
      assert.equal(found?.name, 'foto.png')
      assert.equal(found?.bytes, 4)
      assert.equal(await store.find('T01ACME', 'F9NOPE'), undefined)
      assert.equal(await store.find('T99OTHER', 'F0ABC'), undefined)
    } finally {
      await rm(root, { recursive: true, force: true })
    }
  })

  it('replaces a cached copy instead of adding a second one', async () => {
    const root = await mkdtemp(join(tmpdir(), 'slack-browser-mcp-files-'))
    try {
      const store = new ArchiveFileStore(root)
      const file = { id: 'F0ABC', name: 'foto.png' }
      await store.save('T01ACME', file, new Uint8Array([1]))
      const again = await store.save('T01ACME', file, new Uint8Array([2, 3]))
      assert.equal(again.bytes, 2)
      assert.deepEqual([...(await readFile(again.path))], [2, 3])
      assert.equal((await store.find('T01ACME', 'F0ABC'))?.bytes, 2)
    } finally {
      await rm(root, { recursive: true, force: true })
    }
  })

  it('keeps one copy when the same id comes back under another name', async () => {
    const root = await mkdtemp(join(tmpdir(), 'slack-browser-mcp-files-'))
    try {
      const store = new ArchiveFileStore(root)
      const first = await store.save('T01ACME', { id: 'F0ABC', name: 'foto.png' }, new Uint8Array([1]))
      const renamed = await store.save(
        'T01ACME',
        { id: 'F0ABC', name: 'vacaciones.png' },
        new Uint8Array([2, 3]),
      )

      assert.notEqual(renamed.path, first.path)
      assert.equal(existsSync(first.path), false, 'the old name is removed, not shadowed')
      assert.deepEqual([...(await readFile(renamed.path))], [2, 3])
      const found = await store.find('T01ACME', 'F0ABC')
      assert.equal(found?.name, 'vacaciones.png')
      assert.equal(found?.bytes, 2)
    } finally {
      await rm(root, { recursive: true, force: true })
    }
  })

  it('prefers the newest copy when two are left for one id', async () => {
    const root = await mkdtemp(join(tmpdir(), 'slack-browser-mcp-files-'))
    try {
      const dir = join(root, 'T01ACME', 'files')
      await mkdir(dir, { recursive: true })
      const older = join(dir, 'F0ABC-vieja.png')
      const newer = join(dir, 'F0ABC-nueva.png')
      await writeFile(older, 'vieja')
      await writeFile(newer, 'nueva')
      const past = new Date(Date.now() - 60_000)
      await utimes(older, past, past)

      const found = await new ArchiveFileStore(root).find('T01ACME', 'F0ABC')
      assert.equal(found?.name, 'nueva.png')
    } finally {
      await rm(root, { recursive: true, force: true })
    }
  })

  it('refuses a team id that would become a path', async () => {
    const root = await mkdtemp(join(tmpdir(), 'slack-browser-mcp-files-'))
    try {
      const store = new ArchiveFileStore(root)
      await assert.rejects(
        () => store.save('../evil', { id: 'F0ABC' }, new Uint8Array([1])),
        /Invalid team id/,
      )
    } finally {
      await rm(root, { recursive: true, force: true })
    }
  })
})
