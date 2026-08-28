import { useCoverImage } from '../../api/queries'
import type { ContinueItem } from '../../api/types'
import { THUMB_WIDTH_FOR } from '../../api/urls'
import { FallbackCover } from '../../components/ds/FallbackCover'
import { PageStamp } from '../../components/ds/PageStamp'
import { ProgressBar } from '../../components/ds/ProgressBar'
import { formatContinueCounter } from '../../lib/format'
import { textLang } from '../../lib/textLang'

/**
 * `ContinueCard` (ui-spec §9 #3, §4.3) — one card of the 이어보기 track, in the
 * three widths ui-spec §7 specifies: **the full track below `md` (768), 260px
 * at 768–1023, 269px at and above `lg` (1024).**
 *
 * ---------------------------------------------------------------------------
 * E-46 — the card is a filing card now, not a tile
 * ---------------------------------------------------------------------------
 * The 서고 prototype draws this one thing differently from every other card in
 * the product: it is not a raised surface, it is **a sheet out of a 서류철**.
 * Four marks make it that, and all four are the prototype's:
 *
 *  - **kraft, not cream.** `--fill-track` resolves to the very ramp step the
 *    prototype paints this card in (neutral-300, and unlike a raw ramp step it
 *    flips with the theme), which is a shade *below* the surface the shelf uses.
 *    The 이어보기 band stops being a row of tiles and becomes paper on a desk;
 *  - **a punch hole** in the left gutter, in the ground's own colour, so the
 *    gutter reads as the bound edge of a card rather than as padding;
 *  - **ruled lines** behind the text at a 22px pitch. They are `--fill-track-2`,
 *    which lands within three points of the ink-at-9 % the prototype washes them
 *    in, on either theme;
 *  - **the counter is stamped**, not typed (`PageStamp`), and the progress is a
 *    2px rule along the foot rather than a pill (`ProgressBar track="rule"`).
 *
 * The **widths are untouched** by all of it: they are E-37's, `library.test.tsx`
 * pins the class list, and `07-responsive.spec.ts` 6.12 measures the result in a
 * browser. What the skin costs is 10px of text column — the left gutter that
 * holds the punch hole is 22px where the old symmetric padding was 12 — so what
 * the text column gets is now `width − 22 gutter − 96 cover − 12 gap − 12
 * padding` = **127px** at ≥1024, **118px** at 768–1023, and **226px** on a 400px
 * viewport, where the card is the track.
 *
 * The gutter is 22 and not the prototype's 34 for the same reason the cover
 * stays 96 rather than dropping to the prototype's 52: this card is 269px wide
 * where the prototype's is 336, and the two numbers that would have to give to
 * hold the prototype's proportions are both pinned by rulings that were made
 * against measurements — E-37 on the width, and the note below on the cover.
 * A 34px gutter here is a 115px text column, which is under two lines of title.
 *
 * The bottom two were requirements this component had never met. It carried
 * exactly one breakpoint — Tailwind's `md` — so 768–1023 shipped 269, 9px wide
 * of its tier, and `<768` shipped a 218px card that put most of a second one on
 * a 400px screen where §7 asks for one per screen with snap scroll. Neither was
 * a spec error to be amended away: §7's own note records a previous edit that
 * rewrote both cells to match the code and says that was backwards. The snap
 * half lives on `ContinueRow`'s track (`snap-x snap-mandatory md:snap-none`)
 * against `snap-start` here; one without the other is not the cell.
 *
 * 269 is unchanged and stays sourced from here: below 1024 the spec is
 * specifying a layer, at and above it the code is the target (**E-37**).
 *
 * 272 / 336 were **not** E-32's — that attribution was made up and then
 * repeated into ui-spec twice. They arrived in **session 5**, applied
 * `판정 없이` (HANDOFF §1.0e), are in the first commit, and E-32's own commit
 * leaves them byte-identical. **Until E-37 no ruling had ever covered the width
 * of this card**, which is how ui-spec §4.3 came to say `flex:0 0 300px` from
 * day one — a number this file has never held — with nothing to contradict it.
 * That is the defect **E-36** was raised over. So: if the flex-basis below
 * changes, this comment and ui-spec §4.3 / §7 change in the same edit, and
 * `library.test.tsx` will fail until they do.
 *
 * ---------------------------------------------------------------------------
 * The width above was declared for ten sessions and did not hold
 * ---------------------------------------------------------------------------
 * `flex: 0 0 269px` is not a width. It is a *basis*, and a flex item's
 * `min-width` defaults to `auto`, which floors the used size at the item's
 * **min-content** size — so a card whose content cannot be made narrower than
 * 271px is 271px wide no matter what the basis says. The filename below used
 * to be `truncate`, which is `white-space: nowrap`, and the min-content width
 * of a nowrap line is the whole string. So the E-37 width applied to every
 * card whose filename happened to be short and to none of the others.
 *
 * Measured on the real collection at 1440 before the fix: four cards at 269 and
 * `기동전사 건담 외전 - 우주, 섬광의 끝에서 01권 [번역본].zip` at **413** — 144px
 * over its tier, one card in five on that shelf. The instructive width is 400,
 * where the same card is 413px inside a 368px track: §7's `<768` cell asks for
 * one card per screen with snap scroll, and a card wider than the track cannot
 * be one screen. The cell was met by the class list and not by the layout.
 *
 * Two edits, and they answer different halves. `min-w-0` on the button is what
 * makes the basis load-bearing — it takes the automatic minimum off, so 269 /
 * 260 / 100 % are the used sizes rather than floors. `line-clamp-2` on the
 * filename is what makes the text survive that: the previous `truncate` put the
 * whole name on one clipped line, and clamping wraps it to two the way the
 * title above has always wrapped. `break-words` covers the one shape wrapping
 * alone does not — an unbreakable Latin token such as the collection's
 * `Wolf_Guy_-_Wolfen_Crest_v12_JP_完.rar`, which is one 36-character word and
 * would otherwise be clipped mid-token rather than broken.
 *
 * The basis numbers are untouched, so E-37 and ui-spec §4.3 / §7 are unchanged;
 * what changed is that the product now measures what they say. `07-responsive`
 * 6.12 asserts the geometry over **every** card on the shelf rather than the
 * first one, which is the assertion shape that would have caught this: the old
 * one measured a card and this defect needs a card *with a long name in it*.
 *
 * Clicking it resumes that **book** at its saved page, not the series: the whole
 * point of the shelf is that it is one click from where the reader stopped
 * (FR-LIB-010).
 */
export interface ContinueCardProps {
  item: ContinueItem
  onResume: () => void
}

export function ContinueCard({ item, onResume }: ContinueCardProps) {
  const cover = useCoverImage(item.series_id, {
    w: THUMB_WIDTH_FOR.continueCard,
    v: null,
    enabled: item.has_cover,
  })

  // E-45 §6: the denominator is the index's *current* length, not the baseline
  // `progress.page_count` that `isStale` compares against. Since E-45 §2 stopped
  // every write from re-baselining that column the two can disagree, and the old
  // length is wrong in both directions (a grown file reads 100 %, a shrunk one
  // reads a fraction of a book the reader has finished).
  //
  // A `status != "ok"` volume reports `page_count: 0` (arch §4.11) and this
  // divides by it. There is deliberately **no `> 0` guard here**: `ProgressBar`
  // normalises a non-finite `value` to an empty trough, that is written into its
  // contract, and `library.test.tsx`'s "draws an empty bar rather than a full
  // one" case fails the moment it stops doing so. The guard that used to stand
  // here was removable with every test still green — it was a second answer to a
  // question already answered one component down, and a comment claiming it was
  // load-bearing (§6.5 exactly).
  const total = item.book.page_count
  const ratio = item.progress.last_page / total

  return (
    <button
      type="button"
      onClick={onResume}
      aria-label={`${item.series_name} ${item.book.name}`}
      // E-32 turned a 1px hairline into a raised card that lifts; E-46 keeps the
      // lift and moves the ground under it (see the header). The radius goes
      // with the skin: `--radius-lg` is 3px now, and this card is a sheet.
      className="relative flex min-w-0 flex-[0_0_100%] cursor-pointer snap-start gap-3 rounded-md bg-fill-track pb-5 pl-[22px] pr-3 pt-4 text-left shadow-md transition-[box-shadow,transform] duration-150 hover:-translate-y-0.5 hover:shadow-lg md:flex-[0_0_260px] md:snap-align-none lg:flex-[0_0_269px]"
    >
      {/* The punch hole. `--color-bg` and no shadow: the hole is the *ground*
          showing through the card, so it is painted in the ground rather than in
          a darker step of the card, and that reading survives the theme flip
          without a second token. */}
      <span
        aria-hidden="true"
        className="absolute left-[7px] top-4 block h-[9px] w-[9px] rounded-full bg-bg"
      />

      {/* The ruled lines, at the prototype's 22px pitch: one 21px band of nothing
          and one 1px line. They run the width of the content — behind the cover
          as well as behind the text — because that is where they would be on a
          card somebody wrote on, and the cover's own ground hides the part it
          covers. Stopping short of the foot leaves the progress rule alone on
          its line. */}
      <span
        aria-hidden="true"
        className="pointer-events-none absolute bottom-[26px] left-[22px] right-3 top-[38px] bg-[repeating-linear-gradient(to_bottom,transparent_0_21px,var(--fill-track-2)_21px_22px)]"
      />

      {/* 96×144, up from 66×99: the cover is the only thing on this card that
          identifies the book at a glance, and at 66px wide a title in the art
          was unreadable. The 2:3 ratio is unchanged, and E-46 does not shrink it
          back to the prototype's 52 for the reason the header gives. */}
      <span className="relative z-content block h-[144px] w-[96px] flex-[0_0_96px] overflow-hidden rounded-sm bg-fill-track-2 shadow-sm">
        <FallbackCover title={item.series_name} format={item.book.kind} size="row" />
        {cover.status === 'ready' && (
          <img
            src={cover.url}
            alt=""
            className="absolute inset-0 h-full w-full object-cover"
            draggable={false}
          />
        )}
      </span>

      <span className="relative z-content flex min-w-0 flex-1 flex-col gap-[5px]">
        {/* E-32: card titles drop from 800 to 700. Section headings do not. */}
        <span
          className="line-clamp-2 break-words font-heading text-base font-bold leading-[1.2]"
          lang={textLang(item.series_name)}
        >
          {item.series_name}
        </span>
        <span
          className="line-clamp-2 break-words text-xs text-ink-muted"
          lang={textLang(item.book.name)}
        >
          {item.book.name}
        </span>
        <span className="flex-1" />
        {/* Stamped, and stamped at the *right* edge of the card the way a page
            number is stamped at the edge of a page. */}
        <PageStamp className="self-end">
          {formatContinueCounter(item.progress.last_page, total)}
        </PageStamp>
      </span>

      {/* The foot rule spans the content, not the card: it starts where the
          ruled lines start, so the punch-hole gutter stays margin. */}
      <span className="absolute bottom-[14px] left-[22px] right-3 block">
        <ProgressBar value={ratio} height={2} track="rule" label={item.series_name} />
      </span>
    </button>
  )
}
