import { useId, useState, type FormEvent } from 'react'

import { Button } from '../../components/ds/Button'
import { Hr } from '../../components/ds/Hr'
import { Input } from '../../components/ds/Input'
import { Wordmark } from '../../components/ds/Wordmark'

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
 * Copy note: ui-spec §9's catalogue has no login strings — the prototype has no
 * login screen. The Korean here is new, and deliberately minimal.
 */
export interface LoginScreenProps {
  /** Rejects with an `Error` whose message is shown to the user. */
  onSubmit: (password: string) => Promise<void>
  pending?: boolean
}

export function LoginScreen({ onSubmit, pending = false }: LoginScreenProps) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const fieldId = useId()

  const handleSubmit = (e: FormEvent<HTMLFormElement>): void => {
    e.preventDefault()
    setError(null)
    onSubmit(password).catch((err: unknown) => {
      setError(err instanceof Error && err.message !== '' ? err.message : '로그인에 실패했습니다.')
    })
  }

  return (
    <div className="flex flex-1 items-center justify-center p-8">
      <form
        onSubmit={handleSubmit}
        className="flex w-full max-w-[520px] flex-col items-start gap-4"
      >
        <Wordmark variant="hero" />
        <h1>비밀번호를 입력하세요</h1>
        <Hr className="m-0 w-full" />
        <div className="w-full">
          <Input
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
    </div>
  )
}
