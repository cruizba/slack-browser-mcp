import assert from 'node:assert/strict'
import { chmod, mkdtemp, readFile, rm, stat } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { after, before, describe, it } from 'node:test'

import { FileCredentialStore, type SlackAccount } from '../src/auth/store.ts'
import { FileSettingsStore } from '../src/config/settings.ts'
import { StoreError } from '../src/errors.ts'

let root: string
let store: FileCredentialStore

function account(overrides: Partial<SlackAccount> = {}): SlackAccount {
  return {
    team_id: 'T01OPENVIDU',
    team_name: 'OpenVidu',
    team_domain: 'openvidu',
    user: 'carlos',
    user_id: 'U01CARLOS',
    token: 'xoxc-1234567890-abcdef',
    d_cookie: 'xoxd-abcdef%2Bghij',
    d_cookie_name: 'd',
    saved_at: '2026-10-20T15:00:00.000Z',
    ...overrides,
  }
}

before(async () => {
  root = await mkdtemp(join(tmpdir(), 'slack-browser-mcp-'))
  store = new FileCredentialStore(
    join(root, 'accounts'),
    new FileSettingsStore(join(root, 'config.json')),
  )
})

after(async () => {
  await rm(root, { recursive: true, force: true })
})

describe('FileCredentialStore', () => {
  it('creates the directory 0700 and the account file 0600', async () => {
    await store.save(account())
    const dirMode = (await stat(join(root, 'accounts'))).mode & 0o777
    const fileMode = (await stat(join(root, 'accounts', 'T01OPENVIDU.json'))).mode & 0o777
    assert.equal(dirMode, 0o700)
    assert.equal(fileMode, 0o600)
  })

  it('keeps the secrets out of summaries', async () => {
    const [summary] = await store.list()
    assert.deepEqual(Object.keys(summary ?? {}).sort(), ['team_domain', 'team_id', 'team_name', 'user'])
  })

  it('round-trips an account', async () => {
    const loaded = await store.get('T01OPENVIDU')
    assert.equal(loaded.token, 'xoxc-1234567890-abcdef')
    assert.equal(loaded.d_cookie, 'xoxd-abcdef%2Bghij')
  })

  it('resolves by team id, domain, name and substring', async () => {
    for (const reference of ['T01OPENVIDU', 'openvidu', 'OpenVidu', 'openvi']) {
      const resolved = await store.resolve(reference)
      assert.equal(resolved.team_id, 'T01OPENVIDU', `reference ${reference}`)
    }
  })

  it('refuses a second workspace reference that is ambiguous', async () => {
    await store.save(account({ team_id: 'T02VIDEO', team_name: 'OpenVidu Video', team_domain: 'video' }))
    try {
      await assert.rejects(() => store.resolve('openvi'), StoreError)
      const resolved = await store.resolve('T02VIDEO')
      assert.equal(resolved.team_name, 'OpenVidu Video')
    } finally {
      await store.remove('T02VIDEO')
    }
  })

  it('refuses to read a file whose permissions are wider than 0600', async () => {
    const file = join(root, 'accounts', 'T01OPENVIDU.json')
    await chmod(file, 0o644)
    await assert.rejects(() => store.list(), /permissions are 644/)
    await chmod(file, 0o600)
    assert.equal((await store.list()).length, 1)
  })

  it('never leaves a token in the plain file name or a temp file', async () => {
    const raw = await readFile(join(root, 'accounts', 'T01OPENVIDU.json'), 'utf8')
    assert.match(raw, /xoxc-1234567890-abcdef/)
    assert.deepEqual(await store.list().then((items) => items.length), 1)
  })

  it('removes an account', async () => {
    await store.remove('T01OPENVIDU')
    assert.deepEqual(await store.list(), [])
  })
})
