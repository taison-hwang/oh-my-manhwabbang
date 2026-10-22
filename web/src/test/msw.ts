/**
 * MSW's unhandled-request check, made to fail the test.
 *
 * `onUnhandledRequest: 'error'` fails the **request**, not the test: `msw/node`
 * rejects the fetch, React Query turns that into an error state nothing reads,
 * and the run stays green while stderr says `intercepted a request without a
 * matching request handler`. Several files used to lean on it as though it
 * were an assertion.
 *
 * `failUnhandled` keeps an HTTP(S) request failing as `'error'` did, and also
 * records it (unlike the string strategy it does not exempt `file:` URLs, which
 * nothing here fetches); `src/test/setup.ts` runs `assertNoUnhandled` after every test and
 * after every file, so a request no handler answers is a red test that names it.
 */

interface Print {
  error(): void
}

let unhandled: string[] = []
let installed = false

export function failUnhandled(request: Request, print: Print): void {
  const url = new URL(request.url)
  unhandled.push(`${request.method} ${url.pathname}${url.search}`)
  print.error()
}

/** Hands back the requests recorded so far and forgets them. */
export function drainUnhandled(): string[] {
  const out = unhandled
  unhandled = []
  return out
}

export function assertNoUnhandled(): void {
  const got = drainUnhandled()
  if (got.length > 0) {
    throw new Error(`MSW: no handler answered ${got.join(', ')}`)
  }
}

type Hook = (fn: () => void) => void

/**
 * Registers the check after every test and after every file. setup.ts calls
 * this with vitest's own hooks. afterAll as well, so a request recorded after a
 * file's last test (an unmount flush) is not dropped with the module; it does
 * not wait for work still in flight, and a request that lands during the next
 * test fails that test instead.
 */
export function installStrictMsw(hooks: { afterEach: Hook; afterAll: Hook }): void {
  hooks.afterEach(assertNoUnhandled)
  hooks.afterAll(assertNoUnhandled)
  installed = true
}

export function strictSetupInstalled(): boolean {
  return installed
}
