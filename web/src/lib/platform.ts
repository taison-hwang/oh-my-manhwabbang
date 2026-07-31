/**
 * Platform sniffing, for the one thing it is legitimately needed for: printing
 * `⌘K` on Apple keyboards and `Ctrl K` everywhere else (ui-spec §4.2).
 *
 * The user-agent string rather than `navigator.platform` (deprecated) or
 * `userAgentData` (Chromium-only). The consequence of guessing wrong is one
 * wrong glyph in a hint chip, so the cheap check is the right one — this must
 * never be used to gate behaviour.
 */
export function isApplePlatform(): boolean {
  if (typeof navigator === 'undefined') return false
  return /Mac|iPhone|iPad|iPod/.test(navigator.userAgent)
}

/** `⌘K` or `Ctrl K`. */
export function commandKeyHint(): string {
  return isApplePlatform() ? '⌘K' : 'Ctrl K'
}
