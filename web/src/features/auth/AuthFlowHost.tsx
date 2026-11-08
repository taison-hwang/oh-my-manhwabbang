import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'

import { subscribeAuthFailure } from '../../api/client'
import { queryKeys } from '../../api/queries'
import type { AuthStatus } from '../../api/types'
import { Button } from '../../components/ds/Button'
import {
  decideAuthAction,
  reauthUrl,
  signInUrl,
  type AuthNotice,
} from './authFlow'

/**
 * Acts on the `401`/`403` answers E-74 gives the SPA (§2.7, §2.9): signs in again with a
 * top-level navigation, or says why a request was refused. Mounted once, beside the shell.
 */
export interface AuthFlowHostProps {
  /** Injected for tests; the browser's `location.assign` by default. */
  assign?: (url: string) => void
  reload?: () => void
  now?: () => number
  returnTo?: () => string
}

const currentLocation = (): string => `${window.location.pathname}${window.location.search}`

const NOTICE_TEXT: Record<Exclude<AuthNotice['kind'], 'reauth' | 'loop'>, string> = {
  admin_only: '관리자만 할 수 있는 작업입니다.',
  cross_origin: '요청이 거부되었습니다. 이 페이지를 다시 열어 주세요.',
}

export function AuthFlowHost({
  assign = (url) => {
    window.location.assign(url)
  },
  reload = () => {
    window.location.reload()
  },
  now = Date.now,
  returnTo = currentLocation,
}: AuthFlowHostProps) {
  const queryClient = useQueryClient()
  const [notice, setNotice] = useState<AuthNotice | null>(null)
  // Several requests are usually in flight when a session ends; the first 401 sends the
  // page away and the rest must not count as a second redirect to the same place.
  const leaving = useRef(false)

  useEffect(
    () =>
      subscribeAuthFailure((error) => {
        if (leaving.current) return
        const status = queryClient.getQueryData<AuthStatus>(queryKeys.auth.status)
        const action = decideAuthAction(error, { status, returnTo: returnTo(), now: now() })
        if (action.kind === 'redirect') {
          leaving.current = true
          assign(action.url)
        } else if (action.kind === 'reload') {
          leaving.current = true
          reload()
        } else if (action.kind === 'notice') {
          setNotice(action.notice)
        }
      }),
    [queryClient, assign, reload, now, returnTo],
  )

  if (notice === null) return null

  const dismiss = (): void => {
    setNotice(null)
  }

  return (
    <div
      role="alert"
      className="fixed inset-x-0 top-0 z-50 flex flex-wrap items-center gap-3 border-b border-rule bg-surface p-3 text-sm"
      data-testid="auth-notice"
    >
      {notice.kind === 'reauth' && (
        <>
          <span>관리자 작업은 다시 로그인해야 합니다. 로그인한 뒤 작업을 다시 눌러 주세요.</span>
          <Button
            variant="primary"
            onClick={() => {
              assign(reauthUrl(returnTo()))
            }}
          >
            다시 로그인
          </Button>
        </>
      )}
      {notice.kind === 'loop' && (
        <>
          <span>로그인이 이어지지 않습니다. 잠시 뒤 다시 시도해 주세요.</span>
          <Button
            variant="primary"
            onClick={() => {
              assign(signInUrl(returnTo()))
            }}
          >
            다시 로그인
          </Button>
        </>
      )}
      {(notice.kind === 'admin_only' || notice.kind === 'cross_origin') && (
        <span>{NOTICE_TEXT[notice.kind]}</span>
      )}
      <Button variant="ghost" onClick={dismiss}>
        닫기
      </Button>
    </div>
  )
}
