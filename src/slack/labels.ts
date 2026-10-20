/**
 * A conversation is printed as `<label> (<id>)`, and an agent hands that line
 * back as the `channel` of the next call. Reading a reference therefore has to
 * accept the decoration it printed, or the label is only readable by a human.
 */

const PRINTED_ID = /\s*\(([CGD][A-Z0-9]{5,})\)$/
const MEMBER_COUNT = /\s+\d+\s+members?$/i

export interface PrintedReference {
  id?: string
  reference: string
}

/**
 * Splits the id out of a printed label, member count included, so
 * `dm:Ana Ruiz (D01ANA) 2 members` reads as the id `D01ANA`.
 */
export function parsePrintedReference(value: string): PrintedReference {
  let reference = value.trim()
  const members = MEMBER_COUNT.exec(reference)
  if (members) reference = reference.slice(0, members.index).trimEnd()
  const printed = PRINTED_ID.exec(reference)
  if (!printed) return { reference }
  return { id: printed[1]!, reference: reference.slice(0, printed.index).trimEnd() }
}
