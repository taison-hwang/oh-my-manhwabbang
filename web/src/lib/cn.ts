/** Falsy-tolerant class-name joiner. */
export type ClassValue = string | false | null | undefined

/**
 * Joins class names, dropping anything falsy.
 *
 * Deliberately not `clsx`: the app only ever needs the string/false form, and a
 * dependency the lockfile does not carry is not available to add (WP-00 froze
 * package.json).
 */
export function cn(...parts: ClassValue[]): string {
  let out = ''
  for (const part of parts) {
    if (!part) continue
    out = out === '' ? part : `${out} ${part}`
  }
  return out
}
