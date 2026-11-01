/**
 * The dialect the envelope travels in: a frontmatter block over a markdown body.
 *
 * The shape is YAML's (`key: value` between two `---` lines) and the values are
 * JSON's: a string is always a double-quoted JSON string, an array is a JSON array,
 * a number or a boolean is the bare literal. That combination is deliberate. Real
 * YAML would mean a parser and the rules nobody remembers — `#` starting a comment
 * after a space, `:` splitting a plain scalar, `---` beginning a document — and a
 * task title is exactly where those bite. One writer and one strict reader have no
 * such rules: a value that is not one of the four shapes rejects the document, and
 * the schema in `envelope.ts` still does the validating, so nothing is inferred
 * from how a value looks.
 *
 * The body is everything after the closing line, verbatim. Nothing scans it: it may
 * hold `---`, triple backticks, `#` headings and quotes, which is the whole point of
 * a format whose body is the reason the message exists.
 */

const DELIMITER = '---'
const KEY = /^[A-Za-z][A-Za-z0-9_]*$/
const NUMBER = /^-?\d+(\.\d+)?$/

/**
 * A frontmatter block is a handful of short fields. The cap is here so a 1 MiB body
 * of prose that ends in `---` costs a bounded scan instead of being read to the end
 * looking for a delimiter that was never there.
 */
const MAX_FRONTMATTER_CHARS = 16 * 1024

export type FrontmatterValues = Record<string, unknown>

export interface FrontmatterDocument {
  values: FrontmatterValues
  /** Absent when the document carries no body, or nothing but whitespace. */
  body?: string
}

export function writeFrontmatter(values: FrontmatterValues, body?: string): string {
  const lines = [DELIMITER]
  for (const [key, value] of Object.entries(values)) {
    if (value === undefined) continue
    if (!KEY.test(key)) throw new Error(`${key} is not a frontmatter key`)
    lines.push(`${key}: ${encodeValue(key, value)}`)
  }
  lines.push(DELIMITER)
  const head = `${lines.join('\n')}\n`
  return body === undefined ? head : `${head}\n${body}`
}

function encodeValue(key: string, value: unknown): string {
  if (typeof value === 'string') return JSON.stringify(value)
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  if (Array.isArray(value) && value.every((item) => typeof item === 'string')) {
    return `[${value.map((item) => JSON.stringify(item)).join(', ')}]`
  }
  throw new Error(`${key} does not fit the frontmatter dialect`)
}

export function readFrontmatter(text: string): FrontmatterDocument | undefined {
  if (!text.startsWith(`${DELIMITER}\n`) && !text.startsWith(`${DELIMITER}\r\n`)) {
    return undefined
  }
  const values: FrontmatterValues = {}
  let offset = text.indexOf('\n') + 1
  for (;;) {
    if (offset > MAX_FRONTMATTER_CHARS) return undefined
    const breakAt = text.indexOf('\n', offset)
    if (breakAt < 0) return undefined
    const line = text.slice(offset, breakAt).replace(/\r$/, '')
    const next = breakAt + 1
    if (line === DELIMITER) {
      if (breakAt > MAX_FRONTMATTER_CHARS) return undefined
      // The blank line the writer leaves under the delimiter is a separator, not the
      // start of the body. A body is trimmed by the schema anyway, so dropping one
      // newline cannot change what a reader would have seen.
      const body = text.slice(next).replace(/^\r?\n/, '')
      return { values, ...(body.trim() === '' ? {} : { body }) }
    }
    const entry = readEntry(line)
    if (!entry) return undefined
    const [key, value] = entry
    if (Object.hasOwn(values, key)) return undefined
    values[key] = value
    offset = next
  }
}

function readEntry(line: string): [string, unknown] | undefined {
  const colon = line.indexOf(':')
  if (colon < 1) return undefined
  const key = line.slice(0, colon)
  if (!KEY.test(key)) return undefined
  const raw = line.slice(colon + 1).trim()
  if (raw === '') return undefined
  const head = raw[0]
  if (head === '"' || head === '[') {
    try {
      return [key, JSON.parse(raw)]
    } catch {
      return undefined
    }
  }
  if (raw === 'true') return [key, true]
  if (raw === 'false') return [key, false]
  if (NUMBER.test(raw)) return [key, Number(raw)]
  return undefined
}
