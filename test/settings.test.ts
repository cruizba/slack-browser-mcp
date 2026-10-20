import assert from 'node:assert/strict'
import { chmod, mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { after, before, beforeEach, describe, it } from 'node:test'

import {
  FileCredentialStore,
  workspaceLabels,
  type SlackAccount,
} from '../src/auth/store.ts'
import { FileSettingsStore, ensureDefaultWorkspace } from '../src/config/settings.ts'
import { StoreError } from '../src/errors.ts'

let root: string
let store: FileCredentialStore
let settings: FileSettingsStore
let settingsFile: string

function account(team_id: string, team_name: string, team_domain?: string): SlackAccount {
  return {
    team_id,
    team_name,
    ...(team_domain ? { team_domain } : {}),
    token: 'xoxc-1234567890-abcdef',
    d_cookie: 'xoxd-abcdef%2Bghij',
    d_cookie_name: 'd',
    saved_at: '2026-10-20T15:00:00.000Z',
  }
}

before(async () => {
  root = await mkdtemp(join(tmpdir(), 'slack-browser-mcp-settings-'))
})

beforeEach(async () => {
  await rm(join(root, 'accounts'), { recursive: true, force: true })
  await rm(settingsFile ?? join(root, 'config.json'), { force: true })
  settingsFile = join(root, 'config.json')
  settings = new FileSettingsStore(settingsFile)
  store = new FileCredentialStore(join(root, 'accounts'), settings)
  delete process.env.SLACK_BROWSER_MCP_TEAM
})

after(async () => {
  delete process.env.SLACK_BROWSER_MCP_TEAM
  await rm(root, { recursive: true, force: true })
})

describe('FileSettingsStore', () => {
  it('treats a missing file as empty settings', async () => {
    assert.deepEqual(await settings.read(), {})
  })

  it('writes the default 0600 and reads it back', async () => {
    await settings.setDefaultWorkspace('T01OPENVIDU')
    const mode = (await stat(settingsFile)).mode & 0o777
    assert.equal(mode, 0o600)
    assert.deepEqual(await settings.read(), { default_workspace: 'T01OPENVIDU' })
    assert.match(await readFile(settingsFile, 'utf8'), /"default_workspace": "T01OPENVIDU"/)
  })

  it('drops the key when the default is cleared', async () => {
    await settings.setDefaultWorkspace('T01OPENVIDU')
    await settings.setDefaultWorkspace(null)
    assert.deepEqual(await settings.read(), {})
    assert.equal((await readFile(settingsFile, 'utf8')).trim(), '{}')
  })

  it('names the file when the JSON is broken', async () => {
    await writeFile(settingsFile, '{"default_workspace": ', 'utf8')
    await assert.rejects(() => settings.read(), (error: unknown) => {
      assert.ok(error instanceof StoreError)
      assert.match(error.message, /not valid JSON/)
      assert.match(error.message, /config\.json/)
      return true
    })
  })

  it('refuses unknown keys instead of silently ignoring them', async () => {
    await writeFile(settingsFile, '{"default_workspace": "x", "defaultWorkspace": "y"}', 'utf8')
    await assert.rejects(() => settings.read(), StoreError)
  })
})

describe('ensureDefaultWorkspace', () => {
  it('fills the default the first time a workspace is stored', async () => {
    assert.equal(await ensureDefaultWorkspace(settings, 'T01OPENVIDU'), true)
    assert.deepEqual(await settings.read(), { default_workspace: 'T01OPENVIDU' })
  })

  it('leaves a default the user already chose', async () => {
    await settings.setDefaultWorkspace('T0CODEURJC')
    assert.equal(await ensureDefaultWorkspace(settings, 'T01OPENVIDU'), false)
    assert.deepEqual(await settings.read(), { default_workspace: 'T0CODEURJC' })
  })
})

describe('workspace resolution', () => {
  it('keeps working with a single stored session', async () => {
    await store.save(account('T01OPENVIDU', 'OpenVidu', 'openvidu'))
    assert.equal((await store.resolve()).team_id, 'T01OPENVIDU')
    assert.deepEqual(await store.default(), { team_id: 'T01OPENVIDU', source: 'only' })
  })

  it('asks for a reference when several sessions exist and none is default', async () => {
    await store.save(account('T01OPENVIDU', 'OpenVidu', 'openvidu'))
    await store.save(account('T0PJJ9F1D', 'CodeURJC', 'codeurjc'))
    await assert.rejects(() => store.resolve(), /Several Slack sessions are stored/)
    await assert.rejects(() => store.resolve(), /workspace use/)
    assert.equal(await store.default(), null)
  })

  it('uses the default saved in config.json', async () => {
    await store.save(account('T01OPENVIDU', 'OpenVidu', 'openvidu'))
    await store.save(account('T0PJJ9F1D', 'CodeURJC', 'codeurjc'))
    await settings.setDefaultWorkspace('T0PJJ9F1D')
    assert.equal((await store.resolve()).team_id, 'T0PJJ9F1D')
    assert.deepEqual(await store.default(), { team_id: 'T0PJJ9F1D', source: 'config' })
  })

  it('lets an explicit reference win over env and config', async () => {
    await store.save(account('T01OPENVIDU', 'OpenVidu', 'openvidu'))
    await store.save(account('T0PJJ9F1D', 'CodeURJC', 'codeurjc'))
    await settings.setDefaultWorkspace('T01OPENVIDU')
    process.env.SLACK_BROWSER_MCP_TEAM = 'T0PJJ9F1D'
    assert.equal((await store.resolve('OpenVidu')).team_id, 'T01OPENVIDU')
  })

  it('lets SLACK_BROWSER_MCP_TEAM win over config.json', async () => {
    await store.save(account('T01OPENVIDU', 'OpenVidu', 'openvidu'))
    await store.save(account('T0PJJ9F1D', 'CodeURJC', 'codeurjc'))
    await settings.setDefaultWorkspace('T01OPENVIDU')
    process.env.SLACK_BROWSER_MCP_TEAM = 'codeurjc'
    assert.equal((await store.resolve()).team_id, 'T0PJJ9F1D')
    assert.deepEqual(await store.default(), { team_id: 'T0PJJ9F1D', source: 'environment' })
  })

  it('explains a default that points at a workspace that is no longer stored', async () => {
    await store.save(account('T01OPENVIDU', 'OpenVidu', 'openvidu'))
    await settings.setDefaultWorkspace('T0GONE')
    await assert.rejects(() => store.resolve(), /No Slack session matches "T0GONE"/)
    await assert.rejects(() => store.resolve(), /workspace use/)
  })
})

describe('workspaceLabels', () => {
  it('maps team ids to names', async () => {
    await store.save(account('T01OPENVIDU', 'OpenVidu'))
    const labels = await workspaceLabels(store)
    assert.equal(labels.get('T01OPENVIDU'), 'OpenVidu')
  })

  it('degrades to an empty map instead of failing on a broken account file', async () => {
    await store.save(account('T01OPENVIDU', 'OpenVidu'))
    await chmod(join(root, 'accounts', 'T01OPENVIDU.json'), 0o644)
    assert.equal((await workspaceLabels(store)).size, 0)
  })
})
