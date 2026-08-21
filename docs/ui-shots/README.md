# docs/ui-shots — the review reference set

## What is checked now, and what still is not

Two different comparisons are easy to confuse, so they are separated here.

**The product against itself — checked exactly, for most of the set, since session 22.**
`scripts/e2e.sh` step 11c runs `scripts/pixelbaseline`, which holds the SHA-256 of every PNG the synthetic
round writes that was *measured* reproducible, and fails the round when one of them renders differently.
The comparison is byte-exact rather than tolerant, and the exactness is affordable because most of the
render is deterministic: fixed-seed `feTurbulence` grain, a fixture built with a fixed mtime and no `rand`.

**Not all of it, and the tool says so on every run.** Three full rounds were compared: 76 of 121 shots
were byte-identical, 34 carry a value that changes every round (the settings dialog's cache usage and scan
log, a relative time on the series and 이어보기 rows — the product telling the truth about a clock), and
11 differ by 3–18 of 255 across a wide area, which is image decode variance a reader cannot see. A
tolerance loose enough to pass the second group would pass most colour changes too, so those 45 are
excluded **by name, with the measured delta attached**, and the count of what is not being watched is
printed every time. `scripts/pixelbaseline` carries the full derivation.

`docs/pixel-baseline/` holds the manifest and a 320px thumbnail of each watched shot, so a red gate says
*what* moved and not only *that* something did. This is open item `x`; items `bs` (the 45) and `aj`
(below) are what it leaves behind.

**This directory against the product — still a person's eyes.** It cannot be otherwise: the two sides show
different content on purpose (see *What the v3 set covers* below), so there is no pixel comparison to make.
What used to be missing was any way to know this set had gone stale, and that part is now derived rather
than remembered — `docs/pixel-baseline/reference.json` records the renders `built/v3-*.png` was captured
from, and step 11c prints `BUT docs/ui-shots/built/ was captured from different renders and is stale` the
moment the product's renders move past it. It is a line of output and not a failure, because this directory
is gitignored and nothing in a checkout can confirm it exists. That is open item `aj`.

This set has gone stale four times — E-32, session 10, E-42 and E-44 — and each time nothing turned red and
a person had to notice. Check the dates and the derivation before you trust a file as a target.

> **STALE AGAIN — session 14 (2026-08-08), ruling E-42.** Every control in the product changed shape:
> `.btn-secondary`, `.input` and `.seg` are now raised/recessed **cream** surfaces in *both* themes and in
> the viewer, the segmented control lost its dividers and gained a cream track, `.card` gained
> `--shadow-md`, and the viewer's chrome buttons went from bordered ghosts to cream pills. **Any shot in
> this directory that contains a button, a field or a segmented control is out of date**, including the
> `v3-prototype/` set for everything except the prototype's own intent. Nothing turned red for this —
> that is the whole point of the paragraph above. The current renders are in `docs/e2e-shots/` from the
> session-14 round (105 files, all four widths, both themes); they were reviewed by eye and are the
> nearest thing to a target until open item `x` puts a real pixel baseline in place.

## Which set is current

| | | |
|---|---|---|
| `v3-prototype/` | **CURRENT — the design target.** Measured from the session-10 Claude Design prototype at 1440 / 1024 / 768 / 400 on 2026-08-04. | compare against this |
| `built/v3-*.png` | **CURRENT — what the product actually renders.** Captured 2026-08-04 from the binary built out of the session-10 tree, same five screens, same four widths. | compare against this |
| `built/` everything without a `v3-` prefix | **STALE, pre-E-32.** Implementation results from the Modernist skin. | history |
| `v2-prototype/` | **STALE from session 10 onward.** Measured from `만화방 v2 soft.dc.html` at 1440 only; the source of ruling **E-32**. | history |
| `e2e/`, and the loose `*.png` in this directory | **STALE, pre-E-32.** Captured from the v1 prototype `만화방.dc.html` — light-grey ground, red `#ec3013` accent, zero radius, single-source shadows. | history |

Two visual systems have been replaced since the stale files were made, so **every pre-E-32 shot in here
disagrees with the product on purpose.** They are kept rather than deleted because two sessions have
re-derived design intent from them and the record is worth more than the tidiness. The `v3-` prefix, not
the directory, is what tells you a file is current — `built/` deliberately holds both generations.

## What the v3 set covers, and what it does not

Five screens at four widths, 20 files per side, one-to-one by name:

    v3-{1440,1024,768,400}-{library,series,viewer,viewer-chrome,settings}.png

`library` is the landing screen; `series` is one series' detail; `viewer` is the reading screen with the
chrome asleep; `viewer-chrome` is the same with the chrome awake and the thumbnail strip open; `settings`
is the settings dialog. All 40 files are 2× DPR, so 1440 → 2880×2048, 1024 → 2048×2048, 768 → 1536×2048,
400 → 800×2048.

`v3-prototype/` additionally carries `v3-1440-library-notexture.png` and `v3-1440-viewer-notexture.png` —
the same two frames with the paper grain suppressed, for reading colour and edges without the wash.

**Not covered:** every state that is not one of those five. Hover, focus-visible, the command palette, the
sort menu, the shortcuts dialog, empty search, the scanning banner, spread / vertical modes, error panels,
end-of-volume, the 400px drawer on its own, and both themes on the library. `docs/e2e-shots/` reaches many
of those on the product side each round; the prototype side has no equivalent, so for those states there
is no target to compare against at all.

**Not a pixel baseline.** Same widths, but the two sides show different content — the prototype's mock
library holds 24 series with drawn placeholder covers, the product's holds the 12-series synthetic
fixture with generated gradient covers. Compare *language* (elevation, radius, spacing rhythm, where the
accent is allowed to appear), not pixels.

## How the product half was captured

Reproducible; redo it the same way when the look changes again.

1. `make build` from a clean tree — the shipping binary, SPA embedded.
2. `scripts/e2e-config.sh --synthetic --root <abs>/fixture` with `SHELF_E2E_STATE` pointing outside the
   repo. **Never start a server from the repo root:** `shelf.yaml`'s relative `data_dir`/`cache_dir` make
   it write inside the repository. The synthetic fixture (`scripts/mkfixture`) is preferred over the real
   collection because it is reproducible and needs no media volume.
3. `POST /api/scan {"full":true}`, poll `/api/scan/status` to `idle` — 12 series, 40 covers.
4. Chrome, CDP viewport emulation at `{width}x1024x2`, non-mobile, non-touch. **Emulation, not window
   resizing**, because the browser window has a ~500 px floor and 400 is below it.
5. Reaching each screen — what was actually clicked in the product, which is not identical to the
   prototype's chrome:
   - `library` — the landing screen, after one page had been read so the 이어보기 rail is present (the
     prototype's library shows that rail too).
   - `series` — the first card's `상세` button.
   - `viewer` — the series hero's primary CTA. It reads `이어 읽기` rather than `읽기 시작` once progress
     exists; both land on the same book.
   - `viewer-chrome` — `H` to wake the chrome, then the `썸네일 · T` button to open the strip. The chrome
     auto-hides, so it must be **held**: dispatch a `pointerover` (`pointerType: 'mouse'`) on a control
     inside a bar and repeat it until the shutter fires. Two `viewer-chrome` frames were lost to the
     auto-hide before this was added.
   - `settings` — the sidebar's `설정`. At 400 the sidebar is not rendered at all, so the drawer
     (`라이브러리 탐색 열기`) is opened first; the settings dialog then sits on top of the open drawer,
     and that is what the 400 frame shows.
6. Waiting, because this repo has a history of screenshot assertions passing against unpainted frames:
   before every capture, every `<img>` was polled to `complete && naturalWidth > 0`, then `decode()`d,
   then `document.fonts.ready`, then two `requestAnimationFrame`s. On the viewer screens the poll targets
   the page image specifically (`img[src*="/pages/"]`); on `viewer-chrome` it also waits for all six strip
   thumbnails. The chrome's `opacity` was read as `1` on both bars immediately before each shutter.

The fixture's absolute path is visible in the `series` and `settings` frames. That is the scratch
directory the capture ran from, not a product defect.

## The viewer's bars are not cream — and in the product they no longer look it either

Measured in the session-10 product: the viewer root and both bars are all `rgb(38, 59, 56)` = `#263B38`,
the same surface as the stage. ui-spec §1.4 holds. That much of the old warning survives session 10 and is
worth repeating, because the *prototype* shots still make the bars read light.

What changed is why they read that way. **In the prototype the controls are cream-filled** — sampled at
1440, the `뒤로` button's interior is `(235, 230, 220)` against a `(37, 57, 55)` bar. **In the product they
are transparent ghosts with cream text** — the same pixel is `(37, 58, 55)`, i.e. the bar itself; the fill
is `rgba(0,0,0,0)`, the label `#EAE3D4`, the border a warm grey. Selected states are teal `#17595B`, and
`--color-hot` `#EC3013` marks current/selected, on both sides.

So the two halves of this set now disagree on the viewer's whole weight. See the divergence list below.

## What session 10 changed

A **paper grain** now covers every UI surface. It is a `--paper-grain` mask (`feTurbulence`, `seed='4'`)
over a flat `--paper-tone` fill, painted by `body::after` and re-declared per-box over the viewer's chrome;
`web/src/styles/base.css` carries the derivation. Two consequences matter here:

- **The comic artwork is the one surface it does not touch** (`body:has([data-role='viewer'])::after {
  display: none }` plus six per-box rules). The rule is *an opaque UI surface gets paper; the drawing does
  not.* The bars, the thumbnail strip, the "press H" pill, the error panel and the end-of-volume card all
  get it; the page does not.
- **It is deterministic.** `feTurbulence`'s output is pinned by the SVG spec, so two loads produce
  byte-identical pixels. Verified in the shipping product, not just the prototype: the 400 px viewer
  captured, hard-reloaded, and captured again gave the same MD5 and an empty difference bounding box
  (max channel delta 0).

That last point is new capability, and it is the reason to record it: **a pixel baseline is now possible
where before it was not.** A `toHaveScreenshot` suite over these five screens would turn the top of this
file from a standing warning into a gate. It has deliberately **not** been built — it is an option for a
future session, and it would need a decision about the fixture's absolute paths and the clock-dependent
strings ("2분 전 완료", the scan-log timestamps) before it could be stable.

## Where the prototype and the product visibly disagree

Found while pairing the two sets. Not rulings — a list for whoever looks next.

1. **Viewer chrome weight.** Prototype: control groups are cream-filled pills on the dark bar. Product:
   transparent ghosts with cream text. Measured above. This is the largest difference in the set and it is
   present at all four widths.
2. **Settings backdrop.** The prototype blurs the library behind the dialog until it is unreadable. The
   product only dims it — `backdrop-filter` appears nowhere in `web/src`. Visible in every `settings` pair.
3. **Thumbnail strip.** Prototype: page number *below* each thumbnail, the strip centred on the current
   page, with ◀ ▶ affordances and a scroll thumb. Product: number badge *inside* each thumbnail's bottom
   left, left-aligned, no arrows.
4. **Volume tiles on the series screen.** Prototype labels a volume by ordinal (`1권` / `202p`); the
   product labels it by filename (`Clover 클로버 1.zip` / `6p · 5 KB`).
5. **400 px toolbar rows.** Prototype takes three rows (search / `SORT` + select / grid-list). Product
   takes two, dropping the `SORT` label and putting the select beside the toggle.
6. **Series hero, no-cover state.** The prototype's placeholder is a dashed drop target reading
   `Drop cover`. Unverified against the product — the fixture series used here has a cover, so no product
   frame in this set shows that state.
7. **Progress with no progress.** Prototype prints `—`; product prints `0%`.
8. **Cache block in settings.** Prototype shows `1.84 GB / 4.00 GB` with a filled bar; the product shows a
   bare `19 MB`, an empty bar, and a `THUMBS / PDF / WAZERO` breakdown the prototype has no equivalent of.

Not differences, so do not file them: the `⌘K` / `Ctrl K` chip (the prototype was captured on macOS), the
series and root counts, the cover art, and the fixture's absolute paths.
