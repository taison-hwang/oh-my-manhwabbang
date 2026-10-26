import { useEffect, useId, useRef, useState, type FormEvent } from 'react'

import { Button } from '../../components/ds/Button'
import { Hr } from '../../components/ds/Hr'
import { Input } from '../../components/ds/Input'
import { Wordmark } from '../../components/ds/Wordmark'
import { loginErrorMessage } from './loginErrors'

/**
 * The optional-auth gate (NFR-SEC-002, arch §7.12).
 *
 * Shown when `GET /api/auth/status` reports `auth_required && !authenticated`.
 * Auth is all-or-nothing and covers static assets too, so this screen replaces
 * the entire shell rather than overlaying it.
 *
 * It does **not** talk to the server: `src/api/client.ts` is the only module
 * that may call `fetch` (D-44), so the submit handler is injected. Rate
 * limiting (5/min per IP), the constant-time compare and the ≥250 ms failure
 * floor all live on the server side, per arch §8.
 *
 * Its root is a `<main>`: App renders it *instead of* the shell, so nothing around
 * it supplies a landmark, and the server-rendered form is a `<main>` too.
 *
 * Copy note: ui-spec §9's catalogue has no login strings — the prototype has no
 * login screen. The heading and labels are new, and deliberately minimal; the
 * failure sentences are the server-rendered form's (`loginErrors.ts`), so the two
 * login surfaces say the same thing.
 */
export interface LoginScreenProps {
  /**
   * Rejects on failure. The rejection is mapped by `loginErrorMessage` — an
   * `ApiError`'s status picks the sentence — and its own `message` is never shown.
   */
  onSubmit: (password: string) => Promise<void>
  pending?: boolean
}

export function LoginScreen({ onSubmit, pending = false }: LoginScreenProps) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const fieldId = useId()
  const fieldRef = useRef<HTMLInputElement>(null)
  // Set by a rejection, spent by the effect below once the field can take focus.
  const refocus = useRef(false)
  // Numbers each submit. `pending` reaches this component a render late, so it is no
  // lock: Enter pressed twice before that render puts two requests in flight. Only the
  // latest may report — an older rejection landing after it would replace the error
  // shown and select a password the user may already be retyping.
  const attempt = useRef(0)

  // While the request is in flight the field and the button are both disabled, so
  // whichever held focus has lost it, and a rejected password would leave the user on
  // <body>. `pending` is the parent's and may still be true when the rejection
  // arrives — focusing a disabled input does nothing — so this waits for both: the
  // error to be set and the field to be enabled, in either order. Selected, so that
  // retyping replaces the rejected password.
  useEffect(() => {
    if (!refocus.current || pending || error === null) return
    refocus.current = false
    fieldRef.current?.focus()
    fieldRef.current?.select()
  }, [pending, error])

  const handleSubmit = (e: FormEvent<HTMLFormElement>): void => {
    e.preventDefault()
    setError(null)
    refocus.current = false
    attempt.current += 1
    const mine = attempt.current
    onSubmit(password).catch((err: unknown) => {
      if (mine !== attempt.current) return
      refocus.current = true
      setError(loginErrorMessage(err))
    })
  }

  return (
    <main className="flex flex-1 items-center justify-center p-8">
      <form
        onSubmit={handleSubmit}
        className="flex w-full max-w-[520px] flex-col items-start gap-4"
      >
        <Wordmark variant="hero" />
        <h1>비밀번호를 입력하세요</h1>
        <Hr className="m-0 w-full" />
        <div className="w-full">
          <Input
            ref={fieldRef}
            id={fieldId}
            label="비밀번호"
            type="password"
            autoComplete="current-password"
            autoFocus
            value={password}
            disabled={pending}
            onChange={(e) => {
              setPassword(e.target.value)
            }}
            aria-invalid={error !== null}
            aria-describedby={error === null ? undefined : `${fieldId}-error`}
          />
        </div>
        {error !== null && (
          <p id={`${fieldId}-error`} role="alert" className="text-sm text-accent-text">
            {error}
          </p>
        )}
        <Button type="submit" variant="primary" disabled={pending || password === ''}>
          로그인
        </Button>
      </form>
    </main>
  )
}
