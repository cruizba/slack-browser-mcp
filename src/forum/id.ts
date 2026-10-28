/**
 * ULID-shaped ids for forum envelopes: 26 characters of Crockford base32, the
 * first ten the millisecond and the rest random, monotonic inside a process.
 *
 * The timestamp prefix is why this is not `randomUUID`: ids are typed by people
 * (`agent-forum accept 01j9z7`) and sorted when two tasks land in the same
 * second, and a ULID gives both without a dependency. Prefix matching is
 * case-insensitive and tolerant of the letters Crockford folds (`I`, `L`, `O`),
 * because a human copying an id out of Slack will get them wrong eventually.
 */

import { randomBytes } from 'node:crypto'

const ALPHABET = '0123456789ABCDEFGHJKMNPQRSTVWXYZ'
const TIME_CHARS = 10

export const ID_LENGTH = 26
export const MIN_PREFIX = 4
const RANDOM_CHARS = ID_LENGTH - TIME_CHARS

let lastTime = -1
let lastRandom = ''

function encode(value: number, length: number): string {
  let rest = value
  let out = ''
  for (let index = 0; index < length; index += 1) {
    out = ALPHABET[rest % 32]! + out
    rest = Math.floor(rest / 32)
  }
  return out
}

function randomChars(length: number): string {
  let out = ''
  for (const byte of randomBytes(length)) out += ALPHABET[byte % 32]
  return out
}

/** Base32 increment from the right, so a same-millisecond id still sorts later. */
function increment(value: string): string {
  const chars = value.split('')
  for (let index = chars.length - 1; index >= 0; index -= 1) {
    const at = ALPHABET.indexOf(chars[index]!)
    if (at < ALPHABET.length - 1) {
      chars[index] = ALPHABET[at + 1]!
      return chars.join('')
    }
    chars[index] = ALPHABET[0]!
  }
  return randomChars(value.length)
}

export function newId(now: number = Date.now()): string {
  if (now > lastTime) {
    lastTime = now
    lastRandom = randomChars(RANDOM_CHARS)
  } else {
    // Same millisecond, or a clock that stepped backwards: keep the last stamp so
    // ids stay ordered even when `Date.now` does not.
    lastRandom = increment(lastRandom)
  }
  return encode(lastTime, TIME_CHARS) + lastRandom
}

export function normalizeId(raw: string): string {
  return raw
    .trim()
    .toUpperCase()
    .replace(/[ILO]/g, (letter) => (letter === 'O' ? '0' : '1'))
}

export function isId(value: string): boolean {
  return value.length === ID_LENGTH && [...value].every((letter) => ALPHABET.includes(letter))
}

/** A prefix short enough to be dangerous (fewer than four characters) never matches. */
export function matchesId(id: string, prefix: string): boolean {
  const wanted = normalizeId(prefix)
  return wanted.length >= MIN_PREFIX && normalizeId(id).startsWith(wanted)
}
