/**
 * The application's base path, resolved once from `<base href>` (NFR-SEC-003).
 *
 * The server rewrites `index.html`'s `<base href="/">` to `"{base_path}/"` as it
 * serves the document, so the same build works mounted at `/` or behind a
 * reverse proxy at `/reader/`. Nothing in `src/` may hard-code a leading `/`:
 * the router takes its `basename` from here and `src/api/urls.ts` (WP-06)
 * prefixes every request with it.
 *
 * Resolution is synchronous and happens at module load, before the router is
 * created — a router created with the wrong basename cannot be fixed later
 * without remounting the tree.
 */

/**
 * Normalises a `<base href>` into a base path.
 *
 * Pure and exported so the behaviour is testable without a document: the href
 * may be absolute (`http://host/reader/`), root-relative (`/reader/`) or empty.
 *
 * @returns the path with no trailing slash — `''` for a root mount.
 */
export function resolveBasePath(href: string | null | undefined, origin: string): string {
  if (href === null || href === undefined || href === '') return ''

  let pathname: string
  try {
    pathname = new URL(href, origin).pathname
  } catch {
    // A malformed href is not worth crashing the boot for; a root mount is the
    // only safe guess, and it is also the overwhelmingly common deployment.
    return ''
  }

  // Strip the trailing slash the <base> element requires, and collapse the
  // root case to the empty string so callers can always write `${base}/api/…`.
  const trimmed = pathname.replace(/\/+$/, '')
  return trimmed === '/' ? '' : trimmed
}

/**
 * Turns a base path into a React Router `basename`.
 *
 * React Router wants `'/'` for a root mount, not `''`.
 */
export function toRouterBasename(basePath: string): string {
  return basePath === '' ? '/' : basePath
}

function readBaseHref(): string | null {
  if (typeof document === 'undefined') return null
  const el = document.querySelector('base')
  return el?.getAttribute('href') ?? null
}

function readOrigin(): string {
  if (typeof window === 'undefined') return 'http://localhost'
  return window.location.origin
}

/** `''` for a root mount, otherwise e.g. `'/reader'`. Never has a trailing slash. */
export const BASE_PATH: string = resolveBasePath(readBaseHref(), readOrigin())

/** `'/'` for a root mount, otherwise e.g. `'/reader'`. */
export const ROUTER_BASENAME: string = toRouterBasename(BASE_PATH)
