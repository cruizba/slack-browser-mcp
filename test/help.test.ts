import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { describe, it } from 'node:test'

import {
  commandNames,
  commandPage,
  flagSpecs,
  helpPageFor,
  indexPage,
  resolveCommand,
  subcommandNames,
  usageProblem,
} from '../src/help.ts'

const source = await readFile(new URL('../src/cli.ts', import.meta.url), 'utf8')
const dispatch = source.slice(source.indexOf('async function main('), source.indexOf('\nmain()'))
const dispatched = [...dispatch.matchAll(/case '([^']+)':/g)].map((match) => match[1]!)

describe('help table', () => {
  it('covers every command the CLI dispatches', () => {
    assert.ok(dispatched.length > 10, `found ${dispatched.length} cases in the dispatch`)
    for (const command of dispatched) assert.ok(resolveCommand(command), `no page for ${command}`)
    for (const name of commandNames()) assert.ok(dispatched.includes(name), `${name} is not dispatched`)
  })

  it('serves a page for every command and subcommand, with every flag described', () => {
    for (const name of commandNames()) {
      const page = commandPage(name)
      assert.ok(page, name)
      assert.match(page!, /^Usage: slack-browser /)
      assert.doesNotMatch(page!, /undocumented/, name)
      for (const flag of Object.keys(flagSpecs(name))) {
        assert.match(page!, new RegExp(`--${flag}\\b`), `${name} --${flag}`)
      }
      for (const sub of subcommandNames(name)) {
        const subPage = commandPage(name, sub)
        assert.ok(subPage, `${name} ${sub}`)
        assert.match(subPage!, /^Usage: slack-browser /)
        assert.doesNotMatch(subPage!, /undocumented/, `${name} ${sub}`)
      }
      if (subcommandNames(name).length > 0) assert.equal(commandPage(name, 'nope'), undefined)
    }
  })

  it('gives each forum move its own page and its own whitelist', () => {
    // The moves share one dispatch branch, and the help table once listed them all in
    // a single `accept` entry: `result`, `progress`, `decline`, `ack`, `reopen` and
    // `cancel` were then refused as unknown subcommands by the CLI while the MCP
    // surface worked, which is the worst kind of half-broken.
    const moves = ['accept', 'decline', 'progress', 'result', 'ack', 'reopen', 'cancel']
    for (const move of moves) {
      assert.ok(subcommandNames('agent-forum').includes(move), `no subcommand ${move}`)
      assert.match(commandPage('agent-forum', move) ?? '', new RegExp(`agent-forum ${move} `))
      assert.equal(usageProblem('agent-forum', move, ['post']), null, move)
      // The forum verbs live in their own function, outside the `main` dispatch.
      assert.match(source, new RegExp(`case '${move}':`), `${move} is not dispatched`)
    }
    // Per-verb flags: `--fail` belongs to `result` and nowhere else.
    assert.equal(usageProblem('agent-forum', 'result', ['fail']), null)
    assert.match(usageProblem('agent-forum', 'ack', ['fail']) ?? '', /unknown flag --fail/)
  })

  it('lists every command in the index', () => {
    const index = indexPage()
    for (const name of commandNames()) assert.match(index, new RegExp(`slack-browser ${name}\\b`))
  })
})

describe('help flags', () => {
  it('answers --help on its own command', () => {
    assert.match(helpPageFor('update', ['--help']) ?? '', /^Usage: slack-browser update /)
    assert.match(helpPageFor('update', ['-h']) ?? '', /^Usage: slack-browser update /)
  })

  it('follows an alias to the page of the command it names', () => {
    assert.match(helpPageFor('forum', ['--help']) ?? '', /^Usage: slack-browser agent-forum /)
    assert.match(helpPageFor('workspaces', ['list', '--help']) ?? '', /workspace \[list\]/)
  })

  it('takes the subcommand the help flag follows', () => {
    const page = helpPageFor('archive', ['search', '--help'])
    assert.match(page ?? '', /^Usage: slack-browser archive search /)
    assert.doesNotMatch(page ?? '', /Subcommands:/)
  })

  it('leaves --help alone when it is message text', () => {
    assert.equal(helpPageFor('send', ['general', '--help']), undefined)
    assert.equal(helpPageFor('update', ['--', '--help']), undefined)
  })

  it('ignores a command that does not exist', () => {
    assert.equal(helpPageFor('nope', ['--help']), undefined)
    assert.equal(resolveCommand('nope'), undefined)
  })
})

describe('usageProblem', () => {
  it('accepts the flags of the command', () => {
    assert.equal(usageProblem('read', 'general', ['limit', 'workspace']), null)
  })

  it('refuses a flag that belongs to another subcommand', () => {
    assert.match(usageProblem('archive', 'search', ['team']) ?? '', /unknown flag --team/)
  })

  it('refuses a subcommand that does not exist', () => {
    assert.match(usageProblem('archive', 'nope', []) ?? '', /unknown subcommand "nope"/)
  })

  it('says nothing about commands it does not know', () => {
    assert.equal(usageProblem('nope', undefined, ['whatever']), null)
  })
})
