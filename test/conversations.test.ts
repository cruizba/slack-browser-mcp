import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { conversationFilter, conversationLabel, directoryConversation } from '../src/slack/conversations.ts'
import { channelLabel } from '../src/slack/format.ts'
import type { Directory } from '../src/slack/directory.ts'

const directory: Directory = {
  version: 1,
  team_id: 'T01ACME',
  saved_at: '2026-10-20T18:00:00.000Z',
  dm_count: 2,
  users: [
    { id: 'U02ANA', name: 'ana.ruiz', real_name: 'Ana Ruiz', display_name: 'Ana' },
    { id: 'U03LUIS', name: 'luis.g', real_name: 'Luis Gomez' },
    { id: 'U04MICHEL', name: 'michel.maes95', real_name: 'Michel Maes' },
  ],
  channels: [
    { id: 'C01RANDOM', kind: 'channel', name: 'random' },
    { id: 'C02LEADS', kind: 'private', name: 'leads' },
    { id: 'D01ANA', kind: 'dm', user: 'U02ANA' },
    {
      id: 'D02GROUP',
      kind: 'mpim',
      name: 'mpdm-ana.ruiz--luis.g--michel.maes95-1',
    },
  ],
  usergroups: [],
}

describe('conversationLabel', () => {
  it('names a DM after the person instead of showing the id', () => {
    assert.equal(conversationLabel({ id: 'D01ANA' }, directory), 'dm:Ana')
    assert.equal(channelLabel({ id: 'D01ANA', is_im: true, user: 'U02ANA' }, directory), 'dm:Ana')
  })

  it('resolves a DM archived with the peer id as its channel name', () => {
    assert.equal(conversationLabel({ id: 'D99OLD', name: 'U02ANA' }, directory), 'dm:Ana')
  })
  it('falls back to the raw id when the person is unknown', () => {
    assert.equal(conversationLabel({ id: 'D01ANA' }), 'dm:D01ANA')
    assert.equal(conversationLabel({ id: 'D99OLD', name: 'U09UNKNOWN' }, directory), 'dm:U09UNKNOWN')
  })

  it('names a group DM after its members', () => {
    assert.equal(
      conversationLabel({ id: 'D02GROUP' }, directory),
      'group:Ana, Luis Gomez, Michel Maes',
    )
  })

  it('keeps the group DM name when its members cannot be resolved', () => {
    const strangers = { ...directory, users: [] }
    assert.equal(
      conversationLabel({ id: 'C0GROUP', name: 'mpdm-ana.ruiz--luis.g--michel.maes95-1' }, strangers),
      'group:ana.ruiz, luis.g, michel.maes95',
    )
    assert.equal(conversationLabel({ id: 'C0GROUP', name: 'sin-miembros' }), '#sin-miembros')
  })

  it('leaves channels and private channels as they were', () => {
    assert.equal(conversationLabel({ id: 'C01RANDOM' }, directory), '#random')
    assert.equal(conversationLabel({ id: 'C02LEADS' }, directory), 'private:leads')
    assert.equal(conversationLabel({ id: 'C03GONE', name: 'gone' }), '#gone')
    assert.equal(conversationLabel({ id: 'C03GONE' }), '#C03GONE')
  })

  it('keeps a DM and a group DM what they are when only the id arrived', () => {
    assert.equal(channelLabel({ id: 'D01ANA' }, directory), 'dm:Ana')
    assert.equal(channelLabel({ id: 'D02GROUP' }, directory), 'group:Ana, Luis Gomez, Michel Maes')
    assert.equal(channelLabel({ id: 'C01RANDOM' }, directory), '#random')
  })
})

describe('conversationFilter', () => {
  it('prefers the conversation id when the reference is one', () => {
    assert.deepEqual(conversationFilter('D01ANA'), { channel: 'D01ANA' })
  })

  it('takes the id back out of the label a listing printed', () => {
    for (const ref of [
      { id: 'C01RANDOM' },
      { id: 'C02LEADS' },
      { id: 'D01ANA' },
      { id: 'D02GROUP' },
    ]) {
      const printed = `${conversationLabel(ref, directory)} (${ref.id})`
      assert.deepEqual(conversationFilter(printed, directory), { channel: ref.id }, printed)
    }
  })

  it('maps the peer id a DM is archived under to the same DM', () => {
    assert.deepEqual(conversationFilter('dm:U02ANA', directory), { channel: 'D01ANA' })
    assert.deepEqual(conversationFilter('U02ANA', directory), { channel: 'D01ANA' })
  })

  it('maps a named person to their DM', () => {
    assert.deepEqual(conversationFilter('dm:Ana', directory), { channel: 'D01ANA' })
    assert.deepEqual(conversationFilter('@Ana Ruiz', directory), { channel: 'D01ANA' })
  })

  it('falls back to the peer id when the DM is not listed', () => {
    const withoutDms = { ...directory, channels: directory.channels.filter((c) => c.kind !== 'dm') }
    assert.deepEqual(conversationFilter('dm:Ana', withoutDms), { channelName: 'U02ANA' })
  })
  it('still matches channels by name', () => {
    assert.deepEqual(conversationFilter('#random', directory), { channel: 'C01RANDOM' })
    assert.deepEqual(conversationFilter('leads', directory), { channel: 'C02LEADS' })
  })

  it('passes an unknown name through as a channel name', () => {
    assert.deepEqual(conversationFilter('nadie', directory), { channelName: 'nadie' })
    assert.deepEqual(conversationFilter('mpdm-ana.ruiz--luis.g-1'), {
      channelName: 'mpdm-ana.ruiz--luis.g-1',
    })
  })
})

describe('directoryConversation', () => {
  it('resolves a channel by name (exact and substring)', () => {
    const byId = directoryConversation(directory, '#random')
    assert.equal(byId?.id, 'C01RANDOM')
    const byPartial = directoryConversation(directory, 'lead')
    assert.equal(byPartial?.id, 'C02LEADS')
  })

  it('resolves a DM by person name', () => {
    const dm = directoryConversation(directory, 'dm:Ana')
    assert.equal(dm?.id, 'D01ANA')
    const at = directoryConversation(directory, '@Ana')
    assert.equal(at?.id, 'D01ANA')
  })

  it('resolves the printed label and the archived peer id', () => {
    assert.equal(directoryConversation(directory, 'dm:Ana (D01ANA)')?.id, 'D01ANA')
    assert.equal(directoryConversation(directory, 'dm:U02ANA')?.id, 'D01ANA')
    assert.equal(directoryConversation(directory, 'U02ANA')?.is_im, true)
  })

  it('hands back an unknown printed id instead of asking Slack for it', () => {
    assert.deepEqual(directoryConversation(directory, 'dm:Ana (D09GONE)'), { id: 'D09GONE' })
  })

  it('returns the SlackChannel shape with is_im set for DMs', () => {
    const dm = directoryConversation(directory, '#random')
    assert.equal(dm?.is_private, false)
    assert.equal(dm?.is_im, false)
    const mpim = directoryConversation(directory, 'mpdm-ana.ruiz--luis.g--michel.maes95-1')
    assert.equal(mpim?.is_mpim, true)
  })

  it('returns undefined when no entry matches', () => {
    assert.equal(directoryConversation(directory, 'invisible'), undefined)
    assert.equal(directoryConversation(undefined, 'random'), undefined)
  })
})
