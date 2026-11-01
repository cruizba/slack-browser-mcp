/**
 * Release notes from the commits: the conventional subject grouped into the sections
 * a reader looks for, instead of `gh release create --generate-notes`' flat list of
 * every commit including the release bump itself.
 *
 * The input is `git log --format='%H%x1f%s%x1f%b%x1e'`, so this module never calls
 * git: the shell decides the range and the tests feed a fixture.
 */

export interface Commit {
  hash: string
  subject: string
  /** Everything after the subject: the body and the footers. */
  body: string
}

export interface Entry {
  section: string
  /** Present when the subject carried a `type(scope):` prefix. */
  scope?: string
  summary: string
  hash: string
  /**
   * Present when the commit breaks something: the text of a `BREAKING CHANGE:`
   * footer, or an empty string when only the `!` marker said so. A breaking entry is
   * rendered under `Breaking changes` and nowhere else.
   */
  breaking?: string
}

/** Section order, and the commit type that lands in each. */
const SECTIONS: ReadonlyArray<{ title: string; types: readonly string[] }> = [
  { title: 'Features', types: ['feat'] },
  { title: 'Fixes', types: ['fix'] },
  { title: 'Performance', types: ['perf'] },
  { title: 'Refactoring', types: ['refactor'] },
  { title: 'Documentation', types: ['docs'] },
  { title: 'Tests', types: ['test'] },
  { title: 'Dependencies', types: ['deps'] },
  { title: 'Build and CI', types: ['build', 'ci'] },
  { title: 'Maintenance', types: ['chore', 'style'] },
]

const OTHER = 'Other changes'
const BREAKING_TITLE = 'Breaking changes'

const SUBJECT = /^([a-z]+)(?:\(([^)]+)\))?(!)?: (\S.*)$/
const RELEASE = /^chore(\(.+\))?: release v\d+\.\d+\.\d+/
const BREAKING_FOOTER = /^BREAKING[ -]CHANGE:\s*(.+)$/im

export function parseLog(raw: string): Commit[] {
  return raw
    .split('\x1e')
    .map((record) => record.replace(/^\n+/, ''))
    .filter((record) => record.trim() !== '')
    .map((record) => {
      const [hash = '', subject = '', body = ''] = record.split('\x1f')
      return { hash: hash.trim(), subject: subject.trim(), body: body.trim() }
    })
}

export function entries(commits: readonly Commit[]): Entry[] {
  const out: Entry[] = []
  for (const commit of commits) {
    if (RELEASE.test(commit.subject)) continue
    const footer = BREAKING_FOOTER.exec(commit.body)?.[1]?.trim()
    const match = SUBJECT.exec(commit.subject)
    if (!match) {
      out.push({
        section: OTHER,
        summary: commit.subject,
        hash: commit.hash,
        ...(footer ? { breaking: footer } : {}),
      })
      continue
    }
    const [, type = '', scope, bang, summary = ''] = match
    const breaking = footer ?? (bang ? '' : undefined)
    out.push({
      section: SECTIONS.find((section) => section.types.includes(type))?.title ?? OTHER,
      summary,
      hash: commit.hash,
      ...(scope ? { scope } : {}),
      ...(breaking !== undefined ? { breaking } : {}),
    })
  }
  return out
}

export interface RenderOptions {
  /** The tag the range starts after, for the compare link. */
  from?: string
  /** The tag being released. */
  to?: string
  /** `owner/repo`, for the compare link. Omitted when unknown. */
  slug?: string
}

export function renderNotes(commits: readonly Commit[], options: RenderOptions = {}): string {
  const all = entries(commits)
  const breaking = all.filter((entry) => entry.breaking !== undefined)
  const rest = all.filter((entry) => entry.breaking === undefined)
  const blocks: string[] = []
  if (breaking.length > 0) blocks.push(section(BREAKING_TITLE, breaking))
  for (const { title } of SECTIONS) {
    const items = rest.filter((entry) => entry.section === title)
    if (items.length > 0) blocks.push(section(title, items))
  }
  const other = rest.filter((entry) => entry.section === OTHER)
  if (other.length > 0) blocks.push(section(OTHER, other))

  if (blocks.length === 0) blocks.push('No notable changes.')
  const compare = compareLine(options)
  if (compare) blocks.push(compare)
  return `${blocks.join('\n\n')}\n`
}

function section(title: string, items: readonly Entry[]): string {
  return `## ${title}\n\n${items.map(bullet).join('\n')}`
}

function bullet(entry: Entry): string {
  const scope = entry.scope ? `**${entry.scope}:** ` : ''
  const line = `- ${scope}${entry.summary} (\`${entry.hash.slice(0, 7)}\`)`
  if (!entry.breaking) return line
  return `${line}\n${indent(entry.breaking)}`
}

function indent(text: string): string {
  return text
    .split('\n')
    .map((line) => `  ${line}`)
    .join('\n')
}

function compareLine(options: RenderOptions): string {
  const { from, to, slug } = options
  if (!from || !to) return ''
  if (!slug) return `Changes since \`${from}\`.`
  return `**Full changelog**: https://github.com/${slug}/compare/${from}...${to}`
}
