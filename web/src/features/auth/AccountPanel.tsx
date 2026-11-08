import { useCachedAuthStatus } from '../../api/queries'
import { authEndSessionUrl, authLogoutUrl } from '../../api/urls'
import { Button } from '../../components/ds/Button'
import { shortSub } from './authFlow'

/**
 * 계정 — who is signed in and the two logouts of E-74 §2.12. Both are top-level form
 * POSTs: 모든 서비스에서 로그아웃 answers with a 302 to another origin, which `fetch`
 * cannot follow into a page. Shown in oidc mode only.
 */
export function AccountPanel() {
  const status = useCachedAuthStatus()
  if (status?.mode !== 'oidc' || status.user === undefined) return null
  const { user } = status

  return (
    <section className="flex flex-col gap-2" data-testid="account-panel">
      <h3 className="h6">계정</h3>
      <p className="text-xs text-ink-muted">
        <span title={user.sub}>{shortSub(user.sub)}</span>
        {user.is_admin && ' · 관리자'}
      </p>
      <div className="flex flex-wrap gap-2">
        <form method="post" action={authLogoutUrl()} encType="application/x-www-form-urlencoded">
          <Button type="submit" variant="secondary">
            로그아웃
          </Button>
        </form>
        {status.end_session_available && (
          <form
            method="post"
            action={authEndSessionUrl()}
            encType="application/x-www-form-urlencoded"
          >
            <Button type="submit" variant="secondary">
              모든 서비스에서 로그아웃
            </Button>
          </form>
        )}
      </div>
    </section>
  )
}
