/**
 * Slack stores `&`, `<` and `>` as HTML entities and hands `text` back that way, so
 * a reader meets `&lt;` where the author typed `<`. Both directions live here so a
 * sender can measure the text a receiver will decode instead of the text it wrote.
 *
 * Only those three are escaped in this direction: they are the ones Slack actually
 * rewrites, and predicting more would make a sender refuse bodies that arrive
 * intact. The decoder is wider on purpose — it also undoes `&quot;` and `&#39;`,
 * which other clients and older messages carry — because unescaping something that
 * was never escaped only matters if the text wanted the entity verbatim.
 */

const ESCAPED: ReadonlyArray<readonly [RegExp, string]> = [
  [/&/g, '&amp;'],
  [/</g, '&lt;'],
  [/>/g, '&gt;'],
]

/** `&amp;` last: unescaping it first would turn `&amp;lt;` into `<` and lose the text. */
const UNESCAPED: ReadonlyArray<readonly [RegExp, string]> = [
  [/&lt;/g, '<'],
  [/&gt;/g, '>'],
  [/&#39;/g, "'"],
  [/&quot;/g, '"'],
  [/&amp;/g, '&'],
]

export function escapeEntities(text: string): string {
  let escaped = text
  for (const [pattern, entity] of ESCAPED) escaped = escaped.replace(pattern, entity)
  return escaped
}

export function unescapeEntities(text: string): string {
  let plain = text
  for (const [pattern, character] of UNESCAPED) plain = plain.replace(pattern, character)
  return plain
}
