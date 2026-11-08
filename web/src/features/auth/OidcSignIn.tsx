import { Button } from '../../components/ds/Button'
import { signInUrl } from './authFlow'

/**
 * Stands where the password form stands, for oidc mode: the SPA is only served to a
 * session (E-74 §2.17), so this shows only when a session ends under an open page.
 */
export function OidcSignIn() {
  return (
    <main className="flex flex-1 items-center justify-center p-8">
      <div className="flex flex-col items-start gap-3">
        <h1 className="h5">세션이 끝났습니다</h1>
        <Button
          variant="primary"
          onClick={() => {
            window.location.assign(signInUrl(`${window.location.pathname}${window.location.search}`))
          }}
        >
          다시 로그인
        </Button>
      </div>
    </main>
  )
}
