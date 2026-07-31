/**
 * The §7.2 error envelope as a typed exception.
 *
 * Every non-2xx response — including from image endpoints — carries
 * `{error: {code, message, detail?}}`. `client.ts` turns that into an `ApiError`
 * and rejects with it; nothing else in the frontend inspects `Response`.
 */

import { ERROR_CODES, type ErrorCode } from './types'

/** Status → code for responses whose body is missing or not a valid envelope. */
const STATUS_TO_CODE: ReadonlyMap<number, ErrorCode> = new Map([
  [400, 'bad_request'],
  [401, 'unauthorized'],
  // Amendment A-11 (ruling E-26). Deliberately **not** folded into
  // `unauthorized`: `isAuthError` below drives the re-authentication path of
  // ruling E-17, and no login lifts a refusal that lives in a YAML key.
  [403, 'forbidden'],
  [404, 'not_found'],
  [409, 'conflict'],
  [422, 'unprocessable'],
  [429, 'rate_limited'],
  [500, 'internal'],
  [501, 'unsupported'],
  [503, 'unavailable'],
])

export function isErrorCode(value: unknown): value is ErrorCode {
  return typeof value === 'string' && (ERROR_CODES as readonly string[]).includes(value)
}

/**
 * Maps an HTTP status onto the frozen code enum, for responses whose body is missing or
 * malformed. A status outside the set falls back to `internal`; `ApiError.status` stays
 * authoritative.
 *
 * `429 → rate_limited` used to be the gap this function apologised for: arch §8.2
 * mandates the login limiter but §7.2 had no code for its answer, so the status was all
 * a client had. Amendment A-9 (ruling E-13) put `rate_limited` in the enum, and the
 * workaround became the contract.
 */
export function codeForStatus(status: number): ErrorCode {
  return STATUS_TO_CODE.get(status) ?? 'internal'
}

/** The §7.2 envelope after parsing, with an unrecognised `code` preserved verbatim. */
export interface ParsedErrorEnvelope {
  /** `null` when the server sent a code outside the frozen enum. */
  code: ErrorCode | null
  rawCode: string | null
  message: string
  detail: Record<string, unknown> | null
}

/** Narrows a parsed JSON body to the §7.2 envelope. Returns `null` when it is not one. */
export function parseErrorEnvelope(body: unknown): ParsedErrorEnvelope | null {
  if (typeof body !== 'object' || body === null) return null
  const envelope: unknown = (body as Record<string, unknown>).error
  if (typeof envelope !== 'object' || envelope === null) return null
  const record = envelope as Record<string, unknown>
  const code: unknown = record.code
  const message: unknown = record.message
  const detail: unknown = record.detail
  return {
    code: isErrorCode(code) ? code : null,
    rawCode: typeof code === 'string' ? code : null,
    message: typeof message === 'string' ? message : '',
    detail: typeof detail === 'object' && detail !== null ? (detail as Record<string, unknown>) : null,
  }
}

export interface ApiErrorInit {
  status: number
  code: ErrorCode
  message: string
  detail?: Record<string, unknown> | null
  /** The `X-Request-Id` of the failing response, when the server sent one. */
  requestId?: string | null
  /** Parsed `Retry-After`, in milliseconds; `null` when the header was absent. */
  retryAfterMs?: number | null
  /** The raw `error.code` when it was not a member of the frozen enum. */
  rawCode?: string | null
}

/** A typed failure carrying the frozen contract's `code` alongside the HTTP status. */
export class ApiError extends Error {
  readonly status: number
  readonly code: ErrorCode
  readonly detail: Record<string, unknown> | null
  readonly requestId: string | null
  readonly retryAfterMs: number | null
  readonly rawCode: string | null

  constructor(init: ApiErrorInit) {
    super(init.message === '' ? `HTTP ${String(init.status)}` : init.message)
    this.name = 'ApiError'
    this.status = init.status
    this.code = init.code
    this.detail = init.detail ?? null
    this.requestId = init.requestId ?? null
    this.retryAfterMs = init.retryAfterMs ?? null
    this.rawCode = init.rawCode ?? null
  }

  /**
   * A string-valued key of the `detail` object, or `null` when it is absent or
   * is some other type. Every consumer of `detail` in the app wants exactly
   * this: the envelope's `detail` is `Record<string, unknown>` by contract, so
   * reading a field off it without narrowing is how an object ends up rendered
   * as `[object Object]`.
   */
  detailString(key: string): string | null {
    const value: unknown = this.detail?.[key]
    return typeof value === 'string' ? value : null
  }

  /** The current `cv` the server reports on `409 stale_version` (arch §5.3). */
  get staleVersion(): string | null {
    const cv: unknown = this.detail?.cv
    return typeof cv === 'string' ? cv : null
  }

  /** The `422 thumb_unavailable` reason (arch §5.5), e.g. `"animated_webp"`. */
  get thumbReason(): string | null {
    return this.detailString('reason')
  }

  /**
   * Which rule the request broke. Amendment A-11 (arch §7.4) puts one here on
   * every `400` and every `403` from the root-editing endpoints — `not_absolute`,
   * `duplicate`, `overlaps`, `disabled`, … — and it is the whole reason the
   * validation is server-side: the user is meant to learn *which* rule, not that
   * there was one.
   */
  get reason(): string | null {
    return this.detailString('reason')
  }

  /**
   * The existing root a `duplicate` or `overlaps` rejection collided with
   * (arch §7.4). The rejected value is never echoed back, so this is the only
   * concrete thing such a message can name.
   */
  get conflictsWith(): string | null {
    return this.detailString('conflicts_with')
  }
}

export function isApiError(value: unknown): value is ApiError {
  return value instanceof ApiError
}

/** True for the two codes the app must not retry blindly. */
export function isAuthError(value: unknown): boolean {
  return isApiError(value) && value.code === 'unauthorized'
}

/**
 * A cover/thumbnail that the server has queued but not yet generated (`202`).
 * It is **not** a contract error: it is thrown only inside `queries.ts` so TanStack
 * Query's retry machinery can honour `Retry-After` (impl-plan §4, rule 3).
 */
export class ImageQueuedError extends Error {
  readonly retryAfterMs: number

  constructor(retryAfterMs: number) {
    super('image queued')
    this.name = 'ImageQueuedError'
    this.retryAfterMs = retryAfterMs
  }
}

export function isImageQueuedError(value: unknown): value is ImageQueuedError {
  return value instanceof ImageQueuedError
}
