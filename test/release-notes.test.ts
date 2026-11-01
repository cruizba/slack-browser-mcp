import assert from 'node:assert/strict'
import { test } from 'node:test'

import { parseLog, renderNotes } from '../src/release/notes.ts'

const RECORD = '\x1e'
const FIELD = '\x1f'

function log(...commits: Array<[hash: string, subject: string, body?: string]>): string {
  return commits
    .map(([hash, subject, body]) => `${hash}${FIELD}${subject}${FIELD}${body ?? ''}${RECORD}\n`)
    .join('')
}

test('groups by type, in a fixed order, and skips the release bump', () => {
  const raw = log(
    ['cccc333', 'chore: release v0.4.0'],
    ['bbbb222', 'fix: a body Slack mangled'],
    ['aaaa111', 'feat(forum): carry the envelope as markdown'],
  )
  assert.equal(
    renderNotes(parseLog(raw)),
    [
      '## Features',
      '',
      '- **forum:** carry the envelope as markdown (`aaaa111`)',
      '',
      '## Fixes',
      '',
      '- a body Slack mangled (`bbbb222`)',
      '',
    ].join('\n'),
  )
})

test('a scope is bold and the hash is cut to seven characters', () => {
  const raw = log(['0123456789abcdef0123456789abcdef01234567', 'fix(cli): refuse a short id'])
  assert.equal(renderNotes(parseLog(raw)), '## Fixes\n\n- **cli:** refuse a short id (`0123456`)\n')
})

test('a breaking footer moves the entry to Breaking changes and shows the reason', () => {
  const body = 'The column is gone.\n\nBREAKING CHANGE: the `--old` flag no longer exists.'
  const raw = log(['aaaa111', 'feat(cli): rename the flag', body])
  assert.equal(
    renderNotes(parseLog(raw)),
    [
      '## Breaking changes',
      '',
      '- **cli:** rename the flag (`aaaa111`)',
      '  the `--old` flag no longer exists.',
      '',
    ].join('\n'),
  )
})

test('the bang alone marks a commit as breaking, without repeating itself', () => {
  const raw = log(['aaaa111', 'feat(forum)!: drop the json envelope'])
  assert.equal(
    renderNotes(parseLog(raw)),
    '## Breaking changes\n\n- **forum:** drop the json envelope (`aaaa111`)\n',
  )
})

test('a subject with no type is kept under Other changes instead of dropped', () => {
  const raw = log(['aaaa111', 'Initial import: Slack web-session access'])
  assert.equal(
    renderNotes(parseLog(raw)),
    '## Other changes\n\n- Initial import: Slack web-session access (`aaaa111`)\n',
  )
})

test('the compare link needs the range and the slug', () => {
  const raw = log(['aaaa111', 'feat: algo'])
  assert.match(renderNotes(parseLog(raw), { from: 'v0.3.2', to: 'v0.4.0' }), /Changes since `v0\.3\.2`\./)
  const withSlug = renderNotes(parseLog(raw), {
    from: 'v0.3.2',
    to: 'v0.4.0',
    slug: 'cruizba/slack-browser-mcp',
  })
  assert.ok(
    withSlug.trimEnd().endsWith(
      'https://github.com/cruizba/slack-browser-mcp/compare/v0.3.2...v0.4.0',
    ),
    'the compare link closes the notes',
  )
  assert.doesNotMatch(renderNotes(parseLog(raw)), /compare/)
})

test('a range with nothing worth reporting says so', () => {
  assert.equal(renderNotes(parseLog(log(['aaaa111', 'chore: release v0.4.0']))), 'No notable changes.\n')
})

test('an empty log and an empty body parse to nothing', () => {
  assert.deepEqual(parseLog(''), [])
  assert.deepEqual(parseLog(`${RECORD}\n`), [])
  assert.deepEqual(parseLog(log(['aaaa111', 'feat: algo'])), [
    { hash: 'aaaa111', subject: 'feat: algo', body: '' },
  ])
})

test('the sections come out in the declared order, not the commit order', () => {
  const raw = log(
    ['a000', 'feat: something'],
    ['b000', 'ci: pin the action'],
    ['c000', 'chore: tidy the scripts'],
    ['d000', 'fix: a crash'],
  )
  const rendered = renderNotes(parseLog(raw))
  const positions = ['## Features', '## Fixes', '## Build and CI', '## Maintenance'].map((title) =>
    rendered.indexOf(title),
  )
  assert.ok(positions.every((at) => at >= 0), 'every section is there')
  assert.deepEqual(positions, [...positions].sort((left, right) => left - right))
})

test('documentation and test commits never reach the notes', () => {
  const raw = log(
    ['a000', 'docs: describe the wire'],
    ['b000', 'test(forum): cover the reader'],
    ['c000', 'feat: something'],
  )
  assert.equal(renderNotes(parseLog(raw)), '## Features\n\n- something (`c000`)\n')
})

test('a breaking footer that runs over several lines keeps all of it', () => {
  const body = 'The flag is gone.\n\nBREAKING CHANGE: `--old` is refused,\nand so is `install.sh --no-old`.\n\nRefs: #12'
  const raw = log(['aaaa111', 'feat(cli)!: drop the old flag', body])
  assert.equal(
    renderNotes(parseLog(raw)),
    [
      '## Breaking changes',
      '',
      '- **cli:** drop the old flag (`aaaa111`)',
      '  `--old` is refused, and so is `install.sh --no-old`.',
      '',
    ].join('\n'),
  )
})
