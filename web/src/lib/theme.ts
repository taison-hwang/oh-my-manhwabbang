/**
 * Theme application (NFR-CMP-003).
 *
 * The user setting is light / dark / system; `system` follows
 * `prefers-color-scheme`. Whatever it resolves to is written as `data-theme` on
 * `<html>`, which is the single switch the whole token layer keys off.
 *
 * The viewer is *not* affected: it renders inside `<div data-theme="dark">`,
 * and because tokens.css scopes the dark ramp with a bare attribute selector
 * that div re-scopes the tokens in both app themes. See tokens.css.
 */

/** What the user chose. Mirrors `Settings.theme` (arch §7.8). */
export type ThemeSetting = 'light' | 'dark' | 'system'

/** What is actually painted. */
export type ResolvedTheme = 'light' | 'dark'

export const DARK_MEDIA_QUERY = '(prefers-color-scheme: dark)'

/** Pure resolution, so the mapping is testable without a matchMedia. */
export function resolveTheme(setting: ThemeSetting, prefersDark: boolean): ResolvedTheme {
  if (setting === 'system') return prefersDark ? 'dark' : 'light'
  return setting
}

/** Reads the OS preference; `false` anywhere `matchMedia` does not exist. */
export function prefersDark(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false
  return window.matchMedia(DARK_MEDIA_QUERY).matches
}

/**
 * Writes the resolved theme onto the document element.
 *
 * Exported taking an explicit root so tests can drive it against a detached
 * document rather than mutating the shared one.
 */
export function applyTheme(setting: ThemeSetting, root?: HTMLElement): ResolvedTheme {
  const resolved = resolveTheme(setting, prefersDark())
  const el = root ?? (typeof document === 'undefined' ? null : document.documentElement)
  if (el) el.setAttribute('data-theme', resolved)
  return resolved
}

/**
 * Subscribes to OS theme changes.
 *
 * Only meaningful while the setting is `system`; the caller re-applies on every
 * change and unsubscribes on teardown. Returns a no-op unsubscribe where
 * `matchMedia` is unavailable (jsdom without a shim, SSR).
 */
export function watchSystemTheme(onChange: (dark: boolean) => void): () => void {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
    return () => undefined
  }
  const mql = window.matchMedia(DARK_MEDIA_QUERY)
  const handler = (e: MediaQueryListEvent): void => {
    onChange(e.matches)
  }
  mql.addEventListener('change', handler)
  return () => {
    mql.removeEventListener('change', handler)
  }
}
