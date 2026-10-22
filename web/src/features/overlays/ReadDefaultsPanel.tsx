import { ChevronsDown, Columns2, File } from 'lucide-react'
import { useReducer } from 'react'

import { useSettings } from '../../api/queries'
import { useSettingsWriter } from '../../api/settingsWriter'
import type { DisplayMode, ReadingDir, SettingsUpdate } from '../../api/types'
import { Seg, type SegOption } from '../../components/ds/Seg'
import type { ThemeSetting } from '../../lib/theme'
import { useUiStore } from '../../store/ui'

/**
 * 읽기 기본값 + 테마 (ui-spec §8.6 §2, prd UI-004).
 *
 * Everything here writes `PUT /api/settings` — these are the *global* defaults
 * a book falls back to when it has no override of its own (arch §7.6). Writes go
 * through `api/settingsWriter.ts`, which patches the settings cache at once; a
 * sweep of changes is one ordered write, so nothing is disabled while a request
 * is on the wire.
 *
 * **A pick re-renders in its own event.** The cache patch is synchronous, but
 * react-query hands it to this component a task later (the observer's result
 * is current at once, but query-core `notifyManager` delivers it to React's
 * subscribed listener with `setTimeout(0)`), and React
 * restores a controlled input to the props it last rendered as soon as the
 * handler returns. With nothing else to render, the click committed the *old*
 * option and a browser could paint that frame before the update arrived — 3/3
 * clicks with a save on the wire, 6/20 rapid toggles without (item 4,
 * 2026-10-20). So each pick also bumps local state: React renders inside the
 * event, and `useSettings` reads the cache *during* render — already patched —
 * so the input is committed with the new value before it is restored. The cache
 * stays the only source of what is shown: a refetch, or the re-read after a
 * refused write, is drawn as it lands, with no local copy to fall out of step.
 * `ReadDefaultsPanel.test.tsx` holds the first half synchronously after the
 * event, and the second against a refusal and another device's change.
 *
 * The theme row replaces the prototype's static `다크 (뷰어 고정)` text with a
 * real 라이트 / 다크 / 시스템 control (NFR-CMP-003, ui-spec gap #2). It writes
 * **twice** on purpose: to the Zustand store, which applies `data-theme` to
 * `<html>` synchronously so the switch is instant, and to the server, which is
 * what makes it survive a reload. The note beside it is the truth the prototype
 * was trying to state — the viewer is `data-theme="dark"` in both app themes.
 *
 * `프리페치 페이지` is capped at **12** per ui-spec §8.6 even though the wire
 * accepts 0..20: the UI truth wins for the control, the contract for validity.
 */
const DIR_OPTIONS = [
  { value: 'ltr', label: 'L→R' },
  { value: 'rtl', label: 'R→L' },
] as const satisfies readonly { value: ReadingDir; label: string }[]

/** C-1: wire values are `single | spread | vertical`; labels are 단면/양면/세로. */
const MODE_OPTIONS: readonly SegOption<DisplayMode>[] = [
  { value: 'single', label: '단면', icon: <File size={13} /> },
  { value: 'spread', label: '양면', icon: <Columns2 size={13} /> },
  { value: 'vertical', label: '세로', icon: <ChevronsDown size={13} /> },
]

const THEME_OPTIONS = [
  { value: 'light', label: '라이트' },
  { value: 'dark', label: '다크' },
  { value: 'system', label: '시스템' },
] as const satisfies readonly { value: ThemeSetting; label: string }[]

export const PREFETCH_MAX = 12

export function ReadDefaultsPanel() {
  const settings = useSettings()
  const save = useSettingsWriter()
  const theme = useUiStore((s) => s.theme)
  const setTheme = useUiStore((s) => s.setTheme)
  /** Renders the pick inside its own event (see the module comment). */
  const [, redraw] = useReducer((n: number) => n + 1, 0)

  const data = settings.data
  const disabled = data === undefined

  const pick = (update: SettingsUpdate) => {
    if (disabled) return
    save(update)
    redraw()
  }

  return (
    <section className="flex min-w-0 flex-1 flex-col gap-3">
      <h6>읽기 기본값</h6>

      <div className="flex items-center gap-3">
        <span className="flex-1 text-base">읽기 방향</span>
        <Seg
          value={data?.reading_direction ?? 'ltr'}
          options={DIR_OPTIONS}
          aria-label="읽기 방향"
          onChange={(reading_direction) => {
            pick({ reading_direction })
          }}
        />
      </div>

      <div className="flex items-center gap-3">
        <span className="flex-1 text-base">표시 모드</span>
        <Seg
          value={data?.display_mode ?? 'single'}
          options={MODE_OPTIONS}
          aria-label="표시 모드"
          onChange={(display_mode) => {
            pick({ display_mode })
          }}
        />
      </div>

      <div className="flex items-center gap-3">
        <label className="flex-1 whitespace-nowrap text-base" htmlFor="settings-prefetch">
          프리페치 페이지
        </label>
        {/*
          The 130px lives on a wrapper, not on the input. `styles/base.css`
          @layer base sets `input[type='range'] { width: 100% }` — an
          element+attribute selector, specificity (0,1,1) — and Tailwind emits a
          plain `.w-\[130px\]` at (0,1,0), which loses no matter the source order
          (Tailwind v3 compiles `@layer` away, so no native cascade layer is left
          to arbitrate). Sizing the parent makes the input's own `width:100%`
          resolve to the 130px ui-spec §8.6 §2 asks for, and `flex-none` stops the
          track from being stretched by the row.
        */}
        <span className="flex w-[130px] flex-none items-center">
          <input
            id="settings-prefetch"
            type="range"
            min={0}
            max={PREFETCH_MAX}
            className="w-full"
            value={data?.prefetch ?? 0}
            disabled={disabled}
            onChange={(e) => {
              pick({ prefetch: Number(e.target.value) })
            }}
          />
        </span>
        <span className="w-[20px] text-right text-base tabular-nums">{data?.prefetch ?? 0}</span>
      </div>

      <div className="flex items-center gap-3">
        <span className="flex-1 text-base">테마</span>
        <span className="text-xs text-ink-dim">뷰어 고정</span>
        <Seg
          value={theme}
          options={THEME_OPTIONS}
          aria-label="테마"
          onChange={(next) => {
            // Local first: `data-theme` must flip on the click, not on the
            // round trip (NFR-CMP-003).
            setTheme(next)
            save({ theme: next })
          }}
        />
      </div>
    </section>
  )
}
