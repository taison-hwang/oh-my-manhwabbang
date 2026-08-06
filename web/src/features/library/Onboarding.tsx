import { Plus } from 'lucide-react'
import { useState } from 'react'

import { Button } from '../../components/ds/Button'
import { Hr } from '../../components/ds/Hr'
import { Wordmark } from '../../components/ds/Wordmark'
import { AddRootForm } from '../roots/AddRootForm'

/**
 * First run: no roots registered (ui-spec §4.6), **as amended by C-5 / E-3 and
 * then by A-11 / ruling E-26**.
 *
 * The screen now has two forms, and which one it shows is not a preference:
 *
 *  * **`root_editing_enabled` false** — the C-5 / E-3 screen, unchanged. There
 *    is no endpoint to call, so the prototype's `루트 추가` stays *removed*
 *    rather than disabled, and `설정 파일 위치 보기` is the answer: it copies
 *    the resolved config path, which is the thing the user actually needs.
 *  * **`root_editing_enabled` true** — the design's own screen, whose primary
 *    button is `+ 루트 추가`. The form it opens is the same one the settings
 *    panel uses, so the validation, the messages and the "restart to read it"
 *    sentence are written once.
 *
 * The capability arrives the same way `config_path` does: down
 * `GET /api/settings`, read by `LibraryPage`, handed here. It is a **required**
 * prop and not an optional one — the defect ruling E-25 was written about was a
 * prop that worked and a caller that never supplied it, and a required prop is
 * the one form of that the compiler can catch.
 *
 * The layout is the design's whole argument: flush left, never centred, a 42px
 * heading over a 2px rule. design.md principle 3 — an empty state here is a
 * normal state, not an apology.
 */
export interface OnboardingProps {
  /**
   * The resolved config file path — `Settings.server.config_path` (amendment
   * **A-10**, ruling **E-25**), supplied by `LibraryPage` from `useSettings`.
   *
   * **There is no default.** `LibraryPage` renders this screen the moment
   * `GET /api/roots` answers with no roots, which can be before
   * `GET /api/settings` answers at all, and the earlier default `'shelf.yaml'`
   * filled that window with the exact string E-25 exists to delete: a bare file
   * name, put on screen and on the clipboard as if it were an address, when the
   * lookup order has four candidates.
   *
   * `undefined` (not answered yet) and `''` (a server built from a
   * configuration with no file — arch §7.8 permits it) are the same fact: **no
   * file is known**. Neither is rendered, because an empty line is a claim too.
   */
  configPath?: string
  /**
   * `Settings.server.root_editing_enabled` (amendment **A-11**). Required, so
   * that a caller cannot forget it the way `configPath`'s caller once did.
   */
  rootEditingEnabled: boolean
  onOpenSettings: () => void
}

export function Onboarding({ configPath, rootEditingEnabled, onOpenSettings }: OnboardingProps) {
  const [adding, setAdding] = useState(false)
  // The one place the two unknowns are folded into one. `null` from here on
  // means "this screen cannot name a file", and nothing below invents one.
  const resolvedPath = configPath === undefined || configPath === '' ? null : configPath

  const copyPath = (): void => {
    // Unreachable while the button is disabled; kept because the guarantee is
    // "nothing misleading is ever copied", and that must not depend on which
    // control happens to call this.
    if (resolvedPath === null) return
    // `navigator.clipboard` is typed as always present but is genuinely absent
    // in jsdom and over plain http in some browsers; the path stays on screen
    // either way, which is the actual answer to "where is the config file".
    const { clipboard } = navigator as { clipboard?: Clipboard }
    if (clipboard === undefined) return
    void clipboard.writeText(resolvedPath)
  }

  return (
    <div className="flex flex-1 items-center justify-center p-8">
      <div className="flex w-full max-w-[520px] flex-col items-start gap-4">
        <Wordmark variant="hero" />
        <h1>읽을 폴더를 등록하세요</h1>
        <Hr className="m-0 w-full" />
        <p className="text-pretty text-lg text-ink-muted">
          ZIP · 폴더 · PDF가 담긴 루트를 지정하면 압축을 풀지 않고 그대로 훑어 시리즈로 정리합니다.
        </p>
        <div className="mt-2 flex gap-2">
          {rootEditingEnabled ? (
            // A-11: there is an endpoint now, so the design's primary button is
            // the primary button. `설정 파일 위치 보기` goes with it — the path
            // is still printed below, and a screen with two primary-weight
            // answers to "what do I do first" has neither.
            <Button
              variant="primary"
              onClick={() => {
                setAdding(true)
              }}
            >
              <Plus size={14} aria-hidden={true} />
              루트 추가
            </Button>
          ) : (
            /* Disabled, not removed, while no file is known — and this project
               holds that a disabled control is a promise, so: the promise here
               is "there will be a location to show", and it is kept by the only
               thing that gates it. `LibraryPage` re-renders this screen with the
               value the instant `GET /api/settings` resolves, one request away.
               That is the opposite of the C-5 / E-3 case in `RootsPanel`, where
               `루트 추가` was *removed* because no endpoint would ever exist to
               make it work. Where the wait does not end — `config_path: ''`,
               which arch §7.8 permits for a server built from a configuration
               with no file — "unavailable" is simply true: there is no location,
               so there is nothing this button could show. The alternatives are
               both worse: an enabled button that silently copies nothing, or one
               that copies a guessed name, which is the E-25 defect verbatim. */
            <Button variant="primary" onClick={copyPath} disabled={resolvedPath === null}>
              설정 파일 위치 보기
            </Button>
          )}
          <Button variant="secondary" onClick={onOpenSettings}>
            설정
          </Button>
        </div>

        {adding && (
          <AddRootForm
            // A-12: the picker's gate is the same capability that put this form
            // on screen, so reaching this line already implies it. It is passed
            // explicitly rather than defaulted inside the form — the defect E-25
            // was written about was a prop that worked and a caller that never
            // supplied it.
            canBrowse={rootEditingEnabled}
            onDone={() => {
              setAdding(false)
            }}
            onCancel={() => {
              setAdding(false)
            }}
          />
        )}
        {/* E-32: the 2px rule above the paths becomes a recessed well — the
            paths are a quotation of the filesystem, not a footnote to the copy
            above them, and the skin says "read-only input" with `--shadow-inset`
            exactly as `.input` does. */}
        <div className="mt-3 flex w-full flex-col gap-[3px] rounded-lg bg-fill-subtle p-3 text-sm tracking-[.02em] text-ink-dim shadow-inset">
          {/* Rendered only when there is something to render: an empty `<span>`
              would occupy the line where a path belongs and would satisfy any
              test that looked for the element rather than the value. */}
          {resolvedPath !== null && <span data-testid="onboarding-config-path">{resolvedPath}</span>}
          <span>shelf.yaml을 편집한 뒤 재시작하세요</span>
        </div>
      </div>
    </div>
  )
}
