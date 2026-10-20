import assert from 'node:assert/strict'
import { mkdir, mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, it } from 'node:test'

import {
  DIRECTORY_MAX_AGE_MS,
  DirectoryStore,
  directoryScope,
  directoryUser,
  fetchDirectory,
  loadDirectory,
  type Directory,
  type DirectoryClient,
} from '../src/slack/directory.ts'

function directory(overrides: Partial<Directory> = {}): Directory {
  return {
    version: 1,
    team_id: 'T01ACME',
    saved_at: '2026-10-20T18:00:00.000Z',
    users: [{ id: 'U02ANA', name: 'ana', display_name: 'ana' }],
    channels: [{ id: 'C01RANDOM', name: 'random' }],
    usergroups: [{ id: 'S01TEACH', handle: 'profesores' }],
    ...overrides,
  }
}

const clients: DirectoryClient = {
  listUsers: async () => [{ id: 'U02ANA', display_name: 'ana' }],
  listAllChannels: async () => [{ id: 'C01RANDOM', name: 'random' }],
  listUsergroups: async () => [{ id: 'S01TEACH', handle: 'profesores' }],
}

describe('directory helpers', () => {
  it('answers references from the stored names', () => {
    const scope = directoryScope(directory())
    assert.equal(scope?.user('U02ANA'), 'ana')
    assert.equal(scope?.channel('C01RANDOM'), 'random')
    assert.equal(scope?.usergroup('S01TEACH'), 'profesores')
    assert.equal(scope?.user('U999'), undefined)
    assert.equal(directoryUser(directory(), 'U02ANA'), 'ana')
  })

  it('prefers display_name, then real_name, then the handle', () => {
    const dir = directory({
      users: [{ id: 'U1', name: 'handle', real_name: 'Real Name' }],
    })
    assert.equal(directoryUser(dir, 'U1'), 'Real Name')
  })

  it('returns no scope when there is no directory', () => {
    assert.equal(directoryScope(undefined), undefined)
    assert.equal(directoryUser(undefined, 'U1'), undefined)
  })
})

describe('fetchDirectory', () => {
  it('collects users, channels and groups', async () => {
    const fetched = await fetchDirectory(clients, 'T01ACME', new Date('2026-10-20T18:30:00Z'))
    assert.equal(fetched.saved_at, '2026-10-20T18:30:00.000Z')
    assert.deepEqual(fetched.channels, [{ id: 'C01RANDOM', name: 'random' }])
    assert.equal(fetched.usergroups[0]?.handle, 'profesores')
  })

  it('keeps the users when only the optional lookups fail', async () => {
    const fetched = await fetchDirectory(
      {
        ...clients,
        listAllChannels: async () => {
          throw new Error('missing_scope')
        },
        listUsergroups: async () => {
          throw new Error('missing_scope')
        },
      },
      'T01ACME',
    )
    assert.equal(fetched.users.length, 1)
    assert.deepEqual(fetched.channels, [])
    assert.deepEqual(fetched.usergroups, [])
  })

  it('fails when Slack answers nothing at all', async () => {
    await assert.rejects(
      () =>
        fetchDirectory(
          {
            listUsers: async () => {
              throw new Error('ratelimited')
            },
            listAllChannels: async () => {
              throw new Error('ratelimited')
            },
            listUsergroups: async () => [],
          },
          'T01ACME',
        ),
      /ratelimited/,
    )
  })
})

describe('DirectoryStore', () => {
  it('writes the directory 0600 atomically and reads it back', async () => {
    const root = await mkdtemp(join(tmpdir(), 'slack-browser-mcp-directory-'))
    try {
      const store = new DirectoryStore(root)
      const file = await store.write(directory())
      assert.equal((await stat(file)).mode & 0o777, 0o600)
      assert.deepEqual(await store.read('T01ACME'), directory())
      assert.equal(await store.read('T99MISSING'), undefined)
    } finally {
      await rm(root, { recursive: true, force: true })
    }
  })

  it('treats a corrupt file as missing instead of crashing a read', async () => {
    const root = await mkdtemp(join(tmpdir(), 'slack-browser-mcp-directory-'))
    try {
      const store = new DirectoryStore(root)
      await mkdir(join(root, 'T01ACME'), { recursive: true })
      await writeFile(join(root, 'T01ACME', 'directory.json'), 'not json', 'utf8')
      assert.equal(await store.read('T01ACME'), undefined)
    } finally {
      await rm(root, { recursive: true, force: true })
    }
  })
})

describe('loadDirectory', () => {
  it('writes a fresh directory when none is stored', async () => {
    const root = await mkdtemp(join(tmpdir(), 'slack-browser-mcp-directory-'))
    try {
      const store = new DirectoryStore(root)
      const loaded = await loadDirectory({ client: clients, teamId: 'T01ACME', store })
      assert.equal(loaded?.users[0]?.id, 'U02ANA')
      assert.match(await readFile(join(root, 'T01ACME', 'directory.json'), 'utf8'), /ana/)
    } finally {
      await rm(root, { recursive: true, force: true })
    }
  })

  it('does not call Slack while the copy is fresh', async () => {
    const root = await mkdtemp(join(tmpdir(), 'slack-browser-mcp-directory-'))
    try {
      const store = new DirectoryStore(root)
      await store.write(directory({ saved_at: '2026-10-20T18:00:00.000Z' }))
      let calls = 0
      const loaded = await loadDirectory({
        client: {
          ...clients,
          listUsers: async () => {
            calls += 1
            return []
          },
        },
        teamId: 'T01ACME',
        store,
        now: new Date(Date.parse('2026-10-20T18:00:00.000Z') + DIRECTORY_MAX_AGE_MS - 1000),
      })
      assert.equal(calls, 0)
      assert.equal(loaded?.saved_at, '2026-10-20T18:00:00.000Z')
    } finally {
      await rm(root, { recursive: true, force: true })
    }
  })

  it('refreshes a stale copy and falls back to it when the refresh fails', async () => {
    const root = await mkdtemp(join(tmpdir(), 'slack-browser-mcp-directory-'))
    try {
      const store = new DirectoryStore(root)
      await store.write(directory({ saved_at: '2026-10-01T00:00:00.000Z' }))

      let calls = 0
      const stale = await loadDirectory({
        client: {
          ...clients,
          listUsers: async () => {
            calls += 1
            throw new Error('offline')
          },
          listAllChannels: async () => {
            throw new Error('offline')
          },
        },
        teamId: 'T01ACME',
        store,
      })
      assert.equal(calls, 1)
      assert.equal(stale?.saved_at, '2026-10-01T00:00:00.000Z')

      const refreshed = await loadDirectory({
        client: clients,
        teamId: 'T01ACME',
        store,
        now: new Date('2026-10-21T00:00:00Z'),
      })
      assert.equal(refreshed?.saved_at, '2026-10-21T00:00:00.000Z')
    } finally {
      await rm(root, { recursive: true, force: true })
    }
  })
})
