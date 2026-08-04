# docs/ui-shots — the review reference set

`scripts/e2e.sh` step 11 writes this round's screenshots into `docs/e2e-shots/` and says
*"review against docs/ui-shots/"*. Nothing compares them automatically: Playwright has **no pixel
baselines** in this repo (`toHaveScreenshot` appears nowhere), so the diff is a person's eyes. That is
worth stating plainly, because it means this directory can go stale without any gate turning red.

## Which set is current

| | | |
|---|---|---|
| `v2-prototype/` | **CURRENT.** Measured from `만화방 v2 soft.dc.html` (Claude Design project `ad00fd5c`) at 1440×1024 on 2026-08-04, the source of ruling **E-32**. | compare against this |
| everything else in this directory | **STALE from E-32 onward.** Captured from the v1 prototype `만화방.dc.html`, i.e. the Modernist skin: light-grey ground, red `#ec3013` accent, zero corner radius, single-source drop shadows. | history, not a target |
| `built/` | Implementation results, also pre-E-32. | history |

E-32 replaced the whole visual system — cream `#EAE3D4` ground, deep-teal `#17595B` accent, the retired
brand red kept only as `--color-hot` for "current / selected / focused", radius 3–999px, dual-light
shadows — so **every pre-E-32 shot in here disagrees with the product on purpose.** They are kept rather
than deleted because two sessions have re-derived intent from these files and the record is worth more
than the tidiness.

## What the v2 set does and does not cover

Five screens at one width: library grid, series detail, viewer (chromeless), viewer (chrome awake, with
the thumbnail strip open), settings. That is enough to check the skin's *language* — elevation, radius,
where `--color-hot` is allowed to appear, how the viewer's cream controls sit on its dark ground — and
**not** enough to check the responsive tiers. 768 and 400 are unrepresented here; `docs/e2e-shots/` has
them for the product side.

One thing the screenshots mislead about, so read it before trusting your eyes: **the viewer's bars look
cream and are not.** Measured, the viewer root and both bars are all `#263B38`, the same surface as the
stage (ui-spec §1.4 holds). They read light because the controls fill them and the controls are cream.
The delta E-32 actually makes to the viewer is *controls: bordered ghost → cream fill*, not *ground:
dark → light*.
