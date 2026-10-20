import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { renderMessages } from '../src/slack/format.ts'
import { resolveReferences, type ReferenceScope } from '../src/slack/references.ts'

const scope: ReferenceScope = {
  user: (id) => (id === 'U4C140PEW' ? 'mica' : undefined),
  channel: (id) => (id === 'C0PJJ70E6' ? 'random' : undefined),
  usergroup: (id) => (id === 'S01TEACH' ? 'profesores' : undefined),
}

describe('resolveReferences', () => {
  it('turns a mention into the name the directory knows', () => {
    assert.equal(resolveReferences('<@U4C140PEW> tio, miralo', scope), '@mica tio, miralo')
  })

  it('keeps the id when the directory has never seen the user', () => {
    assert.equal(resolveReferences('hola <@U999UNKNOWN>', scope), 'hola @U999UNKNOWN')
  })

  it('prefers the label Slack already embedded in the markup', () => {
    assert.equal(resolveReferences('<@U1|ana> <#C1|general>', undefined), '@ana #general')
  })

  it('resolves channel references by id', () => {
    assert.equal(resolveReferences('mira <#C0PJJ70E6>', scope), 'mira #random')
    assert.equal(resolveReferences('mira <#C0PJJ70E6|random>', scope), 'mira #random')
  })

  it('resolves user groups and special mentions', () => {
    assert.equal(resolveReferences('<!subteam^S01TEACH>', scope), '@profesores')
    assert.equal(resolveReferences('<!subteam^S01TEACH|@profes>', scope), '@profes')
    assert.equal(resolveReferences('<!here> <!channel> <!everyone>', scope), '@here @channel @everyone')
  })

  it('unfolds links the way Slack displays them', () => {
    assert.equal(
      resolveReferences('ver <https://example.com/a|este enlace>', scope),
      'ver este enlace (https://example.com/a)',
    )
    assert.equal(resolveReferences('<https://example.com>', scope), 'https://example.com')
    assert.equal(resolveReferences('<mailto:a@b.c|escribeme>', scope), 'escribeme (mailto:a@b.c)')
  })

  it('falls back to the visible part of a date reference', () => {
    assert.equal(resolveReferences('<!date^1518032400^{date_short}|7 feb 2018>', scope), '7 feb 2018')
  })

  it('unescapes entities and leaves plain text alone', () => {
    assert.equal(resolveReferences('a &amp; b &lt;3', scope), 'a & b <3')
    assert.equal(resolveReferences('nada que resolver', scope), 'nada que resolver')
  })
})

describe('renderMessages', () => {
  it('resolves mentions inline and keeps the attachment id addressable', () => {
    const line = renderMessages(
      [
        {
          ts: '1792519300.197699',
          user: 'U4C140PEW',
          text: '<@U4C140PEW> mira <#C0PJJ70E6> &amp; esta foto',
          files: [{ id: 'F0ABC', name: 'foto.png', mimetype: 'image/png' }],
        },
      ],
      { names: new Map([['U4C140PEW', 'mica']]), scope },
    )
    assert.match(line, /mica: @mica mira #random & esta foto/)
    assert.match(line, /\[file: foto\.png id=F0ABC\]/)
  })
})
