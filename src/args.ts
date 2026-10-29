/**
 * Argument syntax. Only the flags the command declares are read, which is what
 * keeps a typo from being taken for a flag the command ignores: `update --chek`
 * stops instead of moving the checkout, and `--send yes` cannot pass for a send.
 */

export interface ParsedArgs {
  positional: string[]
  flags: Map<string, string | boolean>
}

export interface KnownFlag {
  readonly value?: string
}

export function parseArgs(
  tokens: readonly string[],
  known: Readonly<Record<string, KnownFlag>>,
  command: string,
): ParsedArgs {
  const positional: string[] = []
  const flags = new Map<string, string | boolean>()

  for (let index = 0; index < tokens.length; index += 1) {
    const token = tokens[index]!
    if (token === '--') {
      positional.push(...tokens.slice(index + 1))
      break
    }
    if (!token.startsWith('--')) {
      positional.push(token)
      continue
    }
    const body = token.slice(2)
    const equals = body.indexOf('=')
    const name = equals === -1 ? body : body.slice(0, equals)
    if (!Object.hasOwn(known, name)) {
      throw new Error(
        `unknown flag --${name}\nrun \`slack-browser ${command} --help\` to see what it takes`,
      )
    }
    const spec = known[name]!
    if (equals > 0) {
      if (spec.value === undefined) throw new Error(`--${name} takes no value`)
      flags.set(name, body.slice(equals + 1))
      continue
    }
    if (spec.value === undefined) {
      flags.set(name, true)
      continue
    }
    const next = tokens[index + 1]
    if (next === undefined || next.startsWith('--')) {
      throw new Error(`--${name} expects ${spec.value}`)
    }
    flags.set(name, next)
    index += 1
  }

  return { positional, flags }
}
