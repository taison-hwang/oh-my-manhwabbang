import { Button } from '../../components/ds/Button'
import { Hr } from '../../components/ds/Hr'
import type { BookSummary } from '../../api/types'
import { formatLabel, formatPageCount, readToggleLabel } from '../../lib/format'
import type { ResolvedTheme } from '../../lib/theme'

/**
 * End of volume → next volume (FR-VWR-010, ui-spec §6.5).
 *
 * Raised at the last page in 단면/양면; never in 세로, where scrolling past the
 * end *is* the end of the volume.
 *
 * The card deliberately flips back to the **light** palette when the app theme
 * is light — it is a surface floating above the reading ground and the contrast
 * is the point (ui-spec §6.5). It is therefore wrapped in the *app* theme, not
 * the viewer's, which is why `appTheme` is a prop rather than being read off
 * the surrounding `data-theme="dark"`.
 *
 * The `읽음 표시`/`읽음 해제` action is FR-VWR-012's manual half. prd requires the
 * completed state to be changeable by hand and impl-plan §1.1 puts the viewer's
 * copy of that control on this card; the automatic half is the server's
 * `page === page_count` rule on `PUT …/progress`.
 *
 * Ruling **E-12** shapes it: the label names the action in both directions
 * (`안읽음` was a *state*, printed one accent word away from the `권의 마지막
 * 페이지` kicker), and it is a bordered `.btn-secondary` rather than a bare
 * accent `.btn-ghost`, so the card carries exactly one accent field — the
 * primary `다음 권 읽기` — as ui-spec §2.5 requires.
 */
export interface NextVolumeCardProps {
  /** `null` when the series detail has not been fetched or this is the last volume. */
  nextBook: BookSummary | null
  /** Whether the *current* book is marked complete. */
  completed: boolean
  appTheme: ResolvedTheme
  onNext: () => void
  onBackToSeries: () => void
  onToggleCompleted: (completed: boolean) => void
}

export function NextVolumeCard({
  nextBook,
  completed,
  appTheme,
  onNext,
  onBackToSeries,
  onToggleCompleted,
}: NextVolumeCardProps) {
  return (
    <div
      data-role="next-volume-scrim"
      className="absolute inset-0 flex items-center justify-center p-4"
      style={{ background: 'var(--scrim-volume-end)' }}
    >
      <div data-theme={appTheme}>
        <div
          data-role="next-volume-card"
          // `p-4` is ui-spec §6.5's `padding: 16px`; the first cut wrote `p-3`.
          className="flex w-[380px] max-w-full flex-col gap-3 bg-bg p-4 text-ink elev-lg"
        >
          <span className="text-3xs uppercase tracking-[.12em] text-accent">권의 마지막 페이지</span>

          {nextBook !== null && (
            <>
              {/* 700, not 800 (open item `o`). The Claude Design v2 prototype
                  paints this one `font-weight:700;font-size:20px;
                  line-height:1.15` — the only 헤딩 in the app that is not
                  extrabold, because the card is a 380px surface where the
                  volume name is a *label* for the primary action beneath it
                  rather than a title the eye has to find on a busy page. */}
              <span className="font-heading text-h4 font-bold leading-[1.15]">{nextBook.name}</span>
              <span className="text-sm tabular-nums text-ink-muted">
                {`${formatPageCount(nextBook.page_count)} · ${formatLabel(nextBook.kind)}`}
              </span>
            </>
          )}

          <Hr style={{ margin: 0 }} />

          <div className="flex gap-2">
            {nextBook !== null && (
              <Button variant="primary" className="flex-1 justify-start" onClick={onNext}>
                다음 권 읽기
              </Button>
            )}
            <Button variant="secondary" onClick={onBackToSeries}>
              시리즈로
            </Button>
            <Button
              variant="secondary"
              onClick={() => {
                onToggleCompleted(!completed)
              }}
            >
              {readToggleLabel(completed)}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
