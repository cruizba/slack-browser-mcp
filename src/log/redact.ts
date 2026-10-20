/**
 * Slack secrets must not reach logs, tool results or error messages. Anything
 * that leaves this process goes through `redact`.
 */

const SECRET_PATTERNS: Array<[RegExp, string]> = [
  [/(^|[?&\s])token=[^&\s"']+/g, '$1token=<redacted>'],
  [/xox[a-z]-[A-Za-z0-9%_.-]{3,}/g, 'xox?-<redacted>'],
  [/(Bearer\s+)[A-Za-z0-9%_.-]{8,}/g, '$1<redacted>'],
]

export function redact(value: string): string {
  let result = value
  for (const [pattern, replacement] of SECRET_PATTERNS) {
    result = result.replace(pattern, replacement)
  }
  return result
}
