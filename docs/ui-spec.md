# UI 명세서 — SHELF (아카이브 기반 만화 뷰어)

> Implementation spec for React 18 + TypeScript + Vite + Tailwind CSS.
> Source of truth: the **Modernist** design system (`_ds/modernist-…/styles.css` + `readme.md`) and the
> runnable prototype `SHELF - Comic Archive Reader.html`, rendered live in Chrome and captured in
> [`docs/ui-shots/`](./ui-shots/).
> Companion documents: [`prd.md`](./prd.md) (URD), [`design.md`](./design.md) (design handoff).

**Prototype render status: SUCCESS.** The self-unpacking bundle was served over `http://localhost:8791`,
booted cleanly in Chrome (one benign 404 for a missing asset, no JS errors), and every screen and state
listed below was driven directly through the prototype's own state controller and photographed.
34 screenshots are indexed in §9.

---

## 0. Six things an implementer must not get wrong

> **Item 6 is new (session 14, E-42 §8③).** The list was five for as long as this document has existed;
> the sixth was added because the skin E-32/E-36 adopted speaks its boundaries, its focus marker *and* its
> selected state in `box-shadow`, and there is one rendering mode where `box-shadow` does not exist. The
> numbering of §0.1–§0.5 is unchanged, because other documents and source comments cite it.

1. ~~**Zero corner radius. Everywhere.** `--radius-sm/md/lg` are all `0px` *on purpose*. The only two curved
   things in the entire product are the radio `.dot` and the viewer's loading spinner (both true circles).
   No rounded cards, no rounded buttons, no rounded badges, no rounded inputs.~~

   > **Superseded — E-32 §2 retired D-40.** The radii are now `sm 3 / md 4 / lg 6 / pill 7 / full 999`
   > (`tokens.css`). What survived is the *enforcement*, not the zero: the ban on arbitrary radius still
   > stands and the whitelist is bound to the values the `--radius-*` tokens produce, plus `50%` /
   > `9999px`. Struck rather than deleted, because this sentence and the shadow rule in item 2 outlived
   > E-32 together and that is the lesson (E-36 §2).
2. **Structure is drawn with rules *and* with elevation — never with whitespace alone.** Major section
   boundaries are **2px** (`--rule-strong`); hairlines are **1px** (`--rule`). Never soften a structural
   rule to a hairline and never replace a rule with padding. *(Row separation is the one exception E-32
   made: a list row is no longer a hairline but a rounded `.row-chip` that appears under the pointer —
   `web/src/styles/base.css`, the `.row-chip` block.)*

   **Shadows carry the other half of the structure, and they are a language, not decoration.** Every box
   in the product answers exactly one of five questions, and its `box-shadow` follows from the answer:

   | What the box is | Shadow | Examples |
   |---|---|---|
   | **Flat on the ground** — type, rules, icons; anything that is neither a surface nor a control | *none* | headings, `.hr`, `.card-meta`, `.btn-ghost` |
   | **A control the reader presses** — *a control is a raised surface, not an outlined box* | `--shadow-sm`, or `--shadow-control-raised` where it is lifted **out of a cream control** | `.btn-primary`, `.btn-secondary`, `.tag-neutral`, `.tag-outline`, the active sidebar row, the slider thumb, `.elev-sm`; the checked `.seg-opt` is the cream case |
   | **A recessed surface** — something sits *inside* it: a well, a trough, a field you type into | `--shadow-control-inset` inside a cream control, `--shadow-accent-inset` inside the accent fill, `--shadow-inset` only where the ground under it flips | `.input`, the `.seg` track (not its options), `.btn-secondary:active`; `.btn-primary:active`; progress troughs, cover wells, skeletons |
   | **A card lifted off the page** | `--shadow-md` | `.card`, `.elev-md`; the sidebar takes `--shadow-sidebar`, the horizontal-only variant of the same lobe |
   | **A dialog over everything** | `--shadow-lg` | `.dialog`, the viewer's next-volume card, `.elev-lg` |

   **Which of those tokens a box actually takes is decided by E-42 §3, and the rule is one sentence:**

   > **A shadow does not follow the theme. It follows the surface it falls on.**

   | Where the shadow falls | Token | Flips with the theme? |
   |---|---|---|
   | The page ground, or a page surface | `--shadow-sm` / `-md` / `-lg` / `--shadow-inset` | **yes** — light and dark twins, §1.1 / §1.4 |
   | **Inside** a cream control | `--shadow-control-inset` | no — the **light** `--shadow-inset`, pinned |
   | **On top of** a cream control | `--shadow-control-raised` | no — the **light** `--shadow-sm`, pinned |
   | **Inside** an accent fill | `--shadow-accent-inset` | no — the **dark** `--shadow-inset`, pinned |

   **Those three are absolutes: they have no `[data-theme='dark']` twin, and writing one is how the
   absolute dies.** They exist because `.btn-secondary`, `.input` and `.seg` are a cream fill in *every*
   scope — light app, dark app, viewer (E-42 §2) — so the surface under the shadow does not flip while a
   theme-relative shadow would. The accent row is the mirror image: the accent is a deep teal in both
   themes, so a recess cut into it is a dark-ground recess even when the app is light. §1.5 carries the
   values, the measured ratios and what each wrong choice actually draws.

   The rule is written this way so it can be **checked**, in either direction:

   - **No control may be defined by a `1px solid` boundary alone.** A rule that gives something the reader
     clicks, types into, or reads as a chip a `border: 1px solid var(--control-border)` and nothing else
     is wrong: on this skin the boundary *is* the shadow. (A border still appears in three places: where it
     is a *marker* — the `--color-hot` inset ring, the focus outline — on the `.radio .dot`, and inside
     `@media (forced-colors: active)`, which is §0.6 and where the shadow does not exist at all.)
   - **No box may carry `--shadow-sm` unless it can be pressed**, and none may carry an inset
     unless something is inside it. A raised badge that is not a control reads as a dead button.
   - ~~`--shadow-inset` must be **used**. It is defined in `tokens.css` in both themes; if a grep of the
     stylesheet layer finds zero `var(--shadow-inset)`, the skin has not been applied.~~

     > **Superseded — E-42 §4 renamed the token this check was counting.** `var(--shadow-inset)` in
     > `base.css` is **zero, and zero is the passing state.** Every recess in that sheet is cut into a
     > cream control, so it takes `var(--shadow-control-inset)` instead — the light `--shadow-inset` pinned
     > to a literal. `soft-ui.test.ts` (*"never uses the theme-relative `--shadow-inset` in the sheet"*)
     > asserts the count is zero against an empty whitelist, so for four sessions the sentence above and
     > the shipped check returned **opposite verdicts on the same grep**, and the sentence was the one an
     > implementer reads first. **What replaces it, still greppable:** `.input` and `.seg` both declare
     > `box-shadow: var(--shadow-control-inset)`; that token's value is byte-identical to the light
     > `--shadow-inset` and contains no `var(`; and no `box-shadow` in `base.css` names `--shadow-inset`.
     > `--shadow-inset` itself is still used — through the Tailwind `shadow-inset` utility, on cover wells,
     > skeletons, progress troughs and the onboarding block, all of which sit on grounds that *do* flip.

   > **This replaces the sentence that used to stand here:** *"Only three things carry a shadow:
   > `.dialog`, the viewer's next-volume card, and the `.elev-*` utilities."* That sentence was true of the
   > **Modernist** skin, and it **survived E-32 by accident.** E-32 adopted the soft-UI skin *wholesale*
   > (전면 채택), and "a control is a raised surface, not an outlined box" is that skin's defining move —
   > but nobody amended this line, so it went on contradicting the ruling that had superseded it, in the
   > one document an implementer reads first. **The old sentence is wrong, not an alternative reading.**
   > Do not restore it and do not cite it. `web/src/components/ds/Card.tsx:6-8` quotes it verbatim as its
   > reason for refusing a shadow; that comment is a pre-E-32 justification and retires with the sentence.
   >
   > ~~**This item, and the eight contracts in §2.3 amended with it, describe the target — the shipped code
   > has not caught up.** As of session 10 `--shadow-inset` is used **zero** times in
   > `web/src/styles/base.css`, and `.btn-secondary`, `.tag-outline` and `.seg` are still the bordered
   > Modernist forms. Ruling **E-36** (`docs/decisions.md`) records the gap, what "applied" means, and the
   > order the next session must work in. Until E-36 is executed, read §0.2 and §2.3 as the specification
   > and the stylesheet as behind it — **not** the other way round.~~
   >
   > **Withdrawn — session 14 executed E-36 (ruling `E-42`), and the instruction is now inverted.**
   > `base.css` has caught up and in four places **overshot** the prose above: `.seg` keeps its
   > `overflow: hidden` against §2.3's ⟳ row (E-42 §8①), `.input` gained a load-bearing
   > `color-scheme: light`, `.btn-primary` gained a lift and an accent-inset press, and the whole sheet
   > gained a `@media (forced-colors: active)` block (§0.6). **Read `base.css` and `tokens.css` as the
   > authority and this document as the thing that has to prove it still matches them.** Struck rather
   > than deleted, because a specification that told an implementer to prefer its own stale prose over
   > shipped code is *precisely* the disease E-36 §2 diagnosed — and this sentence, written by that same
   > ruling, went on doing it for four sessions after the ruling landed.
3. **Button labels are flush left, never centered** — including inside wide buttons (`.btn-block`
   sets `justify-content: flex-start`). Headings and body copy are flush left too. See the grid card hover
   overlay ([`library-grid-card-hover-1440.png`](./ui-shots/library-grid-card-hover-1440.png)): both stacked
   buttons start their text at the left padding edge.
4. **The viewer is a dark ground built from the same tokens, not a second palette.** Viewer background is
   literally `var(--color-text)` (~~#201e1d~~ **`#263B38`**) and its foreground is `var(--color-bg)`
   (~~#f3f2f2~~ **`#EAE3D4`**). §1.4 turns this into a proper theme. The viewer is dark **regardless of the
   app theme** (NFR-CMP-003).

   > **Superseded — E-32 replaced both literals; the mechanism is untouched.** The swap
   > (`--color-text` ⇄ `--color-bg`) is still exactly how `[data-theme='dark']` is built in `tokens.css`
   > (`:416` *"was --color-text"*, `:419` *"was --color-bg"*) — only the two colours changed, from the
   > Modernist near-black/near-white to the teal/cream pair. The struck values stood here for **five
   > sessions** after E-32, and E-36 §2's own audit is what found them; §1's new preamble then cited
   > `#f3f2f2` as the canonical example of a stale value outliving its ruling while leaving the identical
   > literal alive twenty lines above it. That is the third time this list needed a correction E-32 should
   > have carried (§0.1, §0.2, §0.4).
   >
   > **What does *not* re-scope with the ground is the control language** — `.btn-secondary`, `.input` and
   > `.seg` are the same cream in the light app, the dark app and the viewer, because their fill is an
   > absolute (E-42 §2, §1.5). "The viewer is not a second palette" survives that ruling intact: an
   > absolute is not a second palette, it is a token that declines to flip.
5. **The prototype is desktop-only — the responsive layer does not exist yet and must be built.**
   Below ~1024px the fixed 240px sidebar and the 7-column list grid overflow and clip
   ([`library-list-768.png`](./ui-shots/library-list-768.png),
   [`library-grid-400-broken.png`](./ui-shots/library-grid-400-broken.png),
   [`viewer-overlay-400-broken.png`](./ui-shots/viewer-overlay-400-broken.png)).
   §7 specifies the responsive behaviour that must be added. Do not copy the prototype's fixed widths down
   the breakpoints.
6. **A skin that speaks in `box-shadow` says nothing in forced colours — restore it with `outline` and
   system keywords.** This is the mode Windows High Contrast puts the page in, and in it the browser
   computes **`box-shadow` to `none`**. Item 2 moved every control boundary onto `box-shadow`; E-36 §3 then
   moved `.input`'s focus marker off the base layer's `outline` onto `box-shadow` as well; E-42 §1 made
   *selected* a `box-shadow` inset ring on `.radio .dot` and `.seg-opt`. All three vanish together. A
   review measured it with Playwright `forcedColors: 'active'` (E-42 §8③): controls had no edge, a focused
   field had **no indicator at all**, and a checked segment rendered **byte-identical** to an unchecked one
   — so nothing on the screen said which display mode the viewer was in. The repo had never mentioned the
   mode: a `forced-colors` grep across it returned **zero** hits until that block.

   The fix is the last block of `base.css`, and it is the shape any future control rule must copy —
   `border` and `outline` survive forced colours, and the system keywords are what the user's own palette
   is guaranteed to honour:

   ```css
   @media (forced-colors: active) {
     .btn-primary, .btn-secondary, .input, .seg, .tag-neutral, .tag-outline, .card {
       border: 1px solid ButtonBorder;
     }
     .input:focus-visible          { outline: 2px solid Highlight; outline-offset: 0; }
     .seg-opt[data-checked='true'],
     [aria-pressed='true']         { outline: 2px solid Highlight; outline-offset: -2px; }
   }
   ```

   **This is not the bordered box coming back**, and item 2's first check is written to know the
   difference: a `1px solid` on a control is a defect *outside* this at-rule and the required declaration
   *inside* it. `soft-ui.test.ts` enforces exactly that — its border whitelist keys every entry with its
   enclosing `@media` prelude, so moving one of these declarations out of the block changes the key and
   fails, and it asserts the fallback's *presence* separately so it cannot be deleted quietly. Three
   consequences to carry:

   - **A normal render is unaffected.** The whole block is inert unless the mode is on, so it can never be
     "verified" by looking at the app.
   - **Whatever a new rule says with a shadow, it must also say here.** Boundary, focus *and* state — the
     selected marker is the one that got missed the first time, because it does not look like a boundary.
   - **`[aria-pressed='true']` is in the selector on purpose:** the viewer's chrome toggles say "on" the
     same way a checked segment does, and they are not `.seg-opt`.

---

## 1. Design tokens

> **This section is hand-kept, not generated.** The authority for every value below is
> **`web/src/styles/tokens.css`**, and `web/src/styles/tokens.test.ts` is what holds that file to it. §1 is
> that file rendered for a human — which roles exist, what each one is worth, why it is worth that.
> **If a number here and a number there disagree, `tokens.css` wins — and the disagreement is itself a
> defect.** Fix §1; never ship the number you read here over the one the file declares.
>
> **Why that warning is not boilerplate.** §1 stood pre-E-32 from session 9 to session 13: it printed
> `#f3f2f2`, `#ec3013` and `--radius-*: 0px` for five sessions after **E-32** (2026-08-04) replaced all
> three, and
> `--shadow-inset`, `--color-hot` and `--shadow-sidebar` were not in it at all. This has already cost the
> product once. **E-36 §2**: an implementer read the equally stale §0.2 and §2.3, built the contracts they
> stated, and broke E-32 without knowing it — ~30 component rules of the soft-UI skin went unapplied, under
> five green gates, because a retired contract outlived the ruling that retired it. That is how defects
> live here (`docs/HANDOFF.md` §6.5). §1 was rewritten from `tokens.css` in session 14, together with
> ruling **E-42**. Re-reading it against the file before you build from it *is* the job.

### 1.1 Complete token table — light ground, transcribed from `tokens.css`

The `:root, [data-theme='light']` block declares **112** custom properties, and all 112 are below, in the
file's own order. Contrast figures are the ones computed in that file's comments and re-derived in
`tokens.test.ts`; where two are given they read **`on --color-bg` / `on --color-surface`**. *Washed* means
measured through the paper grain, which is the floor that counts (E-35 §4) — a token derived on a dry
ground can pass and then fail on the ground the reader actually sees.

#### Roles (7)

| Token | Value | Role |
|---|---|---|
| `--color-bg` | `#EAE3D4` | Page ground — warm cream |
| `--color-surface` | `#F3EEE3` | Cards, inputs, sidebar, dialogs |
| `--color-text` | `#263B38` | Ink. **Also the viewer ground, in both app themes** (§0.4, §1.4) |
| `--color-accent` | `#17595B` | The single accent — deep teal |
| `--color-accent-2` | `var(--color-accent)` | E-32 §1 collapsed the secondary accent into the accent. The token survives only because `.tag-accent-2` and the Tailwind map still name it |
| `--color-divider` | `#DDD3C0` | The 1px hairline. 1.16:1 on the ground — enough for a rule, **not** enough for a control boundary (see `--control-border`) |
| `--color-hot` | `#EC3013` | The retired brand red. **Theme-invariant, and a marker only:** "current / selected / focused" and nothing else (E-32 §1). The moment it is used as an accent again, that ruling is undone. E-42 §1 adds one site — the checked `.radio .dot`'s inset ring — in the same grammar as `.seg-opt` |

#### Semantic ink and structure — these flip with the theme (34)

| Token | Light value | Contrast | Why this value |
|---|---|---|---|
| `--ink` | `var(--color-text)` | 9.31 / 10.28 | |
| `--ink-muted` | `rgb(38 59 56 / 0.8)` | 5.47 / 5.84 | |
| `--ink-dim` | `#5F5849` (neutral-700) | 5.52 / 6.09 | The prototype's neutral-600 is **3.31** on this ground — an AA fail at the 10–12px this token is used at (E-32 §4) |
| `--ink-faint` | `#68604F` | 4.87 / 5.23 | neutral-600 lifted toward 700 until it clears AA on the *bg*, the harder of the two grounds (neutral-500 is 2.37 on surface). Lifted a **second** time for the grain: `#6C6453` was 4.58 dry and **4.47 washed** — the texture turned a pass into a fail. Now 4.74 washed, 4.63 at the mask's peak |
| `--ink-label` | `rgb(38 59 56 / 0.78)` | 5.18 / 5.52 | `.field > label` |
| `--ink-meta` | `rgb(38 59 56 / 0.75)` | 4.79 / 5.07 | `.card-meta`, 11px |
| `--ink-th` | `rgb(38 59 56 / 0.76)` | 4.92 / 5.22 | `.table th`, 11px |
| `--rule` | `var(--color-divider)` | — | 1px hairlines |
| `--rule-strong` | `#CFC5B0` (neutral-400) | — | The 2px structural rules (§0.2) |
| `--control-border` | `#A79B84` (neutral-500) | 2.14 / 2.37 | The soft divider is 1.16 on the ground; a control boundary drawn in it is not a boundary |
| `--control-border-hover` | `#857A66` (neutral-600) | 3.31 / 3.65 | |
| `--fill-subtle` | `#EFE9DC` (neutral-200) | — | |
| `--fill-track` | `#E2DACA` (neutral-300) | — | Progress troughs |
| `--fill-track-2` | `#CFC5B0` (neutral-400) | — | Troughs sitting over imagery |
| `--hover-tint` | `rgb(38 59 56 / 0.07)` | — | Secondary button / palette item hover |
| `--press-tint` | `rgb(38 59 56 / 0.14)` | — | |
| `--row-hover` | `rgb(38 59 56 / 0.05)` | — | List / volume rows |
| `--row-hover-table` | `rgb(38 59 56 / 0.04)` | — | Table rows |
| `--nav-hover` | `rgb(38 59 56 / 0.06)` | — | Sidebar items |
| `--nav-active` | `rgb(23 89 91 / 0.1)` | — | Selected nav / palette row fill |
| `--scrim-cover` | `rgb(38 59 56 / 0.72)` | — | Grid-card hover scrim |
| `--scrim-modal` | `rgb(38 59 56 / 0.5)` | — | Dialog backdrop |
| `--scrim-cover-base` | `rgb(38 59 56 / 0.92)` | — | The three stops of `.cover-scrim`'s hover gradient, bottom to top. **A gradient is not a colour:** approximating it out of the two scrims plus `--hover-tint` gave .72 / .50 / .07 against the prototype's .92 / .55 / .15 — the top of the card carried half the wash it should and the bottom two thirds of it. Three tokens is what makes it exact |
| `--scrim-cover-mid` | `rgb(38 59 56 / 0.55)` | — | ″ |
| `--scrim-cover-top` | `rgb(38 59 56 / 0.15)` | — | ″ |
| `--accent-hover` | `#135052` (accent-600) | — | One step past base on a light ground |
| `--accent-press` | `#114547` (accent-700) | — | |
| `--accent-text` | `#114547` (accent-700) | 8.36 | The accent at paragraph size (§2.5) |
| `--accent-fill` | `var(--color-accent)` | 5.78 on `--fill-track` | The accent as a *fill* that must read against the ground (floor is 3). On dark it moves up the ramp — §1.4 |
| `--scrollbar-thumb` | `#A79B84` (neutral-500) | — | The prototype's neutral-400 is 1.34 on the ground, and the thumb is 4px wide once the 3px transparent border is cut |
| `--scrollbar-thumb-hover` | `#857A66` (neutral-600) | — | |
| `--ghost-hover` | `rgb(23 89 91 / 0.1)` | — | `.btn-ghost` |
| `--ghost-press` | `rgb(23 89 91 / 0.18)` | — | |
| `--selection-bg` | `rgb(23 89 91 / 0.22)` | ink 6.68 on it | `::selection` |

#### Ramps — constant across themes (27)

| Token | Value | Token | Value |
|---|---|---|---|
| `--color-neutral-100` | `#F7F3EA` | `--color-accent-100` | `#E4EEEC` |
| `--color-neutral-200` | `#EFE9DC` | `--color-accent-200` | `#C7DCDA` |
| `--color-neutral-300` | `#E2DACA` | `--color-accent-300` | `#9BC3C1` |
| `--color-neutral-400` | `#CFC5B0` | `--color-accent-400` | `#5E9B9B` |
| `--color-neutral-500` | `#A79B84` | `--color-accent-500` | `#17595B` |
| `--color-neutral-600` | `#857A66` | `--color-accent-600` | `#135052` |
| `--color-neutral-700` | `#5F5849` | `--color-accent-700` | `#114547` |
| `--color-neutral-800` | `#3B3730` | `--color-accent-800` | `#0D3436` |
| `--color-neutral-900` | `#23211D` | `--color-accent-900` | `#082325` |

Nine more make the accent-2 ramp, each an alias of the accent step of the same number:
`--color-accent-2-100` `--color-accent-2-200` `--color-accent-2-300` `--color-accent-2-400`
`--color-accent-2-500` `--color-accent-2-600` `--color-accent-2-700` `--color-accent-2-800`
`--color-accent-2-900`. E-32 §1 collapsed the second accent; `tokens.test.ts` asserts the two ramps
resolve step for step, so a drift there is a failing test rather than a design decision.

**The ramps are an absolute lightness scale and do not move when the theme does.** That is the whole reason
the semantic layer above exists: the prototype has no such layer and inlines ramp steps, and an inlined
step draws the *same* colour inside a dark scope, where the contrast then collapses (E-32 §3). Never paint
`bg-neutral-200` for "a subtle fill" — paint `bg-fill-subtle`. §1.4 has the mapping.

#### Absolutes — not theme-relative (13)

The viewer ground is `#263B38` in **both** app themes, so what is painted on top of it cannot flip; the
same goes for any foreground whose surface does not flip.

| Token | Value | Why it is an absolute |
|---|---|---|
| `--scrim-volume-end` | `rgb(38 59 56 / 0.92)` | Painted on the viewer ground |
| `--scrim-broken` | `rgb(8 35 37 / 0.82)` | accent-900 @ 82%, on the viewer ground |
| `--on-accent` | `#F6F2E9` | The ink on an accent fill — 7.18 on the accent, 8.18 on accent-600. Constant because the accent is |
| `--on-hot` | `#000000` | The ink on the hot marker — 4.9988 dry, **4.73 washed**, and it has to be *pure* black. Light ink cannot get there at all (`#F6F2E9` is 3.76, pure white 4.20); dark ink can, barely. The shipped `#1B0B07` was 4.55 dry and **4.33 washed** — a fail the contrast tests could not see because they did not model the texture. At the mask's *peak* alpha this is 4.46–4.51 depending on how "peak" is defined; `tokens.test.ts` reports that number, does not gate on it, and says why. The next 0.04 of margin would have to come from **`--color-hot`**, and changing the red E-32 §1 pinned is a ruling, not a side effect |
| `--control-fill` `--control-fill-hover` `--control-well` `--on-control` `--on-control-accent` `--on-control-dim` | `#F3EEE3` `#F8F4EC` `#EFE9DC` `#0D3436` `#17595B` `#5F5849` | **E-42.** The cream control surface and the three inks that sit on it — the same cream in the light app, the dark app and the viewer. Values, ratios and the rule that governs them: **§1.5** |
| `--shadow-control-inset` `--shadow-control-raised` `--shadow-accent-inset` | the light inset, the light `sm`, the **dark** inset — each pinned | **E-42.** A shadow follows the surface it falls on, not the theme — **§1.5** |

#### Type (3)

| Token | Value |
|---|---|
| `--font-heading` | `'Archivo', 'Pretendard Variable', Pretendard, 'Apple SD Gothic Neo', 'Malgun Gothic', 'Noto Sans KR', system-ui, sans-serif` |
| `--font-heading-weight` | `800` |
| `--font-body` | `var(--font-heading)` |

Archivo is vendored latin-only (C-14, decisions E-7) and has **no Hangul coverage**, so the Korean fallback
stack is explicit and ordered rather than left to `system-ui` (§2.1).

#### Space and radius (11)

| Token | Value | |
|---|---|---|
| `--space-1` `--space-2` `--space-3` `--space-4` `--space-6` `--space-8` | `4px · 8px · 12px · 16px · 24px · 32px` | Keys **1, 2, 3, 4, 6, 8** — the DS has no 5 or 7 |
| `--radius-sm` `--radius-md` `--radius-lg` `--radius-pill` `--radius-full` | `3px` / `4px` / `6px` / `7px` / `999px` | E-32 §2 retired D-40's `0px`. **The ban did not go away with the zero:** the hygiene whitelist is now *bound to these five values* plus `50%` / `9999px`, so an arbitrary px radius is still a build failure (`web/src/lib/hygiene.test.ts`). A new radius is a new token here, not a number at a call site |

#### Elevation — dual light on a warm ground (5)

Down-right lobe = ochre shadow; up-left lobe = cream highlight. **The highlight is what makes the skin read
as soft, and it is also what makes this set unusable on a dark ground**, where `rgb(255 253 246 / .9)`
becomes a white outline. §1.4 re-derives all five; §1.5 pins two of them for surfaces that never flip.
Which box gets which shadow is §0.2 — the five-question table.

| Token | Value |
|---|---|
| `--shadow-sm` | `3px 3px 8px rgb(150 128 96 / 0.22), -3px -3px 8px rgb(255 253 246 / 0.9)` |
| `--shadow-md` | `6px 6px 16px rgb(150 128 96 / 0.26), -5px -5px 14px rgb(255 253 246 / 0.92)` |
| `--shadow-lg` | `12px 14px 34px rgb(150 128 96 / 0.3), -8px -8px 22px rgb(255 253 246 / 0.9)` |
| `--shadow-inset` | `inset 3px 3px 7px rgb(150 128 96 / 0.28), inset -3px -3px 7px rgb(255 253 246 / 0.85)` |
| `--shadow-sidebar` | `4px 0 18px rgb(150 128 96 / 0.16)` — the sidebar's right edge, and the one elevation off the sm/md/lg scale: **horizontal only.** `--shadow-md` stood in for it, but a 6px *downward* offset on a full-height panel puts a shadow under its top edge, where nothing casts one, at an alpha meant for a card that floats rather than an edge that abuts |

#### Paper grain (4)

| Token | Value | |
|---|---|---|
| `--paper-grain` | `url("data:image/svg+xml,…")` | A 200px `feTurbulence` tile — `fractalNoise`, `baseFrequency .9`, `numOctaves 3`, **`seed 4`**, `stitchTiles stitch`. Re-derived rather than ported (E-35): the prototype loads `@paper-design/shaders` from esm.sh at runtime, which NFR-OPS-001 forbids outright; its canvas fallback costs 21.7 ms of synchronous main thread and lands as a ~198 KB inline `style`; and it ignores its own `seed` in favour of `Math.random()`, so a plain reload changes 89.1 % of the pixels — which would make `docs/ui-shots/` useless as a review reference. The filter writes **alpha and nothing else**: the colour matrix zeroes RGB and scales the noise into alpha, so the image is a *mask* and the tone stays a token instead of being buried in the data URI. Amplitude 0.126 is the prototype's own, read off its fallback tile; measured here mean 16.05/255, max 29/255 |
| `--paper-tone` | `var(--color-neutral-900)` | The tone the grain washes in. The prototype's `#201E1D` is three units from `#23211D` on two channels — 0.35/255 through this mask at the shipped intensity — so the ramp step *is* the prototype's colour and adds no literal to the file |
| `--paper-intensity` | `1` | Moves the average pixel **12.7/255** darker over the cream. **Ruling E-43** doubled the prototype's `0.5` after the user compared four intensities rendered on the real screens: at 0.5 the grain was only findable up close. Two dark inks were re-derived for it and the hot marker was exempted from the wash — see `base.css` |
| `--paper-tone-viewer` | `#0D0C0C` | An absolute, for the same reason the viewer scrims are: the viewer ground does not flip. The one paper value that is a literal, because no ramp step is near it — `--color-accent-900` is as dark as the palette goes and its relative luminance is still 3.7× this. The reading screen's grain is soot, not more of the ground's own green |

#### z-index ladder (5) — §3 owns the ladder

| Token | Value | |
|---|---|---|
| `--z-content` | `0` | |
| `--z-sticky` | `2` | |
| `--z-viewer` | `60` | |
| `--z-overlay` | `80` | |
| `--z-texture` | `90` | One step above the overlay and deliberately the top of the ladder: the grain is a property of the screen, so a dialog is printed on it too. The prototype's bare `z-index: 900` is the same intent with the ladder abandoned. Mirrored in `tailwind.config.ts`, and `tokens.test.ts` checks that both files state it |

#### Misc (3)

| Token | Value | |
|---|---|---|
| `--disabled-opacity` | `0.45` | Plus `cursor: not-allowed` (§2.2) |
| `--chrome-fade` | `0.18s` | Viewer chrome show/hide |
| `--hover-fade` | `0.12s` | |

#### Not part of the 112 — the responsive knobs

`--grid-min`, `--grid-gap`, `--sidebar-w`, `--drawer-w` and `--touch-min` also live in `tokens.css`, but in
their own ascending `:root` + `@media` blocks at the foot of the file. They are specified in **§7** (D-42),
not here, and the reason they are one ascending block rather than breakpoint variants sprinkled across the
grid class is stated there.

#### Element type scale (`base.css`, not tokens)

| Element | Size | Notes |
|---|---|---|
| `body` | 14px / 1.45 / 400 | 14px, not the DS's 15px: density beats comfort here (design.md #4) |
| `h1` … `h5` | 42 / 32 / 25 / 20 / 16px | Archivo 800, `line-height: 1.12`, `letter-spacing: -0.015em` |
| `h6` | 16px, `-0.01em`, mixed case | **E-32 restyled the section label and kept the tag.** It used to be 13px uppercase +0.08em. The prototype reaches the new look by swapping every `<h6>`/`<h4>` for a styled `<div>`, which E-32 §4 refuses: that deletes the document outline, kills screen-reader heading navigation, and breaks `e2e/06-settings.spec.ts`'s `getByRole('heading')`. The *style* moves; the *tag* stays |
| `.card-meta` / muted copy | 11px | `--ink-meta` |

### 1.2 The shape of `tokens.css` — structure here, values in the file

**This section explains the file's structure. It does not restate its contents.** A second copy of the CSS
is a second thing to keep current, and the copy is what goes stale — which is the failure this whole
section is a correction for. Read the values from `web/src/styles/tokens.css`; §1.1 tabulates the light
block and §1.4 the dark one. (`tokens.test.ts` cites "§1.2" for the light block and "§1.4" for the dark.)

The file is imported once from `main.tsx`, **before Tailwind's layers**, and it holds every colour literal
in the product — `hygiene.test.ts` fails the build on a hex anywhere else in the style layer.

```css
/* web/src/styles/tokens.css */
:root,
[data-theme='light'] { color-scheme: light; /* 112 tokens */ }

[data-theme='dark']  { color-scheme: dark;  /*  46 tokens — only what flips */ }
```

Four structural facts, each load-bearing:

- **The dark selector is bare, not `:root[data-theme='dark']`.** §1.4 requires `<div data-theme="dark">` to
  re-scope the same tokens so the viewer is dark in *both* app themes (NFR-CMP-003). `:root[…]` only ever
  matches `<html>` — so the selector §1.4 used to print could not do what the paragraph three lines under
  it asked for, and the file has never obeyed it. Written bare, it matches any element. `tokens.test.ts`
  asserts both halves — that a `<div>` matches it, and that it does **not** start with `:root`.
- **Light is declared first, dark second, and the order is the mechanism.** Both blocks have specificity
  (0,1,0), so on `<html data-theme="dark">` the dark block wins only because it comes later. The same works
  in reverse: `<div data-theme="light">` inside the dark viewer re-scopes back to the light palette, which
  is exactly what the next-volume card of §6.5 needs. Reordering these two blocks silently breaks the theme.
- **The dark block redeclares only what flips** — 46 of the 112. The ramps, the absolutes, the type, the
  space/radius scale, the z ladder and the misc timings are stated once, in the light block, because they
  are theme-invariant by definition. Adding a dark twin for one of them is not a refinement; it is a claim
  that the token was never invariant — and for the E-42 group it is the specific mistake §1.5 forbids.
- **The comments carry the derivation, not just the value.** Every ratio in the file is computed WCAG 2.1
  relative luminance, and the same numbers are re-derived and asserted in `tokens.test.ts`. When you change
  a token, the comment and the test move with it — a value whose reason is not written down is a value the
  next session will "simplify".

### 1.3 Tailwind mapping

`web/tailwind.config.ts` maps the CSS variables through `theme.extend`, so `bg-surface`, `text-accent-text`,
`border-divider`, `p-4`, `gap-6` all resolve to tokens and a `data-theme` swap re-themes every utility with
no rebuild. What the config actually contains:

| Config key | Maps to | Note |
|---|---|---|
| `content` | `./index.html`, `./src/**/*.{ts,tsx}` | |
| `darkMode` | `['selector', '[data-theme="dark"]']` | Bare attribute selector, matching `tokens.css` — a nested viewer `<div>` re-scopes |
| `borderRadius` | **override**, not extend | `none 0px` · `DEFAULT var(--radius-md)` · `sm`/`md`/`lg`/`pill` → the four tokens · `full 9999px` |
| `screens` | `md 768` / `lg 1024` / `xl 1440` | `xl` is moved off Tailwind's 1280 so the desktop tier is the one §7 describes |
| `colors.bg` `.surface` `.ink` `.divider` | `--color-bg` `--color-surface` `--color-text` `--color-divider` | |
| `colors.accent.{DEFAULT,100…900}` | the accent ramp | |
| `colors['accent-2'].{DEFAULT,100…900}` | the accent-2 aliases | Which resolve to the accent (E-32 §1) |
| `colors.neutral.{100…900}` | the neutral ramp | Raw steps — reach for the semantic names instead (§1.4) |
| `colors.hot` / `colors['on-hot']` | `--color-hot` / `--on-hot` | Marker only |
| 27 semantic colour names | `ink-muted`, `ink-dim`, `ink-faint`, `ink-label`, `ink-meta`, `ink-th`, `rule`, `rule-strong`, `control-border`, `fill-subtle`, `fill-track`, `fill-track-2`, `hover-tint`, `press-tint`, `row-hover`, `row-hover-table`, `nav-hover`, `nav-active`, `scrim-cover`, `scrim-modal`, `scrim-volume-end`, `scrim-broken`, `accent-hover`, `accent-press`, `accent-text`, `accent-fill`, `on-accent` | Each `var(--…)` |
| 6 control colours (E-42) | `control-fill`, `control-fill-hover`, `control-well`, `on-control`, `on-control-accent`, `on-control-dim` | Absolute — see §1.5. These are what make a readable *override* possible inside the dark viewer chrome, where `text-ink` is 1.10 on cream and `text-accent-text` is 1.65 |
| `borderColor.DEFAULT` | `--color-divider` | |
| `spacing` `1,2,3,4,6,8` | `--space-*` | An `extend`, so Tailwind's own numeric scale survives for everything else |
| `fontFamily.sans` / `.heading` | `--font-body` / `--font-heading` | |
| `fontSize` | literal px | `2xs 9 · 3xs 10 · xs 11 · sm 12 · base 13 · md 14 · lg 15`, plus `h1…h6` |
| `boxShadow` `sm/md/lg/inset` | `--shadow-*` | These flip with the theme |
| `boxShadow` `control-inset` / `control-raised` / `accent-inset` (E-42) | `--shadow-control-*`, `--shadow-accent-inset` | These do not — §1.5 |
| `zIndex` | `content 0 · sticky 2 · chrome 3 · viewer 60 · overlay 80 · texture 90` | `chrome` is viewer-internal (the two bars, above the end-of-volume scrim) and has **no** token; the rest mirror the ladder |
| `keyframes` / `animation` | `shimmer 1.6s`, `spin .7s` | |

**Three rules travel with this mapping.**

- **`borderRadius` is overridden, not extended, and that is D-40 surviving E-32.** D-40's zero-radius rule
  is retired; its reasoning — *enforcement beats discipline* — is not. After this config the only radius
  utilities that exist are the five token steps plus `rounded-none` / `rounded-full`: `rounded-xl`,
  `rounded-2xl` and every arbitrary `rounded-[13px]` cannot be produced at all, and `hygiene.test.ts` reads
  the token block and fails the build on anything outside it. A new radius is a new token in `tokens.css`,
  never a number at a call site.
- **Tailwind opacity modifiers do not work on themed colours.** `bg-ink/50` silently fails, because the
  values are opaque hexes and pre-composed `rgb(… / α)` strings rather than `<alpha-value>` channel
  triples. That is intentional and matches the DS: every tint the design needs is already a named semantic
  token. Add one to `tokens.css` rather than inventing `/40` at the call site.
- **Not every token needs a utility.** Roughly twenty are painted only from `base.css` —
  `--control-border-hover`, the three `--scrim-cover-*` stops, `--ghost-*`, `--selection-bg`,
  `--scrollbar-thumb*`, the paper set, `--shadow-sidebar`, `--disabled-opacity`, the two fade durations.
  A token absent from this table is not a defect; a *utility* that resolves to a literal is.

> **One stale step, recorded rather than left silent:** `fontSize.h6` is still `13px / 1.12`, the pre-E-32
> section-label size, while `base.css`'s `h6` element is 16px / `-0.01em` mixed case (E-32). Nothing in
> `src/` uses `text-h6` today, so the two do not currently collide — but if you reach for that utility,
> take 16px from the element style, not from this map.

### 1.4 Dark ramp (required by NFR-CMP-003)

**The dark theme is derived, not copied — the prototype ships a light palette only.** The ground is the
light theme's ink (`#263B38`) and the ink is the light theme's ground (`#EAE3D4`): the same swap the
Modernist palette used, carried onto the teal. The accent is held constant, structure is taken from the
opposite end of the ramps, and the hover/press steps move, because a step that reads as "one past base" on
cream does not on teal.

The `[data-theme='dark']` block redeclares **46** tokens — these and only these flip. Contrast reads
**`on --color-bg` / `on --color-surface`** in the dark theme.

| Token | Dark value | Contrast | Why |
|---|---|---|---|
| `--color-bg` | `#263B38` | | Was `--color-text` |
| `--color-surface` | `#2F4A46` | 1.24 vs the ground | One step up — the same separation the old `#201e1d` / `#2d2b2b` pair had |
| `--color-text` | `#EAE3D4` | | Was `--color-bg` |
| `--color-accent` | `#17595B` | | Brand constant |
| `--color-accent-2` | `var(--color-accent)` | | |
| `--color-divider` | `#3E5B57` | | **Solid**, not a tint — the viewer chrome rule |
| `--ink` | `var(--color-text)` | 9.31 / 7.51 | |
| `--ink-muted` | `rgb(234 227 212 / 0.78)` | 6.36 / 5.29 | |
| `--ink-dim` | `#B9C8C4` | 6.87 / 5.54 | |
| `--ink-faint` | `#AFC0BC` | 6.28 / 5.07 | **On dark the *surface* is the harder ground** — the shipped dark faint was 3.27 on it. Lifted two steps at **E-43**: `#A9BAB6` was 4.42 washed once the intensity doubled |
| `--ink-label` | `rgb(234 227 212 / 0.75)` | 6.01 / 5.03 | |
| `--ink-meta` | `rgb(234 227 212 / 0.75)` | 5.76 / 5.03 | Was 0.7 — 4.61 on the surface dry and **4.45 washed**, and this is the token that sits on the surface most (`.card-meta` at 11px). 0.75 puts it level with `--ink-label`: 4.85 washed, 4.71 at peak |
| `--ink-th` | `rgb(234 227 212 / 0.75)` | 6.01 / 5.03 | Raised from `0.72` at **E-43** (4.45 washed). Level with `--ink-label` and `--ink-meta`, not past them — alphas that mean the same thing in the hierarchy stay equal |
| `--rule` | `#3E5B57` | | |
| `--rule-strong` | `#4A6B66` | | |
| `--control-border` | `#5A7C77` | | The viewer's button/seg borders |
| `--control-border-hover` | `#79A09A` | | |
| `--fill-subtle` | `#2F4A46` | | |
| `--fill-track` | `#3E5B57` | | |
| `--fill-track-2` | `#5A7C77` | | |
| `--hover-tint` | `rgb(234 227 212 / 0.08)` | | |
| `--press-tint` | `rgb(234 227 212 / 0.16)` | | |
| `--row-hover` | `rgb(234 227 212 / 0.06)` | | |
| `--row-hover-table` | `rgb(234 227 212 / 0.05)` | | |
| `--nav-hover` | `rgb(234 227 212 / 0.07)` | | |
| `--nav-active` | `rgb(155 195 193 / 0.16)` | | **Tinting a teal ground with the teal accent does nothing** — same hue, nearly the same lightness. The active row is washed with accent-300 instead |
| `--scrim-cover` | `rgb(0 0 0 / 0.72)` | | The scrims go to black on dark |
| `--scrim-modal` | `rgb(0 0 0 / 0.6)` | | |
| `--scrim-cover-base` `--scrim-cover-mid` `--scrim-cover-top` | `rgb(0 0 0 / 0.92)` `rgb(0 0 0 / 0.55)` `rgb(0 0 0 / 0.15)` | | |
| `--accent-hover` | `#256668` | label 5.91 | Up the ramp, but only as far as the cream label on the fill still clears AA — accent-400 would take `#F6F2E9` to 2.6 |
| `--accent-press` | `#2C6D6E` | label 5.34 | |
| `--accent-text` | `#9BC3C1` (accent-300) | 6.21 / 5.01 | |
| `--accent-fill` | `#9BC3C1` | 3.86 on `--fill-track` | `--color-accent` is **1.09** there: `bg-accent` on a dark progress bar is invisible |
| `--scrollbar-thumb` | `rgb(234 227 212 / 0.4)` | | |
| `--scrollbar-thumb-hover` | `rgb(234 227 212 / 0.55)` | | |
| `--ghost-hover` | `rgb(155 195 193 / 0.12)` | | |
| `--ghost-press` | `rgb(155 195 193 / 0.2)` | | |
| `--selection-bg` | `rgb(155 195 193 / 0.32)` | | |
| `--shadow-sm` | `0 0 0 1px #3E5B57, 0 1px 2px rgb(0 0 0 / 0.5)` | | **Elevation on a dark ground is a hairline edge plus ambient darkness.** The light block's up-left lobe is `rgb(255 253 246 / .9)`; painted here it is a white outline around every card, not a highlight |
| `--shadow-md` | `0 0 0 1px #3E5B57, 0 3px 10px rgb(0 0 0 / 0.55)` | | |
| `--shadow-lg` | `0 0 0 1px #3E5B57, 0 12px 32px rgb(0 0 0 / 0.6)` | | |
| `--shadow-inset` | `inset 3px 3px 7px rgb(0 0 0 / 0.5), inset -3px -3px 7px rgb(155 195 193 / 0.12)` | | Keeps the dual-light *shape* — dark down-left, light up-right — at an alpha a dark ground can carry |
| `--shadow-sidebar` | `1px 0 0 #3E5B57, 4px 0 18px rgb(0 0 0 / 0.5)` | | The panel is `--color-surface` on `--color-bg`, 1.24:1 here, and the ochre lobe is a light-ground device that vanishes. So the edge is stated as a hairline — `1px 0 0`, right side only, the only side of a full-height panel that shows |
| `--paper-tone` | `var(--color-accent-900)` | | The grain's tone follows the ground it washes. On today's numbers this is worth under 1/255 — the measured max channel delta in the viewer is 1 — and it is declared anyway because the tone is the one part of the texture that is *paint*: raise `--paper-intensity`, or move the tone off near-black, and a warm wash over a teal ground is immediately a brown cast. Paint that does not flip with the theme is how a token quietly stops being one |

**The other 66 do not flip**, and that is a statement, not an omission: `--color-hot` (1), the ramps (27),
the absolutes (13, nine of them E-42's — §1.5), type (3), space + radius (11), `--paper-grain` /
`--paper-intensity` / `--paper-tone-viewer` (3), the z ladder (5), misc (3).

**Semantics that must be preserved when the theme flips.** What changes is not the ramp but which end of it
each *role* points at. The prototype's raw-ramp references map onto semantic tokens like this:

| Prototype (light) | Semantic token | Dark resolves to |
|---|---|---|
| `neutral-200` / `neutral-300` cover stripes | `--fill-subtle` / `--fill-track` | `#2F4A46` / `#3E5B57` |
| `neutral-300` progress trough | `--fill-track` | `#3E5B57` |
| `neutral-400` progress trough over art | `--fill-track-2` | `#5A7C77` |
| `neutral-500` disabled / idle numerals | `--ink-faint` | `#AFC0BC` |
| `neutral-600` labels, counts | `--ink-dim` | `#B9C8C4` |
| `neutral-700` secondary body text | `--ink-muted` | `rgb(234 227 212 / .78)` |
| `neutral-800` strong body text | `--ink` | `#EAE3D4` |
| `accent-700` accent text | `--accent-text` | `#9BC3C1` (accent-300) |

`tokens.test.ts` has a `describe` for exactly this — *"a ramp step painted by a class flips with the
theme"* — so a class that paints a raw step without a dark twin turns that block red.

**Theme scope.** `data-theme` on `<html>` follows the user setting (light / dark / system, §5.4). The
viewer root **always** renders dark regardless — implement it as
`<div data-theme="dark" className="viewer-root">` so it re-scopes the same tokens rather than hardcoding
colours. That is what makes the light-theme viewer and the dark-theme viewer identical, which is what
`--theme: 다크 (뷰어 고정)` in the prototype's settings panel is telling you. The viewer is a dark ground
built from these tokens, **not a second palette** (§0.4, NFR-CMP-003) — and what does *not* re-scope with
it is the control language, which is §1.5.

### 1.5 Control surfaces are absolutes, and shadows follow the surface (E-42)

E-36 §4 pinned the viewer's controls cream: the viewer's *ground* stays dark, and what changes against the
prototype is that a control stops being a border ghost and becomes a **cream fill**. **E-42 §2 raises that
from the viewer to the product.** `.btn-secondary`, `.input` and `.seg` are the same cream surface in the
light app, in the dark app and in the viewer.

**Why it could not stay a viewer exception.** The viewer's dark scope and the app's dark theme are the
*same* token block — `[data-theme='dark']` — and a custom property cannot tell the two apart. There is no
honest way to write "cream in the viewer, whatever the theme says elsewhere" without a `:root[data-theme]`
vs `[data-theme]:not(:root)` split, and that split is exactly what §1.2 refuses. So there were two options:
raise the cream to global, or fork the control language by scope. The user ruled **global** — one control
language, and E-36 §4's viewer becomes the rule rather than the exception. The price is that controls in
the app's dark theme visibly change, which is why the ruling went to the user.

**Provenance of the cream.** Session 9 measured the prototype's `뒤로` button directly (full text in
E-36 §4): its interior is `(235, 230, 220)` against a bar of `(37, 57, 55)`, while the same pixel in the
product was `(37, 58, 55)` — the bar itself, fill `rgba(0,0,0,0)`. `design-v2/MEASURED.md` records the
button as `bg #F3EEE3`, `color rgb(13,52,54)` (= accent-800), `border-radius 7px`.

#### The six colours (E-42 §3) — light values, pinned, no dark twin

Ratios below are quoted the way `tokens.css` quotes them: dry → **washed** (at the mask's peak), worst of
the three tones.

| Token | Value | What it is worth |
|---|---|---|
| `--control-fill` | `#F3EEE3` | The raised control surface — the light theme's `--color-surface` pinned to a literal so it cannot flip. `--on-control` on it: 11.62 → **11.02** (10.54). As a *fill* it must also separate from the ground it sits on, and both clear the 3:1 non-text floor with room: 10.28 → **9.80** (9.40) against the viewer ground `#263B38`, 8.29 → **7.96** (7.69) against the dark surface `#2F4A46` |
| `--control-fill-hover` | `#F8F4EC` | The prototype's hover cream. Until E-42 it had **no token at all** — E-36 §4 counted it zero times across `tokens.css` and `base.css`, which is why `.btn-secondary:hover` had nothing to move to. `--on-control` 12.26 → **11.62** (11.11); `--on-control-accent` 7.32 → **7.06** (6.84) |
| `--control-well` | `#EFE9DC` | The *recessed* track (`.seg`) — the light theme's `--fill-subtle` pinned. **Not** written as `var(--fill-subtle)`, which flips to `#2F4A46` and would put a teal track inside a cream control; **not** written as `var(--color-neutral-200)` either, because the ramp step is a coincidence of value and what this token *is* is "the light control track, held still". `--on-control` 11.12 → **10.55** (10.09); `--on-control-dim` 5.83 → **5.65** (5.50); separation from the viewer ground 9.83 → **9.37** (9.00) |
| `--on-control` | `#0D3436` | accent-800 pinned — the ink on cream, the colour read straight off the prototype's `뒤로` label. Absolute because the cream is: `--ink` would flip to `#EAE3D4` here and land at **1.10** |
| `--on-control-accent` | `#17595B` | The accent when it is ink *on cream* — the hovered `.btn-secondary` label, the viewer bottom bar's strip-open control. `--accent-text` cannot do this job: it flips to accent-300, which is **1.65** on the cream |
| `--on-control-dim` | `#5F5849` | neutral-700 pinned — placeholders and unselected seg options, the ink that is *meant* to be quiet and therefore has the least room: 6.09 → **5.90** (5.75) on the fill. `--ink-dim` flips to `#B9C8C4`: **1.50**. The prototype's neutral-500 (2.37) was already refused by E-32 §4 |

**"Absolute" means there is no dark twin, and writing one is how the absolute dies.** These six, and the
three shadows below, sit in the `absolutes` block of `tokens.css` beside `--on-accent`, `--on-hot`, `--scrim-volume-end` and
`--scrim-broken`, which exist for exactly this reason: *a foreground on a surface that never flips.* The
cream pill is that surface. The moment a `[data-theme='dark']` block re-declares `--control-fill`, a
control in the viewer is a deep-teal pill on a deep-teal bar — a dark button on a dark ground, the exact
defect E-36 §4 was written to stop — and the inks on it stop being letters (1.10, 1.65, 1.50 above).

#### The rule this ruling establishes (E-42 §3)

> **A shadow does not follow the theme. It follows the surface it falls on.**

| Where the shadow falls | Token | Value | Example |
|---|---|---|---|
| The page ground or a page surface — **flips** | `--shadow-sm` / `-md` / `-lg` / `--shadow-inset` | §1.1, §1.4 | `.card`, `.dialog`, the sidebar, cover wells, skeletons, progress troughs |
| **Inside** a cream control — absolute | `--shadow-control-inset` | = the **light** `--shadow-inset`, pinned | `.input`, the `.seg` track, `.btn-secondary:active` |
| **On top of** a cream control — absolute | `--shadow-control-raised` | = the **light** `--shadow-sm`, pinned | The selected `.seg-opt` lifted out of the cream track |
| **Inside** an accent fill — absolute | `--shadow-accent-inset` | = the **dark** `--shadow-inset`, pinned | `.btn-primary:active` |

The three are **literal copies, never `var(--shadow-…)`**. A reference resolves in the scope it is *used*
in, so it would flip with the theme — and not flipping is the only reason these tokens exist.

Read the last row twice: the accent is a dark teal in *both* themes, so a recess cut into it is a
dark-ground recess even when the app is light. The light inset's up-left lobe is `rgb(255 253 246 / 0.85)`;
on a `#17595B` fill that is a cream smear across the button, not a recess. Symmetrically, on dark
`--shadow-sm` leads with a `0 0 0 1px #3E5B57` hairline, and that hairline is 6.19 washed against the cream
— not a soft lift but a hard teal box drawn around the option.

**The failure the rule prevents** is the one that follows from doing nothing: in the dark theme the cream
pill gets the dark inset — black plus an accent-300 highlight — painted inside it. The surface did not
flip and the shadow did. That is the whole error, and it is the same shape as the one §0.2's shadow
language and §1.4's ramp discipline each guard against: a value flipping for a reason that has nothing to
do with what it is drawn on.

#### One thing to know before you count

E-36 §3's completion criterion 1 is *"`var(--shadow-inset)` is not zero times in `base.css`"*. Because of
this ruling, the name `.input` and `.seg` actually use is **`var(--shadow-control-inset)`** — the light
values pinned. Counted literally, criterion 1 still reads zero. **That is a rename, not a shortfall**, and
it is written here and in E-42 §4 because the disease E-36 §2 diagnosed is precisely *a difference with no
record*. `--shadow-inset` itself stays in use through the Tailwind `shadow-inset` utility — cover wells,
skeletons, progress troughs — and those sit on grounds that do flip.

---

## 2. Global rules (non-negotiable, from the DS readme)

### 2.1 No CDN — Archivo must be vendored

`_ds/…/styles.css` line 2 is:

```css
@import url('https://fonts.googleapis.com/css2?family=Archivo:wght@400;600;800&display=swap');
```

This **violates NFR-OPS-001/002** (single binary, no runtime external dependency) and must be deleted.
Instead:

1. `npm i @fontsource/archivo` (or drop the `.woff2` files into `src/assets/fonts/`).
2. Import only the three weights the DS uses — **400, 600, 800** — latin + latin-ext subsets.
3. Declare `@font-face { font-display: swap; }` and let Vite fingerprint + inline them into `dist/`, which
   `go:embed` then swallows.

**Korean text is not set in Archivo.** Archivo has no Hangul coverage — every Korean glyph in the
screenshots is being rendered by the system fallback. The stack in §1.2 makes that explicit and ordered:

```
"Archivo", "Pretendard Variable", Pretendard, "Apple SD Gothic Neo", "Noto Sans KR", system-ui, sans-serif
```

Decide deliberately whether to vendor a Korean face. A subsetted Pretendard/Noto Sans KR is 1.5–4 MB and
would dominate the binary; the recommendation is **latin-only Archivo bundled + system Korean fallback**,
which is exactly what the prototype does and what the screenshots show. If you later vendor Korean, subset
to the Hangul syllable block actually used and load it `font-display: optional`.

### 2.2 Interaction states — themed, never browser defaults

```css
:focus { outline: none; }
:focus-visible { outline: 2px solid var(--color-accent); outline-offset: 2px; }
::selection { background: rgb(236 48 19 / 0.30); }
```

Every interactive element gets a `:hover` tint and a pressed state one step past the accent base
(`--accent-hover` / `--accent-press`, which flip with the theme). Disabled → `opacity: .45; cursor: not-allowed`.
Verified live: [`focus-visible-ring-1440.png`](./ui-shots/focus-visible-ring-1440.png) — the 2px accent ring
with 2px offset on the sidebar `?` button.

### 2.3 Component class contracts

Rebuild these as React components, but keep the *exact* geometry.

> **Amended by E-36 — eight rows below are no longer the Modernist forms, and this section's heading is no
> longer "verbatim from the DS".** E-32 replaced the skin wholesale; the rows for `.btn-secondary`,
> `.tag`/`.tag-neutral`/`.tag-outline`, `.input`, `.seg`, `.seg-opt` and `.card` were never amended with it, so an
> implementer who read E-32, fixed the tokens and the radii, and then came here to build the components
> found the **superseded** contract still stated as current — and built it. That is why ~30 component rules
> of the soft-UI skin are unapplied. Every amended row below is now quoted from the prototype's
> `soft-ui.css`, marked ⟳, and every untouched row is still the DS's.
>
> ~~**These rows describe the target; the shipped `base.css` has not caught up** (E-36 §3 says what
> "applied" means and §5 gives the order).~~
>
> **Superseded — the ⟳ rows are the shipped contract now, not a target.** Session 14 executed E-36 under
> ruling **E-42**; each ⟳ row below has been rewritten from `web/src/styles/base.css` as it actually ships,
> and where the row's instruction was **not adopted** the row says so and gives the reason, because
> *unrecorded non-adoption* is the exact mechanism E-36 §2 was written to stop. Four halves went
> unadopted and are marked in place: `.seg`'s `overflow: hidden` (kept — E-42 §8①), `.seg-opt`'s
> `5px 12px` (refused — unmeasured), `.tag-neutral`'s 200/700 ramp pair (refused — only the shadow was
> missing), and `.dialog-backdrop`'s `.34` scrim alpha (refused — the token is shared with the drawer).
> **`base.css` remains the authority; this table is what has to prove it still matches.**
>
> Two standing constraints override anything quoted here:
>
> - **No raw ramp step and no literal hex may be shipped.** E-32 §3 ruled that the prototype has no
>   semantic layer — it inlines ramp steps, and the ramps are a theme-invariant absolute lightness scale
>   (§1.4), so an inlined step draws the *same* colour in a dark scope and the contrast collapses. Where a
>   row below quotes `var(--color-neutral-N00)` or a hex, **a semantic token is to be derived** (light and
>   dark, and after the paper grain — E-35 §4). The rows below now name the tokens that were derived; the
>   six absolutes of the cream control set are in §1.5.
> - **No arbitrary radius.** E-32 §2 kept D-40's enforcement and bound the whitelist to what the
>   `--radius-*` tokens produce, plus `50%` / `9999px`. The prototype's literal `5px` (`.tag`), `6px`
>   (`.seg-opt`) and `8px` (`.seg`, `.dialog`) are **not** token values — `sm 3 / md 4 / lg 6 / pill 7`.
>   `6px` is `--radius-lg`; `5px` and `8px` were resolved rather than shipped — `.tag` ships
>   `--radius-sm`, `.seg` and `.dialog` ship `--radius-lg`.
>
> Nothing E-32 §4 or E-35 §7 refused is reversed here. In particular `.btn { justify-content: center }`
> stays refused — `.btn-block`'s flush-left rule (§0.3) is untouched by any row below.

| Class | Contract |
|---|---|
| `.btn` ⟳ | `inline-flex; align-items:center; justify-content:center; gap:6px; font-family:var(--font-heading); font-weight:800; font-size:14px; line-height:1.2; color:var(--color-text); background:transparent; cursor:pointer; padding: var(--space-2) calc(var(--space-3) * 1.2)` → **8px 14.4px**. The boundary and the corner both moved: ~~`border:1px solid transparent`~~ → **`border: 0`**, ~~`border-radius:0`~~ → **`border-radius: var(--radius-pill)`** (7px, E-32 §2). The transition covers **four** properties — `background`, `box-shadow`, `color`, `transform` — because the variants below move all four. **`font-weight` stays 800**: `soft-ui.css`'s 600 is E-42 §5's non-adopted row ▪3 (this row was never ⟳-amended, the label is the heading family, and E-32 kept 800). `.btn svg{display:block}`. `:disabled{opacity:var(--disabled-opacity) /* .45 */;cursor:not-allowed}`. Below 768: `min-height` **and** `min-width` = `var(--touch-min)` |
| `.btn-primary` ⟳ | `background:var(--color-accent); color:var(--on-accent); box-shadow:var(--shadow-sm)` — a filled control is a *raised* one (§0.2). Not `--color-bg`: the accent is a dark teal in both themes and `--color-bg` on it is 1.48:1 in the dark theme. Hover `--accent-hover`; active `background:var(--accent-press); box-shadow:var(--shadow-accent-inset); transform:translateY(1px)`. **The press inset is the accent absolute, not `--shadow-inset`** — the recess is cut into a fill that does not flip, and the light inset's cream up-left lobe would smear across a teal pill (§0.2, E-42 §3) |
| `.btn-secondary` ⟳ | **A raised cream pill, not a bordered ghost — in every scope.** Shipped: `background: var(--control-fill); color: var(--on-control); box-shadow: var(--shadow-sm)`; `:hover{ background:var(--control-fill-hover); color:var(--on-control-accent) }`; `:active{ box-shadow: var(--shadow-control-inset); transform: translateY(1px) }`. The three values E-36 flagged were all derived as **absolutes** (§1.5): the prototype's `var(--color-surface)` flips and would have made this a deep-teal pill on the viewer's deep-teal bar, `--color-accent-800` was a raw ramp step, and `#F8F4EC` had no token at all. `--shadow-sm` and **not** `--shadow-control-raised`: this pill sits on the page ground, which does flip. **The old `background: var(--press-tint)` on `:active` is deleted, not kept beside the inset** — a translucent tint *replaces* the fill, so for the length of a press the bar behind would show through the one surface the absolute exists to keep opaque. Two measured notes: on **light** this fill is 1.00 against `--color-surface`, so inside a dialog the only thing making it a button is `--shadow-sm` (E-42 §8); and a disabled one is 1.05 against the light ground, effectively invisible — not a violation, WCAG 1.4.3 exempts disabled components |
| `.btn-ghost` ⟳ | `color:var(--ink-dim); padding-inline:var(--space-1)`; hover `background:var(--ghost-hover)` (accent@10% light / accent-300@12% dark) **and** `color:var(--accent-text)`; active `background:var(--ghost-press)` (18% / 20%). **Not `--color-accent` at rest** — a ghost has no fill, so it is ink on the page ground and takes the neutral ink that flips; the accent moves to the hover, where the prototype puts it. **No inset and no `translateY`**, unlike the two filled variants: E-36 §3.4 presses *surfaces*, and an inset needs a fill to be cut into. ⚠️ **Open defect `al`: the hover ink is 3.90 washed on the dark `--color-surface`** — an AA fail affecting the sidebar's and the dialogs' ghost buttons. It is 4.70 on the dark *ground*, which passes. Not introduced by E-42 (the hover ink was `--accent-text` before it); fixing it means re-deriving either `--ghost-hover`'s dark alpha or the hover ink, and both need measuring first |
| `.btn-icon` | `width:36px; height:36px; padding:0` |
| `.btn-block` | `width:100%; margin-top:var(--space-2); justify-content:flex-start; text-align:left` ← **the flush-left rule** |
| `.tag` ⟳ | `inline-flex; align-items:center; font-size:11px; letter-spacing:.02em; padding:3px 10px; border:0; font-weight:600; border-radius:var(--radius-sm)`. The prototype's `5px` is **not** a `--radius-*` value and was resolved down to `--radius-sm` (3px), not shipped as written. `border:0` costs nothing here — the only consumer of the transparent border was `.tag-outline`, and that variant is a filled chip now |
| `.tag-accent` ⟳ | `bg accent-100 / fg accent-800`, **plus the `[data-theme='dark']` counterpart with the ends swapped** (`bg accent-800 / fg accent-100`). The ramps are theme-invariant, so a single declaration paints the same near-white slab on the dark ground. Swapping the ends rather than re-choosing the pair keeps the measured 11.36 exactly, and separates from the dark surface at 1.40 — the same order of separation the light chip has from the cream (1.08). A tag is a label, not a boundary |
| `.tag-accent-2` ⟳ | `bg accent-2-100 / fg accent-2-800`, same swapped dark counterpart. (E-32 collapsed accent-2 into the accent; the tokens survive only because this class and the Tailwind map still name them) |
| `.tag-neutral` ⟳ | **The shadow was adopted; the ramp pair was not.** Shipped: `background: var(--color-neutral-100); color: var(--color-neutral-800); box-shadow: var(--shadow-sm)`, with the swapped `[data-theme='dark']` counterpart (800 fill / 100 ink). **NOT ADOPTED (E-42 §5, E-36 audit row ⬛18): `soft-ui.css`'s 200/700 pair.** What was missing from this class was the *shadow*, not the fill — the shipped 100/800 pair is measured at 10.68:1 and already has its dark twin, and moving it one step buys nothing while costing two new semantic tokens and a second dark counterpart. A measured pair is not traded for an unmeasured one |
| `.tag-outline` ⟳ | **Not outlined any more — the name is now historical.** Shipped: `background: var(--color-surface); color: var(--accent-text); box-shadow: var(--shadow-sm)`. The `1px solid` accent border is gone. `--accent-text` is the semantic for the prototype's raw `--color-accent-700` — that step on light, accent-300 on dark. **`--color-surface` and not the cream `--control-fill`:** a tag is a label printed on the page, not a control, so its ground is the page's surface and flips with it |
| `.field > label` | `display:block; font-size:12px; margin-bottom:5px; color:var(--ink-label)` — text@78% light / @75% dark, 5.18 / 5.52 |
| `.input` ⟳ | **A recessed well, not a bordered box.** Geometry unchanged: `width:100%; min-height:36px; padding:6px 10px; font-size:14px; font-family:inherit`. The boundary moved: `border: 0; border-radius: var(--radius-md); background: var(--control-fill); box-shadow: var(--shadow-control-inset)`, with the ink set absolute for the same reason the fill is — `color: var(--on-control)`, `caret-color: var(--on-control-accent)`, `::placeholder: var(--on-control-dim)`. The prototype's `::placeholder: neutral-500` stays **refused** (2.37:1, E-32 §4 / E-36 §8). The `border-color` hover state has nothing left to move and is deleted outright. Focus: `box-shadow: var(--shadow-control-inset), 0 0 0 2px var(--color-hot); outline: none`. ~~the hot ring is **inside** the well, not an outline beside it~~ — **a render says otherwise: `0 0 0 2px` carries no `inset`, so it paints the 2px band just *outside* the border box, pixel for pixel where `outline: 2px; outline-offset: 0` was.** What actually changes is that the marker is one declaration with the recess instead of two properties that can be switched off separately — which is also why §0.6 has to restate it for forced colours. `@layer components` beating `@layer base` is what lets the local `outline: none` win; drop only that line and the field draws both rings. **`color-scheme: light` is load-bearing, not tidiness:** a field has parts this sheet does not paint — `::-webkit-search-cancel-button`, the native `<select>` menu, an inner scrollbar — and the UA colours those from the *scheme*. `tokens.css` sets `color-scheme: dark` on the dark theme, which was true while this fill flipped with it and became a lie the moment the fill was pinned to cream: the native clear ✕ was drawn near-white on cream, measured at **1.14:1**. Declaring the scheme here says what the fill says — this surface is light in every theme — and fixes the ✕, the caret and the menu together. (`appearance:none` on the ✕ would have hidden the symptom by deleting the control.) Below 768 `min-height` = `var(--touch-min)` (NFR-CMP-002) |
| `.radio` ⟳ | `inline-flex; align-items:center; gap:8px; font-size:14px; cursor:pointer`. `.dot` = `16×16; flex:0 0 16px; border-radius:50%; border:1.5px solid var(--control-border); box-sizing:border-box`. **Checked is ruling z (E-42 §1):** `border-color + background: var(--accent-fill)`, then `box-shadow: inset 0 0 0 1.5px var(--color-hot), inset 0 0 0 4px var(--color-bg)`. Not `--color-accent` for the body — it is 1.48:1 on the dark ground, so a checked radio would read as unchecked there; `--accent-fill` is 6.21. The hot ring is the *marker*, not the shape: hot alone is 2.83 on the dark ground and 2.28 on the dark surface, under the 3:1 floor. **The order of the two insets is load-bearing** — in a `box-shadow` list the *first* layer paints on top, so the 1.5px hot ring must precede the 4px `--color-bg` ring; put it second and the bg ring covers the outer 4px of the padding box and the marker silently disappears. The bg donut is 2.5px wide now, not 4px: hot took 1.5px of it, and that is intended. This is deliberately the same grammar as the checked `.seg-opt` — accent fill carries the shape, hot says *selected* |
| `.seg` ⟳ | **A recessed track that the options sit in**, not a box with dividers. Shipped: `display:inline-flex; border: 0; border-radius: var(--radius-lg); background: var(--control-well); box-shadow: var(--shadow-control-inset); padding: 3px; gap: 2px`. The prototype's `8px` is not on the scale and was resolved to `--radius-lg` (6); its raw `--color-neutral-200` became the absolute `--control-well` (§1.5), because this track is cream on the viewer's dark bar too. **NOT ADOPTED (E-42 §8①): this row's own instruction that `overflow: hidden` "goes with `padding`+`gap`". `overflow: hidden` stays.** That reasoning was half the truth — the declaration was clipping *elevation*, not just corners. The selected option carries `--shadow-control-raised`, whose up-left lobe is `rgb(255 253 246 / 0.9)` reaching **11px** (3px offset + 8px blur) while the track holds it off by **3px** of padding: remove the clip and a near-white halo paints 8px out onto whatever the track sits on. On the viewer's bar a real Chrome render measured **3.29:1** — *above* the 3:1 floor, so not a soft lift but a bright edge, the "white outline around every card" `tokens.css` condemns in its own dark-elevation block. `soft-ui.css` could not have known: it is light-only, and on cream the halo is invisible. This is E-36 §4's warning arriving a second time — a light-only instruction moved into a scope the prototype never had |
| `.seg-opt` ⟳ | `inline-flex; align-items:center; gap:6px; font-size:13px; cursor:pointer; white-space:nowrap; font-weight:600; border-radius:var(--radius-lg)` (= the prototype's 6px, which *is* a token value); no border at all, so **the `+ .seg-opt` left divider is deleted** — separation comes from the `gap` and the recessed track. Ink is `--on-control-dim`, the absolute: an unselected option sits on cream in every scope. Transition covers `background`, `color`, `box-shadow`. Checked → `background:var(--color-accent); color:var(--on-accent); box-shadow: var(--shadow-control-raised), inset 0 0 0 2px var(--color-hot)` — **`--shadow-control-raised`, not `--shadow-sm`**: this shadow falls on the cream track 3px away, not on the page, and the dark `--shadow-sm` leads with a `0 0 0 1px #3E5B57` hairline that is 6.19 washed against cream, i.e. a hard teal box instead of a lift. The hot ring stays for the reason E-32 §1 gave it — hot means *selected* — not as a crutch: on the cream well the accent fill is 6.41 washed, so the selection is legible without it. Hover (unchecked) is **colour only**, `--on-control-dim` → `--on-control-accent`; the product's `background: var(--hover-tint)` was **deleted**, because a theme-relative tint on a theme-invariant surface computed to **1.00:1** in the dark theme — the background hover did not happen at all (E-42 §8②). Focus: `outline: 2px solid var(--color-hot); outline-offset: -2px` via `:has(input:focus-visible)`. **NOT ADOPTED (E-42 §5, audit row ▪28): `padding: 5px 12px`.** The product keeps **`7px 12px`**, which is what this row itself instructed — "unless the 5px version is measured to still clear the target", and it has not been. Below 768 `min-height: var(--touch-min)` covers a phone either way, but 768–1023 is the tier where `--touch-min` is `0px` and the one most likely to be a tablet; a hit area is not shrunk on a guess |
| `.card` ⟳ | `flex column; gap:var(--space-2); padding:var(--space-3); background:var(--color-surface); border-radius: var(--radius-lg); box-shadow: var(--shadow-md)`. **A card is lifted, per §0.2** — `--shadow-md` and not a control token, because a card sits on the page ground, which flips. ~~that component still refuses a shadow citing the retired §0.2 sentence~~ — **fixed: `Card.tsx`'s docstring now withdraws the quote and points at `base.css`.** Measured while fixing it (E-42 §7): **`web/src/components/ds/Card.tsx` is imported nowhere in `web/src`** — not by a screen, not by a test. Every surface that looks like a card builds its own box out of utilities (`useLibrary.ts`'s `LIST_CARD_CLASS`, `ContinueCard`, `NextVolumeCard`'s `elev-lg`). So giving `.card` its shadow satisfied the contract without changing a pixel on screen, and this component is the DS's unused reference implementation of it |
| `.card-kicker` | `10px; ls .1em; uppercase; color:var(--accent-text)` — accent at 10px is paragraph-size, so it takes the readable accent, not `--color-accent` (§2.5) |
| `.card-title` | Archivo 800 17px / 1.2 |
| `.card-body` | `13px; opacity:.8; flex:1` |
| `.card-meta` | `flex; gap:6px; 11px; color: var(--ink-meta)` — text@75% light / @75% dark, 4.79 / 5.07. The DS's `@50%` was an AA fail at 11px |
| `.elev-sm/md/lg` | `box-shadow: var(--shadow-sm/md/lg)` |
| `.nav` | `flex; align-items:center; gap:var(--space-4); padding:var(--space-3) var(--space-4); border-bottom:2px solid var(--color-divider)` |
| `.nav-brand` | Archivo 800 18px; `margin-right:auto` |
| `.table` | `width:100%; border-collapse:collapse; font-size:14px`. `th` → `text-align:left; 11px; font-weight:400; ls .08em; uppercase; color:var(--ink-th)` (text@76% / @72%, 4.92 / 5.22); `padding:var(--space-2); border-bottom:2px solid var(--color-divider)`. `td` → `padding:var(--space-2); border-bottom:1px solid var(--color-divider)`. `tbody tr:hover` → `var(--row-hover-table)` (text@4% light / @5% dark) |
| `.dialog-backdrop` ⟳ | `position:fixed; inset:0; display:grid; place-items:center; padding:var(--space-4); background: var(--scrim-modal)` (text@50% light / black@60% dark, **not** neutral-900@50%); `z-index: var(--z-overlay)`; **and `backdrop-filter: blur(2px)` + the `-webkit-` spelling**, which is not decoration — without it the effect is simply absent in Safari. **NOT ADOPTED (E-42 §5, audit rows ▪32·33): the other half of the prototype's separation, `--scrim-modal` `.50 → .34`.** The prototype separates a dialog with blur *plus* a lighter wash; only the blur is taken. `--scrim-modal` is not the dialog's own token — `.drawer-backdrop` shares it — so thinning it would thin the drawer's scrim too, and the drawer has no blur to make up the difference (a full-viewport blur is the most expensive thing on a phone frame and was left off deliberately). **The prototype has no drawer at all** — it is desktop-only (§0.5) — so it never answered this question, and porting its alpha would change a screen it never saw. Moving the alpha requires deriving a dialog-only scrim token first |
| `.dialog` | `width:min(440px,100%); flex column; gap:var(--space-3); padding:var(--space-4); background:var(--color-surface); box-shadow:var(--shadow-lg); border-radius: var(--radius-lg)` — the prototype's `8px` is not on the scale (E-32 §2) |
| `.dialog-title` / `-body` / `-actions` | Archivo 800 20px / `14px opacity .85` / `flex; justify-content:flex-end; gap:var(--space-2); margin-top:var(--space-2)` |
| `.hr` | `height:2px; border:0; margin:var(--space-4) 0; background:var(--color-divider)` |
| `.ds-grayscale` | `filter: grayscale(1) contrast(1.08)`. **Renamed from the DS's `.grayscale`**: Tailwind ships a `grayscale` utility in a later layer, so the DS name would lose whenever both were applied |

### 2.4 App-shell CSS the prototype adds on top of the DS

```css
html, body { margin:0; padding:0; overflow:hidden; }   /* the app shell owns all scrolling */

::-webkit-scrollbar { width:12px; height:12px; }
::-webkit-scrollbar-thumb { background: rgb(32 30 29 / .30); }  /* text @ 30% */
::-webkit-scrollbar-track { background: transparent; }

input[type=range] { -webkit-appearance:none; appearance:none; background:transparent; }
input[type=range]::-webkit-slider-runnable-track { height:2px; background: var(--color-divider); }
input[type=range]::-webkit-slider-thumb {
  -webkit-appearance:none; width:12px; height:16px; margin-top:-7px; background: var(--color-accent);
}

@keyframes shimmer { 0%{opacity:.3} 50%{opacity:.7} 100%{opacity:.3} }
@keyframes spin { to { transform: rotate(360deg) } }
```

> The block above is the **prototype's** CSS, kept verbatim. Two rules of it are superseded downstream and
> the difference is not a regression: the range input carries a real hit box (`height:24px`, `--touch-min`
> below 768) with a `12×18` / `16×28` thumb per **E-28** — a range with no height collapses onto its 2px
> track — and the thumbnail strip suppresses the 12px scrollbar on itself, because on a 72px track it ate a
> sixth of the height. Both are specified in §6.7.

Root shell: `height:100vh; display:flex; flex-direction:column; overflow:hidden;
background:var(--color-bg); color:var(--color-text); font-family:var(--font-body); font-size:14px`.
Note the **14px** app base size — smaller than the DS's 15px `body`, because density beats comfort here
(design.md principle 4).

### 2.5 Accent discipline

The accent-to-ground pair is tuned to **≥3:1** — enough for icons, large text and interface chrome, **not
for body copy**. For paragraph-size accent text use `--accent-text` (accent-700 on light, accent-400 on
dark). The prototype obeys this: progress percentages, the `이어보기` page counter, WARN log levels and the
scan-log summary all use accent-700, never `--color-accent`.

Red runs as a *field* (a solid fill) in exactly these places and nowhere else:
`.btn-primary`, the checked `.seg-opt`, the `완독` corner badge, the `암호화`/`손상` badge, the
`이미지 로드 실패` badge, progress-bar fills, the slider thumb, the sidebar brand square, the active
sidebar row's 3px left border, and the current-page thumbnail border. Everything else is ink on ground.

### 2.6 Icons

Lucide (`lucide-react`), tree-shaken per-icon. Interface size 16px inside `.btn`, 14px inline.
Never a CDN sprite sheet. The prototype ships text-only chrome (`←`, `⌘K`, `↑`/`↓`, `↵`) — keep those as
text, and only reach for Lucide where a real icon is clearer than a glyph.

---

## 3. Application shell

```
<div class="app">                       height:100vh; flex column; overflow:hidden; bg; text; 14px
  ├─ if (roots.length === 0) → Onboarding                     (§4.6)
  └─ else
     └─ <div class="shell">              flex:1; display:flex; min-height:0
         ├─ <Sidebar/>                   240px fixed                        (§4.1)
         └─ <main>                       flex:1; flex column; min-width:0; min-height:0
             ├─ <TopBar/>                flex:0 0 auto                      (§4.2)
             └─ Home (§4.3) | SeriesDetail (§5)
  ── portals ──
  ├─ <Viewer/>                           position:fixed inset:0 z-60        (§6)
  ├─ <CommandPalette/>                   z-80                               (§5.2)
  ├─ <ShortcutsDialog/>                  z-80                               (§5.3)
  └─ <SettingsDialog/>                   z-80                               (§5.4)
```

Z-index ladder: content `0` → sticky section header `2` → viewer `60` → dialogs/palette `80`.
One step is **internal to the viewer** and does not belong on this ladder: the two chrome bars take
`3` (**E-28**, §6.6), which orders them against the end-of-volume scrim *inside* the viewer's own
stacking context and never against anything on the list above.

Routing (the prototype uses a `route` enum; use React Router with the same shape):

| Route | Screen |
|---|---|
| `/` | Home / Library |
| `/series/:sid` | Series detail |
| `/series/:sid/books/:bid?page=n` | Viewer (renders over the shell, not instead of it) |

`settings` / `palette` / `shortcuts` are **state, not routes** — they are overlays over whatever is beneath.

---

## 4. Screen 1 — Home / Library

Reference: [`library-grid-1440.png`](./ui-shots/library-grid-1440.png)

![Library, grid mode, 1440px](./ui-shots/library-grid-1440.png)

### 4.1 Sidebar

`width:240px; flex:0 0 240px; border-right:2px solid var(--rule-strong); background:var(--color-surface);
display:flex; flex-direction:column; min-height:0`

| Block | Spec |
|---|---|
| **Brand** | `padding:12px 16px; border-bottom:2px solid rule; display:flex; align-items:center; gap:8px`. 14×14px `background:var(--color-accent)` square + `SHELF` in Archivo 800 17px, `letter-spacing:.02em` |
| **Scroller** | `flex:1; overflow-y:auto; min-height:0; padding-bottom:16px` |
| **Section label** | `루트`: `padding:12px 16px 4px`. `목록`: `padding:16px 16px 4px`. Both `font-size:10px; letter-spacing:.1em; text-transform:uppercase; color:var(--ink-dim)` |
| **Nav row** | `display:flex; align-items:center; gap:8px; padding:7px 16px; cursor:pointer; border-left:3px solid X; background:Y`. Inactive → `X=transparent, Y=transparent`, label `color:var(--ink-dim)`. Active → `X=var(--color-accent), Y=var(--nav-active)`, label `color:var(--ink)`. Hover → `background:var(--row-hover)` (text@6%). Label `flex:1; min-width:0; ellipsis; nowrap; font-size:13px`. Count `font-size:11px; font-variant-numeric:tabular-nums; color:var(--ink-dim)` |
| **Footer** | `border-top:2px solid rule; padding:12px 16px; display:flex; flex-direction:column; gap:8px` |
| ↳ Scan indicator | `display:flex; align-items:center; gap:8px; cursor:pointer; font-size:11px; letter-spacing:.02em; color:var(--ink-muted)`; hover `color:var(--color-accent)`. Dot: `7×7px; flex:0 0 7px`, `background: var(--color-accent)` while scanning else `var(--ink-faint)`. Label ellipsis/nowrap. Click → opens Settings (scan log). |
| ↳ Buttons | `display:flex; gap:8px`. `설정` = `.btn.btn-secondary` `flex:1; justify-content:flex-start; font-size:12px`. `?` = `.btn.btn-secondary` `font-size:12px` |

Root list = `roots` (user-named, with series count). Smart lists, fixed order and semantics:

| Key | Label | Predicate |
|---|---|---|
| `reading` | 읽는 중 | `0 < progress < 1` |
| `added` | 최근 추가 | recently-modified window |
| `done` | 완독 | `progress >= 1` |

Active-scope styling: [`library-sidebar-scope-active-1440.png`](./ui-shots/library-sidebar-scope-active-1440.png)

### 4.2 Top bar

`flex:0 0 auto; border-bottom:2px solid rule; display:flex; align-items:center; gap:12px; padding:12px 16px`

Left → right:

1. **Back button** — *series detail only*. `.btn.btn-secondary` `font-size:12px`, label `← 라이브러리`.
2. **Search** — wrapper `position:relative; flex:1; max-width:400px`.
   `.input` with `padding-right:52px`, placeholder `시리즈 검색 (초성 가능)`.
   `⌘K` chip: `position:absolute; right:9px; top:50%; transform:translateY(-50%); font-size:11px;
   letter-spacing:.04em; color:var(--ink-dim); border:1px solid var(--rule); padding:0 5px`.
   Show `Ctrl K` on non-Apple platforms.
3. **Spacer** `flex:1`.
4. **Scan progress** — only while `scanning`. `display:flex; align-items:center; gap:8px; font-size:11px;
   tabular-nums; color:var(--ink-muted)`; track `96×2px background:var(--fill-track)`, fill
   `height:100%; width:{pct}%; background:var(--color-accent)`; then `{pct}%`.
   See [`library-scanning-progress-1440.png`](./ui-shots/library-scanning-progress-1440.png).
5. **Sort** — `gap:8px`. Label `SORT` `font-size:10px; letter-spacing:.1em; uppercase; color:var(--ink-dim)`.
   `<select class="input">` `width:auto; min-width:132px; cursor:pointer; font-size:13px`.
   Options: `이름` / `수정일` / `최근 읽은 순` / `용량` / `권 수` (`name|mtime|read|size|vols`).
6. **View toggle** — `.seg` with two `.seg-opt` radios: `그리드` / `리스트`. Checked option is an accent field.

### 4.3 Continue-reading row (이어보기)

**Hidden entirely when there is nothing in progress** (and hidden during the skeleton state).

`padding:16px` ~~`; border-bottom:2px solid rule`~~ — **no border.** E-32 replaced the divider with space
and the cards' own elevation (`ContinueRow.tsx`, `flex-none p-4`).

- Header: `display:flex; align-items:baseline; gap:8px; margin-bottom:12px`.
  `<h6>이어보기</h6>` + count `font-size:11px; tabular-nums; color:var(--ink-dim)` (e.g. `5개`).
- Track: `display:flex; gap:12px; overflow-x:auto; padding-bottom:` ~~`4px`~~ **`16px`** (`pb-4`,
  `--space-4` — the hover lift needs somewhere to go or the scroller clips the card's shadow).
  Max 5 cards.
- Card: ~~`flex:0 0 300px`~~ **`flex:0 0 218px`, and `flex:0 0 269px` at ≥768** — see the note below;
  `display:flex; gap:12px; padding:12px; background:var(--color-surface);
  cursor:pointer` ~~`; border:1px solid var(--rule)`; hover → `border-color:var(--color-accent)`~~
  **→ `--radius-lg` + `--shadow-md`, lifting to `--shadow-lg` on hover (E-32)**.
  Click → resume that book at its saved page.
  - Thumb ~~`flex:0 0 66px; height:99px`~~ **`flex:0 0 96px; height:144px`** (still 2:3 — at 66px a
    title in the cover art was unreadable); `overflow:hidden; background:var(--fill-track)`.
  - Body `flex:1; min-width:0; display:flex; flex-direction:column; gap:5px`:
    - Title — Archivo ~~800~~ **700** (`font-bold`; E-32's commit changed it) 13px / 1.2,
      `-webkit-line-clamp:2`.
    - Volume name — 11px `color:var(--ink-muted)`, ellipsis nowrap.
    - Spacer `flex:1`.
    - Page counter — 12px tabular, `color:var(--accent-text)`, format `` `${page} / ${total}p` ``.
    - Bar — `height:` ~~`3px`~~ **`6px`, and a `--radius-full` pill, not a square bar**;
      `background:var(--fill-track)` + `--shadow-inset`, fill ~~`var(--color-accent)`~~
      **`var(--accent-fill)`** (`ProgressBar`'s defaults — `ContinueCard` passes no `height` and no
      `tone`). The accent itself is 1.09:1 on the trough in dark; `--accent-fill` is the token that
      moves up the ramp there (E-32).

> **Amended by E-37 — the card is 218 / 269px, and `300px` never shipped a day in its life.** What ships
> is `flex-[0_0_218px] md:flex-[0_0_269px]` — `web/src/features/library/ContinueCard.tsx`, which is the
> source of truth for this bullet and for the 이어보기 column of the §7 matrix at ≥1024.
>
> **Where the numbers actually came from, because the first draft of this note got it wrong.** 272 / 336
> and the 96×144 thumb entered in **session 5**, applied **판정 없이** — deliberately without a ruling,
> as one line of a dimensions table (`docs/HANDOFF.md` §1.0e: *"이어보기 카드 300→336px(터치 272), 커버
> 66×99→96×144"*, under *"나머지(브랜드·아이콘·치수)는 충돌이 없어 판정 없이 반영했다"*). They are in the
> **first commit** and every commit since. **E-32 did not widen the card and did not enlarge the thumb**:
> its own commit leaves `flex-[0_0_272px] md:flex-[0_0_336px]` and `h-[144px] w-[96px]` byte-identical
> (`git show 27b122d -- web/src/features/library/ContinueCard.tsx`), and the E-32 ruling text contains
> neither number. E-32 restyled this card — radius, shadow, hover lift, title weight — and that is all.
> **So until E-37 these dimensions had no ruling behind them at all**, which is exactly why the spec
> could carry `300px` for ten sessions with nothing to contradict it.
>
> Both numbers are **border-box**, so the arithmetic an implementer actually needs is
> `width − 96 thumb − 12 gap − 24 padding`: the body column is **86px** below 768 and **137px** at and
> above (it was 140 / 204, measured in Chrome). The two-line clamped title and the truncated volume name
> are sized against *that*, not against the card. The 12px gap, the 12px padding, the 5px body gap and
> the counter format did not move.
>
> **Four other values in this section were already wrong and are struck above, not silently corrected**
> — the section's `border-bottom`, the track's `padding-bottom`, the card's border-and-hover, the title
> weight and the bar. Every one of them is a pre-E-32 contract that survived the ruling which retired
> it: the same disease **E-36** §2 diagnosed, in the same file, found again by re-measuring instead of
> re-reading. The card border in particular is **E-36**'s own work to close, and is flagged rather than
> fixed here because a width edit quietly rewriting the skin is how the two got out of step to begin
> with.

### 4.4 Section header (sticky)

`padding:16px 16px 12px; display:flex; align-items:baseline; gap:12px; position:sticky; top:0;
background:var(--color-bg); z-index:2; border-bottom:1px solid var(--rule)`

`<h4>{scopeLabel}</h4>` (전체 시리즈 / 읽는 중 / 최근 추가 / 완독 / root name) +
`font-size:11px; tabular-nums; color:var(--ink-dim)` result count (`24개 시리즈`).

### 4.5 Body

Container `padding:16px`. Exactly one of: skeleton, empty, grid, list.

#### Grid mode

`display:grid; gap:16px; grid-template-columns: repeat(auto-fill, minmax(var(--grid-min), 1fr))`
where `--grid-min` is `152px` at desktop (see §7 for the responsive values; the prototype exposes it as a
110–280px knob).

Card = `display:flex; flex-direction:column; gap:7px`:

**Cover** — `position:relative; aspect-ratio:2/3; overflow:hidden; background:var(--color-surface);
border:1px solid var(--rule); cursor:pointer`.

| Layer | Spec |
|---|---|
| Fallback cover (always beneath the real image; **FR-LIB-008**) | `position:absolute; inset:0; background-image: repeating-linear-gradient(135deg, var(--fill-subtle) 0 8px, var(--fill-track) 8px 16px); display:flex; flex-direction:column; justify-content:flex-end; padding:8px`. Kicker `font-size:9px; letter-spacing:.1em; uppercase; color:var(--ink-dim); margin-bottom:5px` = `` `${FORMAT} · NO THUMBNAIL` ``. Title Archivo 800 12px / 1.15, `color:var(--ink)`, `-webkit-line-clamp:4` |
| Cover image | `position:absolute; inset:0; width:100%; height:100%; object-fit:cover` — fades in over the fallback; **no layout shift, ever** (UI-5.3) |
| Format badge | `position:absolute; top:0; left:0; font-size:9px; letter-spacing:.08em; padding:2px 6px; background:var(--color-text); color:var(--color-bg)` → `ZIP` / `FOLDER` / `PDF` |
| Done badge | `position:absolute; top:0; right:0`, same metrics, `background:var(--color-accent); color:var(--color-bg)` → `완독`. Only when `progress >= 1` |
| Hover overlay | `position:absolute; inset:0; opacity:0; transition:opacity .12s; display:flex; flex-direction:column; justify-content:flex-end; gap:4px; padding:8px; background:var(--scrim-cover)`. On hover **and on keyboard focus-within** → `opacity:1`. Contains `.btn.btn-primary.btn-block` (`margin:0; font-size:12px`) labelled `이어 읽기` when in progress else `읽기 시작`, and a second `.btn.btn-block` with `background:var(--color-bg); color:var(--color-text); margin:0; font-size:12px` labelled `상세`. **The scrim itself is `pointer-events:none` permanently** — it spans `inset-0` above the cover button with no `z-index`, so a scrim that took pointer events would make the cover unclickable by mouse forever (a mouse must hover before it can click). Only the two buttons flip to `pointer-events:auto`, and only under the same gate that makes them visible |
| Progress bar | Only when `0 < progress < 1`. `position:absolute; left:0; right:0; bottom:0; height:4px; background:var(--fill-track-2)`, fill `width:{pct}%; background:var(--color-accent)` |

**Below the cover** — title `font-size:12px; line-height:1.3; -webkit-line-clamp:2`; meta row
`display:flex; gap:8px; font-size:11px; tabular-nums; color:var(--ink-dim)` → `22권` `4.4 GB`.

Hover state: [`library-grid-card-hover-1440.png`](./ui-shots/library-grid-card-hover-1440.png)

![Grid card hover overlay](./ui-shots/library-grid-card-hover-1440.png)

> **Amended by E-29 — what a touch device gets is *nothing here*, and that is the specification.**
> A pointer that cannot hover never opens this overlay, so on such a device both buttons are inert. This
> section used to say only "revealed on hover" and leave the other side blank; the blank is now filled, and
> it is filled with **no fallback**. What is lost there is a shortcut and no destination: `상세` and the
> cover are the *same* action, and for a series with nothing started `이어 읽기` is that action too (there is
> no book id to resume to). The one thing genuinely lost — reopening a *started* series' last-read volume at
> its saved page — is what the series screen's own always-visible `이어 읽기` does (§5.1), one tap behind the
> cover this card already answers, and what the 이어보기 row does with no hover gate at all (§4.3,
> FR-LIB-010).
>
> **Do not copy `VolumeTile`'s `[@media(hover:none)]` fallback (§5.3) here.** That tile carries one because
> its screen has no second route and its overlay is a 66×29px corner button; this overlay is
> `--scrim-cover` (ink @ 72 %) across `inset-0`, so the same classes would paint out **every cover in the
> grid on every touch device**.
>
> **If this is ever reversed**, the scrim keeps `pointer-events:none` *unconditionally* and only the two
> buttons may flip — otherwise the cover stops being clickable at all, which is the defect E-29 names and
> the reason the row above states the invariant. A fallback also needs the overlay's own tests rewritten in
> the same change: headless Chrome reports `(hover: none)` as **true**, so the coverage would pass with the
> gate deleted.

#### List mode

Reference: [`library-list-1440.png`](./ui-shots/library-list-1440.png)

![Library, list mode, 1440px](./ui-shots/library-list-1440.png)

design.md principle 1: **list mode is co-equal with grid, not a fallback.** Same data density priority.

Column template (identical on the header and every row):

```
grid-template-columns: 32px minmax(0,1fr) 66px 64px 78px 100px 148px;
gap: 12px; align-items: center;
```

| # | Column | Header | Cell |
|---|---|---|---|
| 1 | thumb | — | `24×36px`; `background-image: repeating-linear-gradient(135deg, var(--fill-subtle) 0 5px, var(--fill-track) 5px 10px); border:1px solid var(--rule)` |
| 2 | 시리즈명 | sortable | `min-width:0; ellipsis; nowrap; font-size:13px` |
| 3 | 형식 | static | `<span class="tag …">` — **ZIP → `.tag-neutral`, FOLDER → `.tag-accent`, PDF → `.tag-outline`** |
| 4 | 권 | sortable | `font-size:12px; tabular-nums; text-align:right` |
| 5 | 용량 | sortable | `12px; tabular; right; color:var(--ink-muted)` |
| 6 | 수정일 | sortable | `12px; tabular; right; color:var(--ink-dim)` — `YYYY-MM-DD` |
| 7 | 진행률 | static | `display:flex; align-items:center; gap:8px`: bar `flex:1; height:3px; background:var(--fill-track)` with fill `var(--color-accent)` (or **`var(--ink)`/neutral-800 when 100%**), then label `width:54px; text-align:right; font-size:11px; tabular` — `완독` / `34%` / `—`; color `var(--accent-text)` when progress > 0 else `var(--ink-faint)` |

Header row: `padding:8px; border-bottom:2px solid var(--rule-strong); font-size:11px; letter-spacing:.08em;
uppercase; color:var(--ink-dim)`. Sortable cells `cursor:pointer`; the active one takes `color:var(--ink)`
and appends ` ↑` / ` ↓`. First click on 시리즈명 sorts **ascending**; first click on 권/용량/수정일 sorts
**descending**; clicking the active column flips direction.
See [`library-list-sorted-size-desc-1440.png`](./ui-shots/library-list-sorted-size-desc-1440.png).

Data row: `padding:4px 8px; border-bottom:1px solid var(--rule); cursor:pointer`; hover
`background:var(--row-hover)`. Click → series detail.

> **Virtualise both modes** (FR-LIB-007). Target: first paint of 1,000 series < 1.5s (NFR-PRF-003).
> `@tanstack/react-virtual` over a fixed row height (list) / fixed row height derived from the computed
> column width (grid). The sticky section header must sit outside the virtual scroller.

#### Loading — skeleton

Reference: [`library-skeleton-loading-1440.png`](./ui-shots/library-skeleton-loading-1440.png)

Same grid template, 18 placeholder cells, **no layout shift** (UI-5.3). Continue-reading row is hidden.
Each cell: `display:flex; flex-direction:column; gap:7px` containing

- `aspect-ratio:2/3; background:var(--fill-track); animation: shimmer 1.6s ease-in-out infinite;
  animation-delay: {(i % 6) * 0.12}s`
- `height:10px; width:84%; background:var(--fill-track)` — same animation/delay
- `height:10px; width:44%; background:var(--fill-subtle)` — same animation/delay

#### Empty — no search results

Reference: [`library-empty-search-1440.png`](./ui-shots/library-empty-search-1440.png)

`padding:32px 0; display:flex; flex-direction:column; align-items:flex-start; gap:12px;
border-top:2px solid var(--rule-strong); border-bottom:2px solid var(--rule-strong)`

`<h3>검색 결과 없음</h3>` · `<p style="color:var(--ink-muted)">초성 검색도 지원합니다. 다른 표기를 시도해 보세요.</p>`
· `.btn.btn-secondary` `검색 지우기`.

The two 2px rules are the whole design — the empty state is a *band*, not a centered illustration
(design.md principle 3: empty states are common and must not look impoverished).

### 4.6 Empty — no roots registered (onboarding)

Reference: [`onboarding-no-roots-1440.png`](./ui-shots/onboarding-no-roots-1440.png)

![Onboarding, no roots](./ui-shots/onboarding-no-roots-1440.png)

Replaces the entire shell (no sidebar, no top bar).
`flex:1; display:flex; align-items:center; justify-content:center; padding:32px`;
inner `max-width:520px; display:flex; flex-direction:column; align-items:flex-start; gap:16px`:

1. Kicker `font-size:10px; letter-spacing:.18em; uppercase; color:var(--color-accent)` → `SHELF`
2. `<h1>읽을 폴더를 등록하세요</h1>` (42px Archivo 800) — flush left, never centered
3. `<hr class="hr" style="width:100%;margin:0">`
4. `<p style="font-size:15px; color:var(--ink-muted); text-wrap:pretty">` the explanatory sentence
5. `display:flex; gap:8px; margin-top:8px` → `.btn.btn-primary` `루트 추가` + `.btn.btn-secondary` `설정`
6. Suggested-paths block: `margin-top:16px; width:100%; border-top:2px solid var(--rule-strong);
   padding-top:12px; font-size:12px; letter-spacing:.02em; color:var(--ink-dim);
   display:flex; flex-direction:column; gap:2px`

---

## 5. Screen 2 — Series detail

Reference: [`series-detail-grid-1440.png`](./ui-shots/series-detail-grid-1440.png)

![Series detail, volume grid](./ui-shots/series-detail-grid-1440.png)

Reuses the same sidebar and top bar; the top bar gains the `← 라이브러리` button at position 1, and the
grid/list toggle now switches the **volume** list.

### 5.1 Header

`display:flex; gap:24px; padding:24px 16px; border-bottom:2px solid var(--rule-strong)`

- **Cover** — `flex:0 0 176px; height:264px; overflow:hidden; background:var(--fill-track);
  border:1px solid var(--rule)`. Falls back to the same striped placeholder as the grid card.
- **Body** — `flex:1; min-width:0; display:flex; flex-direction:column; gap:12px`:
  - `<h2>` series title, `text-wrap:pretty`
  - Path — `font-size:12px; letter-spacing:.02em; color:var(--ink-dim); ellipsis; nowrap`.
    *The only place a filesystem path is shown* (UI 5.3 / design.md IA).
  - `<hr class="hr" style="margin:4px 0">`
  - **Stat strip** — `display:flex; gap:32px`. Each stat `display:flex; flex-direction:column; gap:2px`:
    label `font-size:10px; letter-spacing:.1em; uppercase; color:var(--ink-dim)`;
    value **Archivo 800 22px, tabular-nums**. Order: `권` · `용량` · `형식` · `진행률`.
    The 진행률 stat is `gap:4px; min-width:200px`, value in `var(--accent-text)`, and carries a
    `height:4px` bar (`background:var(--fill-track)` / fill `var(--color-accent)`) underneath.
  - Spacer `flex:1`
  - **Action row** — `display:flex; gap:8px; align-items:center`:
    `.btn.btn-primary` (`이어 읽기` if in progress else `읽기 시작`) ·
    `.btn.btn-secondary` `처음부터 읽기` · `.btn.btn-secondary` `이 시리즈 재스캔` ·
    spacer `flex:1` · `읽기 방향` label (10px, .1em, uppercase, `--ink-dim`) ·
    `.seg` `L→R` / `R→L`.

**Reading-direction default** (FR-VWR-002). ~~`dirs[seriesId] ?? (root is a manga root ? 'rtl' : 'ltr')`.
Persist per series.~~ **Superseded — conflict resolution C-9 (`impl-plan.md`) and ruling E-33.** There is no
"manga root" metadata to key a heuristic on, so the heuristic never existed in the product; and the
persisted direction is **per book**, on the server (`PUT /api/books/{bid}/prefs`), falling back to the
global default in `/api/settings`. The sentence above is kept struck through rather than deleted because
two sessions re-derived it from here and it needs to stay findable.

The `.seg` on **this** screen is the seed C-9 describes: it is client-only (`store/seriesDir.ts`,
`localStorage`), it never writes the server, and per **E-33 §2** it seeds the direction of a book opened
from this screen **only when that book has no override of its own**. Before E-33 it seeded nothing at all —
the value was written and displayed but never travelled to the viewer, so the control looked like it worked
and did not. A test that asserts on the store cannot see that; assert on what the viewer opens with.

### 5.2 Volume list header

`padding:16px 16px 12px; display:flex; align-items:baseline; gap:12px; border-bottom:1px solid var(--rule)`
→ `<h6>권 목록</h6>` + `font-size:11px; tabular; color:var(--ink-dim)` count (`12권`).

### 5.3 Volumes — grid mode

Container `padding:16px`; grid `gap:12px; grid-template-columns: repeat(auto-fill, minmax(128px, 1fr))`.
Natural sort (FR-IDX-007).

Tile `display:flex; flex-direction:column; gap:6px; cursor:pointer`:

- Box — `position:relative; aspect-ratio:2/3; overflow:hidden;
  background-image: repeating-linear-gradient(135deg, var(--fill-subtle) 0 8px, var(--fill-track) 8px 16px);
  border:1px solid X`; hover `border-color:var(--color-accent)`.
  `X` = `var(--color-accent)` when broken · `var(--ink-muted)`/neutral-700 when started · `var(--rule)` otherwise.
- Volume number — `position:absolute; inset:0; display:flex; align-items:center; justify-content:center;
  font-family:heading; font-weight:800; font-size:26px`, `color: var(--ink-dim)` when finished else
  `var(--ink-faint)`.
- **Error overlay** (encrypted / corrupt — FR-IDX-010, design.md 화면 2 오류 상태):
  `position:absolute; inset:0; background: rgb(77 23 14 / .82)` (accent-900@82%);
  `display:flex; flex-direction:column; align-items:flex-start; justify-content:flex-end; gap:4px; padding:8px`.
  Badge `font-size:9px; letter-spacing:.08em; padding:2px 6px; background:var(--color-accent);
  color:var(--color-bg)` → `암호화` / `손상`.
  Reason `font-size:10px; line-height:1.25; color:var(--color-accent-200)` →
  `비밀번호가 필요한 ZIP` / `중앙 디렉터리 손상`.
  Broken tiles are **not clickable**.
- Progress bar (started only) — `position:absolute; left:0; right:0; bottom:0; height:4px;
  background:var(--fill-track-2)`, fill `var(--color-accent)`.
- Name `font-size:12px; ellipsis; nowrap`; page count `font-size:11px; tabular; color:var(--ink-dim)` → `214p`.

Error tile in context: [`series-detail-volume-error-badge-1440.png`](./ui-shots/series-detail-volume-error-badge-1440.png)

![Broken volume tile](./ui-shots/series-detail-volume-error-badge-1440.png)

### 5.4 Volumes — list mode

Reference: [`series-detail-volume-list-1440.png`](./ui-shots/series-detail-volume-list-1440.png)

`grid-template-columns: 26px minmax(0,1fr) 66px 62px 132px; gap:12px; align-items:center;
padding:4px 8px; border-bottom:1px solid var(--rule); cursor:pointer`; hover `var(--row-hover)`.
No header row (volumes are naturally ordered; sorting is not offered).

| # | Cell |
|---|---|
| 1 | `20×30px` striped thumb (5px stripe pitch), `border:1px solid X` — same `X` rule as the tile |
| 2 | Volume name, `font-size:13px; ellipsis; nowrap` |
| 3 | `.tag` format badge |
| 4 | Page count, `12px; tabular; right; color:var(--ink-muted)` |
| 5 | Bar `flex:1; height:3px` + state label `width:46px; right; 11px; tabular` → `ERR` (accent) / `100%` / `34%` (accent-text) / `—` (`--ink-faint`) |

---

## 6. Screen 3 — Viewer (the core screen)

> **§6.1–§6.7 were reconciled against the shipped viewer on 2026-08-01.** They now describe the display
> model as rulings **E-27**, **E-28** and **E-30** left it. Where this section and those rulings disagreed, the
> rulings won — `docs/decisions.md` sits above this file in HANDOFF §3's priority list — and every number
> below was then taken from the **code**, not from either ruling's prose. Three claims in particular were
> removed then because they were pre-E-27 and would have read as a regression: a 2200 ms auto-hide, "mouse
> movement wakes the chrome", and a fourth 맞춤 mode. The amendment notes E-28 left in §6.5/§6.7 are kept.
>
> **One of those three has since come back.** Ruling **E-44** (2026-08-09) restored 화면 to the 맞춤 control
> and re-amended prd FR-VWR-005 to four modes, so §6.2 and §6.6 describe **four** again and a book stored at
> `contain` opens at `contain`. The other two claims stay gone — E-44 touches nothing in the display model.
>
> The `viewer-*.png` captures under `ui-shots/` are the **pre-E-27 prototype**. They remain the reference
> for everything except the display model: a bar visible in a capture is not evidence that a bar belongs
> on screen when the book opens.

`position:fixed; inset:0; z-index:60; background:var(--color-text); color:var(--color-bg);
display:flex; flex-direction:column; overflow:hidden`, plus a cursor driven by the **pointer's own**
idleness rather than by the chrome (§6.1).

Render it inside `<div data-theme="dark">` (§1.4) so every token inside resolves to the dark ramp
automatically, in both app themes.

### 6.1 Base state — chromeless

Reference: [`viewer-chromeless-base-1440.png`](./ui-shots/viewer-chromeless-base-1440.png)

![Viewer, chromeless base state](./ui-shots/viewer-chromeless-base-1440.png)

design.md principle 2: **while reading, there is no UI.** Nothing but the page on a near-black ground.

**The book opens in this state** (**E-27**): `open()` sets `chromeVisible: false`, and the chrome never
appears on its own afterwards. Before the ruling the first frame of a book was three rows of controls over
the page — the principle broken at the one moment it matters most. The viewer root carries
`data-chrome="visible" | "hidden"` for anything that needs to assert which state it is in.

| | Shipped | Taken from |
|---|---|---|
| Auto-hide | **2600 ms** after the last wake | `CHROME_AUTOHIDE_MS`, `web/src/store/viewer.ts:21` |
| …**held** while the pointer rests inside either bar | **Derived from where the pointer is, not latched by the bars** (**E-30**). One rule on the viewer root answers `pointerover`/`pointerout` with "is the node under the pointer inside `[data-role=viewer-top-bar]` or `[data-role=viewer-bottom-bar]`?" → `holdChrome()` / `releaseChrome()`. A wake during a hold does not arm a timer behind it. Without the hold the bars dissolve under a pointer resting on the control it is about to press. **A touch never holds** (`pointerType === 'touch'`): a tap inside a bar leaves the 2600 ms running like anywhere else, because a finger does not *rest* on a control | `trackChromeHover`, `web/src/features/viewer/ViewerPage.tsx:438`; the root's three handlers at `:501`–`:503`; `holdChrome`/`releaseChrome`, `web/src/store/viewer.ts:345`–`:358` |
| …and never fires mid-drag | the timer runs but declines to hide while `dragging` — the slider's preview lives in the bar it would have taken away | `web/src/store/viewer.ts:192` |
| Cursor | hidden **1600 ms** after the pointer stops moving, **independently of the chrome**; `pointer` over the two page-turn zones while awake, `default` over the centre and over the bars | `POINTER_IDLE_MS`, `web/src/features/viewer/ViewerPage.tsx:92`; the cursor itself at `:498` |
| Summons the chrome | a **44px screen-edge strip** at the top or the bottom (`mouseenter` *or* click) wakes it; the **centre 36 %** tap zone and the **`H`** key *toggle* it, so a second tap or press sends it away | `EDGE_STRIP_PX`, `ViewerPage.tsx:101`; the strips at `:511`–`:522`; §8.2, §8.3 |
| Does **not** wake it | moving the mouse — only the cursor comes back — and scrolling a webtoon, which reports its page through `syncPage` precisely so it cannot | `nudgePointer`, `ViewerPage.tsx:338`; `web/src/store/viewer.ts:293` |

The edge strips are rendered **only while the chrome is away**. Once the bars are up they are what the
pointer reaches for, and a strip over them would eat the first click on 뒤로. A click on a strip also stops
propagating, or the same click would turn a page in the zone underneath.

**The hold is one rule on the viewer root, and the bars are presentational** (**E-30**). It used to be
`onMouseEnter`/`onMouseLeave` on each bar, and that only ever engaged when the reader *crossed* into a bar:
because the strips exist only while the chrome is away, a wake from a strip unmounts the strip and mounts
the bar **in the same commit, under a pointer that has not moved**. The browser handles that — measured at
all four widths, Chrome re-hit-tests and dispatches `pointerover`/`mouseover` on the bar ~10 ms later
(22/24/24/26 ms at 1440/1024/768/400). React drops it: `onMouseEnter`/`onPointerEnter` are *synthesised*
from `mouseover`/`pointerover` and the synthesis returns early when `relatedTarget` is a React-managed
node, on the assumption that the pair already went out with the matching `…out` — which had gone to the
strip being removed. So no hold was taken, and at 768/400 the chrome dissolved 2600 ms later under a
resting pointer while at 1440/1024 it hid, was re-summoned ~13 ms later by the strip re-mounting under that
same pointer, and the bars blinked every 2.6 s indefinitely. `pointerover`/`pointerout` bubble, are not
synthesised, and are the browser's own answer to "what is under the pointer now", so one rule covers every
wake path: crossing in from the stage, strip hover, strip click, `H`, centre tap. Neither bar carries hold
handlers or a store subscription for it any more — the rule recognises them by `data-role`.

**Four independent releases, deliberately** (**E-30**). A hold that is never released disarms the auto-hide
for the rest of the session: `chromeHeld` is module-scoped and nothing renders from it, so there is no
state a reader could see or correct. The hold is taken without a crossing, so the release may not depend on
the matching crossing arriving either. (1) a `pointerout` whose destination is not a bar — including
`relatedTarget: null`, the pointer leaving the window; (2) `onPointerLeave` on the root; (3) a plain
`mousemove` over the stage, folded into `nudgePointer` (`ViewerPage.tsx:358`) — a different event family
that keeps arriving rather than firing once at a boundary; (4) `open()` and `close()` reset the flag
(`store/viewer.ts:237`, `:266`), so a viewer that was left, or a volume swapped underneath one, cannot
bequeath a hold. Mutation showed (1) and (2) are genuinely independent: breaking the `pointerout` release
left the window-exit case still releasing through the root's `pointerleave`.

**`holdChrome` and `releaseChrome` are idempotent, and that is a contract.** The derived rule fires many
times for one journey across a bar, so an unconditional `releaseChrome` would push the 2600 ms deadline
back on every mouse move over the page — a chrome that outstays its deadline for as long as the reader's
hand is on the mouse, which is what E-27 removed. A call that does not change the answer does nothing.

> **Known residual, measured, unruled (2026-08-01, 8세션차).** Pressing **`H`** to dismiss the chrome
> *while the pointer is resting inside one of the 44px edge strips* does not put it away: the strip
> re-mounts under the stationary pointer and wakes it again. Before E-30 that produced the endless 2.6 s
> blink described above; it now settles **visible and held** on the first cycle. Arguably correct — the
> pointer really is inside the bar, so the hold is doing its job — but it means `H` appears not to work
> from that one position. Not fixed, not ruled; recorded so it is not rediscovered as a new defect.

> **Found unreconciled, then fixed in code (2026-08-01, 8세션차).** For three sessions the "does not wake
> it" row was E-27's, binding, and **not honoured by the shipped build.** `useViewerStore.step()`
> implemented it exactly and `store/viewer.test.ts` pinned it — but the viewer screen never called `step`.
> Page turns went through `goNext`/`goPrev` → `goTo`, and `goTo` wakes unconditionally, because it is also
> the *control* path where the bar must not vanish under the press. So an arrow key or a side-zone tap
> raised the chrome, and the quiet page counter below — which renders only while the chrome is away — was
> unreachable after the reader's first turn.
>
> The reading path now commits through **`turnTo(page)`**: absolute, sets `loading`, never wakes. `goTo`
> is unchanged and still wakes for controls. The dead `step(delta)` is gone — dead code implementing a
> binding rule is what caused this.
>
> **Keep the guard at the screen, not only at the store.** Reverting the fix leaves the store's E-27 test
> green and reddens only `ViewerPage.test.tsx` — the blind spot reproduced exactly. That test asserts the
> quiet counter's text *advances* across the turn, so it cannot pass against a viewer that turns nothing.
> This is HANDOFF §6.5's pattern: the check watched the function the screen does not use.

**The opening hint.** While the chrome is away and the hint is unanswered, one line sits 22px off the
bottom, horizontally centred: `border:1px solid var(--color-neutral-800); background:var(--color-bg);
padding:7px 12px; font-size:11px; letter-spacing:.04em; text-align:center;
color:var(--color-neutral-400)`, `role="status"`, `pointer-events:none`. Its text is

`좌·우 클릭으로 페이지 · 중앙 클릭 또는 상하 가장자리로 컨트롤`

and it is **timed, not dismissible** — **3400 ms** (`CHROME_HINT_MS`, `store/viewer.ts:31`) — because a
hint that has to be closed is a second thing to learn. It is armed **once per entry**: a deliberate chrome
toggle answers it early (`toggleChrome` → `dismissHint`), and 다음 권 읽기 is a *continuation*, not an
entry, so it does not come back on the next volume (**E-28** §3).

**The quiet page counter.** With no bar to hold the number, `12 / 214` sits at `bottom:10px; right:14px`,
`font-size:11px; tabular-nums; letter-spacing:.06em; color:var(--ink-dim); pointer-events:none`. It is
suppressed wherever it would lie or crowd: while the chrome is up (the bar has its own counter), while the
loading indicator (§6.3) is showing, in 세로 — several pages are on screen at once — and at 맞춤 너비, where
the page is taller than the viewport. `ViewerPage.tsx:584`.

### 6.2 Stage

`flex:1; min-height:0; display:flex; align-items:center; justify-content:center`

| Property | single / double | vertical (webtoon) |
|---|---|---|
| `overflow` | `hidden` on the flow axis — that is the axis 양면 needs clipped. **`overflow-y:auto` at 맞춤 너비 and 원본**, where the fitted page is taller than the stage and clipping would cut the top and bottom off every page, and `overflow-x:auto` at 원본. When the stage scrolls it also anchors to `align-items:flex-start`, or the start of the overflow is out of reach (`stageScrollsY`, `web/src/features/viewer/fit.ts:158`) | `auto` |
| `padding` | `20px` (`0` when fit = 원본) | `0` |
| flow `flex-direction` | `row` (LTR) / **`row-reverse` (RTL)** | `column` |
| flow `gap` | `2px` | `12px` |

Page frame: `position:relative; flex:0 0 auto` plus the fit rule:

| Fit mode | Label | Sizing |
|---|---|---|
| `width` | 너비 | `width:100%; height:auto` — the stage scrolls vertically |
| `height` | 높이 | `height:100%; width:auto` — **default** (`DEFAULT_FIT`, `store/viewer.ts:34`) |
| `original` | 원본 | intrinsic size, stage padding drops to 0, stage scrolls on both axes |
| `contain` | 화면 | `max-width:100%; max-height:100%`. **The control is back** (**E-44**) — 맞춤 is *four* options again (§6.6) and prd FR-VWR-005 was re-amended to match, after **E-27** had cut it to three. 화면 sits **third** in the group, between 높이 and 원본. Nothing had to be re-derived: the geometry stayed in `fit.ts` and stayed tested throughout (`fit.test.ts:185,201,210,225,247`), and the wire enum never moved (`arch-backend.md` §7 lists `width｜height｜original｜contain` at `:407`, `:726`, `:1559`; `PUT /api/books/{id}/prefs` accepts `contain` — `internal/httpapi/api_test.go:832-846`). **A book stored at `contain` opens at `contain`**: E-27's coercion in `openingFit` (`store/viewer.ts`) was a corollary of the control's absence, and it goes out with the absence (E-44 §2) |

**RTL is the single easiest thing to get wrong.** In double-page mode with `R→L`, the flow container is
`row-reverse`, so page *n* renders on the **right** and page *n+1* on the **left**. Verify against
[`viewer-mode-double-spread-1440.png`](./ui-shots/viewer-mode-double-spread-1440.png) — the counter reads
`12 / 214` and page 13 sits to the left of page 12. Arrow-key semantics invert to match (§8).

Modes: [`viewer-mode-double-spread-1440.png`](./ui-shots/viewer-mode-double-spread-1440.png) ·
[`viewer-mode-vertical-scroll-1440.png`](./ui-shots/viewer-mode-vertical-scroll-1440.png)

**Double-page auto-split** (FR-VWR-004): a page whose intrinsic aspect ratio is landscape (`w > h`) is
rendered as a single page even in 양면 mode. Not implemented in the prototype — build it.

**Prefetch** (FR-VWR-006): `prefetch` pages ahead (default 4, range 0–12 from Settings), plus 1 behind.

### 6.3 Page loading state

Reference: [`viewer-page-loading-indicator-1440.png`](./ui-shots/viewer-page-loading-indicator-1440.png)

**Never blank the stage.** The previous page stays on screen; only a small indicator appears:

`position:absolute; bottom:24px; right:24px; display:flex; align-items:center; gap:8px; font-size:11px;
letter-spacing:.06em; text-transform:uppercase; color:var(--ink-faint)` →
spinner `width:11px; height:11px; border:2px solid var(--color-neutral-700);
border-top-color:var(--color-accent-400); border-radius:50%; animation: spin .7s linear infinite`
+ the word `페이지 로딩`.

The spinner is one of only two circles in the product (§0.1). It appears when a transition takes longer than
~240 ms; below that, don't show it at all.

### 6.4 Page load error

Reference: [`viewer-page-load-error-1440.png`](./ui-shots/viewer-page-load-error-1440.png)

Scoped to the failed page frame: `position:absolute; inset:0; background:var(--color-bg-of-viewer)`
(i.e. `#201e1d`); `display:flex; flex-direction:column; align-items:flex-start; justify-content:center;
gap:8px; padding:24px`:

- Badge — `font-size:10px; letter-spacing:.1em; padding:3px 7px; background:var(--color-accent);
  color:var(--color-bg)` → `이미지 로드 실패`
- Detail — `font-size:12px; color:var(--ink-faint)` → the entry name and cause,
  e.g. `page_044.jpg — CRC mismatch`
- `.btn.btn-secondary` `다시 시도`, `margin-top:4px; font-size:12px`, with the dark-chrome overrides
  (`color:var(--color-bg); border-color:var(--color-neutral-700)`)

> The prototype's page frame is a fixed 2:3 placeholder box, so this panel looks cramped in the screenshot.
> In the real viewer the frame is the full fitted page, so the error content sits flush-left in a
> comfortably wide column. Keep `align-items:flex-start` — do not centre it.

### 6.5 End of volume → next volume card

Reference: [`viewer-next-volume-card-1440.png`](./ui-shots/viewer-next-volume-card-1440.png)

![Next-volume card](./ui-shots/viewer-next-volume-card-1440.png)

Shown when `page >= totalPages` **and** mode ≠ vertical — in 세로, scrolling past the end *is* the end of
the volume. Derived as a selector off the store, never stored, so it cannot drift out of sync with the page.
Scrim `position:absolute; inset:0; background: var(--scrim-volume-end); display:flex; align-items:center;
justify-content:center; padding:16px` — the token resolves to `rgb(32 30 29 / .92)` (`tokens.css`).
Card `width:380px; max-width:100%; background:var(--color-bg); color:var(--color-text); padding:16px;
display:flex; flex-direction:column; gap:12px; box-shadow:var(--shadow-lg)`.

> Note this card deliberately flips back to the **light** palette — it is a surface floating above the
> reading ground, and the contrast is the point. Wrap it in `data-theme="light"` when the app theme is light,
> or keep it token-driven off the app theme rather than the viewer theme.

Contents: kicker `10px; ls .12em; uppercase; color:var(--color-accent)` → `권의 마지막 페이지` ·
title Archivo 800 20px / 1.15 (next volume name) · meta `12px; tabular; color:var(--ink-muted)` →
`214p · FOLDER` · `<hr class="hr" style="margin:0">` ·
actions `display:flex; gap:8px` → `.btn.btn-primary` `flex:1; justify-content:flex-start` `다음 권 읽기`
+ `.btn.btn-secondary` `시리즈로` + `.btn.btn-secondary` `읽음 표시` / `읽음 해제`.

The third button is FR-VWR-012's manual half (the automatic half is the server's `page === page_count` rule
on `PUT …/progress`). Ruling **E-12** shapes it: the label names the **action** in both directions, and it
is a bordered secondary rather than a bare accent ghost, so the card carries exactly one accent field — the
primary — as §2.5 requires.

On the **last** volume of a series `next_book_id` is `null`: the title, the meta line and 다음 권 읽기 are
all omitted, and the card is the kicker, the rule and the two remaining buttons.

> **Amended by E-28.** The scrim covers the stage but **not the chrome**: both bars carry `z-chrome` (3) and
> the scrim none, so 뒤로, the slider, the display controls and the strip stay live at the end of a volume.
> And 다음 권 읽기 is a *continuation* — it changes the book and the page and leaves the chrome, the strip and
> the already-answered opening hint exactly as the reader left them.

### 6.6 Top overlay

Reference: [`viewer-overlay-visible-1440.png`](./ui-shots/viewer-overlay-visible-1440.png)

![Viewer with overlays visible](./ui-shots/viewer-overlay-visible-1440.png)

The bar is **never unmounted.** It fades on `opacity` over `--chrome-fade` (`0.18s`, `tokens.css`) and
`pointer-events` is what actually turns it off. What changes with the chrome is the *box*:

| | Chrome up | Chrome away |
|---|---|---|
| Position | `position:relative; order:-9999; flex:none` — **a row of the viewer's own flex column** (**E-27**), so the stage shrinks beside the bar instead of being covered by it, including when the bar has wrapped to three rows | `position:absolute; inset-inline:0; top:0` — an overlay again, so a chromeless viewer is the full height of the screen and the layout never depends on an invisible box |
| Opacity | `1` | `0`, plus `pointer-events:none` |

`z-index: 3` in both states — the `chrome` step, a *viewer-internal* layer that has nothing to do with the
viewer's own `60` on §3's ladder (**E-28**). The end-of-volume scrim (§6.5) is later in the DOM and
carries no `z-index`, so without this it painted over the entire bar and the only way out of a finished
volume was the card's own two buttons.

`background:var(--color-text); border-bottom:2px solid var(--color-neutral-800); padding:8px 16px;
display:flex; flex-wrap:wrap; align-items:center; gap:12px`. Left → right:

1. `.btn.btn-secondary` `← 뒤로` — `color:var(--color-bg); border-color:var(--color-neutral-700); font-size:12px`
2. `.btn.btn-secondary` `라이브러리` (**E-34**) — the same treatment. `뒤로` returns to the series detail
   the reader came from; this one goes to the library. It **does not clear `scope` or `q`**: in this
   product `library_scope` is written back to the server (A-5), so the prototype's
   `scope:'all'; q:''` would make "I left the viewer" permanently unset the reader's sidebar filter.
   It sets the reveal target, and the library scrolls that card into view and focuses it — through the
   **virtualiser's `scrollToIndex`**, not `getElementById`, because both the grid and the list are
   virtualised and paginated so an off-window card is not in the DOM at all (E-34 §2).
3. Title block `min-width:0; display:flex; flex-direction:column`:
   series title Archivo 800 13px, ellipsis nowrap; volume name 11px `color:var(--color-neutral-500)`, ellipsis nowrap
4. Spacer `flex:1`
5. **Override chip** (**E-33 §3**), rendered only when `BookPrefs.is_override` is true. Label
   `이 권 전용 설정` — *권*, not *시리즈*: the override is per book (C-9, prd FR-VWR-002), and the
   prototype's per-series wording would name a scope the product does not have. It is a button; one press
   `PUT`s all three fields as `null` and clears the override. Filled `background:var(--color-hot);
   color:var(--on-hot)` — **not** an outline. `--color-hot` on the dark viewer ground measures **2.83:1**,
   below the prototype's own rejected 3.76 (E-32 §4) and far below AA for 11px text; filled it is **4.55**.
6. Three `.seg` groups, each `color:var(--color-bg); border-color:var(--color-neutral-700)`, with each
   `.seg-opt + .seg-opt` overriding `border-left-color: var(--color-neutral-700)`, and each carrying
   `flex:none; white-space:nowrap`:

| Group | Options |
|---|---|
| Display mode | `단면` / `양면` / `세로` |
| Reading direction | `L→R` / `R→L` |
| Fit | `너비` / `높이` / `화면` / `원본` — **four again** (**E-44**, which re-amended prd FR-VWR-005 after **E-27** had cut it to three; 화면 is third, icon lucide `Maximize` — §6.2) |

**The bar wraps; it does not collapse into a sheet** (**E-28**). All three groups stay inline at every
width and the bar is allowed to become two or three rows tall. Measured **in the product**, at the four
viewport widths the e2e projects actually run, with 맞춤 carrying its fourth option (**E-44**):
**57px @1440 · 108px @1024 · 103px @768 · 232px @400**. The figures this line used to carry — 55 @1440,
103 @900, 151 @500 — were taken against the design prototype when 맞춤 had three options and 900/500 were
the widths of the day; E-44 widened the group by ~77px and invalidated all three. The numbers now come
from `09-viewer-chrome.spec.ts`'s E-44 test, which prints them as `[measured]` on every round, so the next
person to move this control gets fresh ones out of a log they already have. **They are reported, not
asserted** — pinning a layout's height reddens a test for every legitimate change to the bar's contents.
Two rules do the wrapping: `flex-wrap` on the bar, and
`flex:none; white-space:nowrap` on each `.seg`, so a group moves to the next row **whole** or not at all.
That is exactly the breakage
[`viewer-overlay-400-broken.png`](./ui-shots/viewer-overlay-400-broken.png) captured — groups overflowing
and their labels breaking vertically.

The `⋯` `뷰어 컨트롤` bottom sheet this section and §7 used to specify below 1024 is **deleted**, and with
it the three mechanisms it needed to stay upright: pinning the chrome while the sheet was open, closing the
sheet when the chrome went anyway, and the ban on either bar carrying a `z-index`. That last one is why the
deletion had to come first — a stacking context on the bar trapped the sheet's escape inside it, and the
bottom bar then covered it (measured at 400px). Dropping the sheet is what let both bars take `z-chrome`.

**Opacity, not `display:none`.** The bars stay mounted and fade, which is what makes the 180 ms wake feel
instant and what stops the thumbnail strip (§6.7) from re-mounting — and re-requesting every visible
thumbnail — each time the chrome comes back. *(The pre-E-27 text justified this as avoiding "reflow on
every mouse move". Moving the mouse no longer does anything to the chrome — §6.1.)*

**The bar takes no part in the hover-hold beyond its `data-role`** (**E-30**). E-27's "a pointer resting in
the chrome pins it open" is *not* this component's hover behaviour and this component does not subscribe to
the store for it: the rule is stated once on the viewer root, as a `pointerover`/`pointerout` question
about what is under the pointer, and it finds this bar by `[data-role=viewer-top-bar]` — see §6.1. The
`onMouseEnter`/`onMouseLeave` pair that used to live here never engaged when the bar lit up *underneath* a
pointer that had not moved, which is exactly what waking from a screen-edge strip does.

**`파일이 변경되었습니다` (FR-VWR-009) follows the bar, not a fixed offset.** With the chrome up it is a
row **in the same column**, sharing `order:-9999` and sitting *after* the bar in the DOM, so it lands
directly under a bar of whatever height it has wrapped to; chromeless it goes back to being an overlay at
`top:56px`. It is deliberately **not** gated on the chrome (**E-27**): the chrome no longer appears on its
own, so a notice that rides along with it is a notice nobody is ever shown. The old fixed `top:56px`
cleared a one-row 53px bar by three pixels and nothing else — once the bar wrapped (103px at 900, 122px at
760) the notice was inside the bar's box and `z-chrome` painted over it. The offset had always been a
coincidence.

### 6.7 Bottom overlay

Same never-unmounted `opacity`/`pointer-events` fade contract as the top bar, the same two boxes
(`position:relative; order:9999; flex:none` up, `position:absolute; inset-inline:0; bottom:0` away), the
same `z-chrome` (3), and the same standing in the auto-hide's hold — which is **not** either bar's own
hover behaviour but the viewer root's `pointerover`/`pointerout` rule recognising this surface, thumbnail
strip included, by `[data-role=viewer-bottom-bar]` (**E-30**, §6.1).
`background:var(--color-text); border-top:2px solid var(--color-neutral-800)`.

**Thumbnail strip** (when open) sits *above* the control row **inside the same bar**, so opening it grows
one surface rather than introducing a second floating panel:
`overflow-x:auto; overflow-y:hidden; padding:12px 16px; border-bottom:1px solid var(--color-neutral-800)`,
**with the scrollbar suppressed on this one element** (`scrollbar-width:none` +
`::-webkit-scrollbar{display:none}`). §2.4 gives every scroller a permanent 12px bar; on a 72px track that
ate a sixth of the height and cut a grey band across the overlay (measured in Chrome on Linux). Wheel,
drag and programmatic scrolling all still work.

Each thumb `56×84`, **`48×72` from `md` (768) up** (**E-28** — below that the strip is a touch target, and
48px is under the 44px minimum once the 2px border and the gap come off). The virtualiser lays cells out by
absolute offset, so the **slot pitch** and the **track height** have to track the cell: **60/52** and
**84/72**. They used to be fixed at 52 and 72, so below 768 a 56px cell was drawn into a 52px slot inside a
72px box — overlapping every neighbour by 4px and clipped at the bottom, all three wrong at once.

> **Changing `estimateSize` alone does nothing** (E-28). `virtual-core` memoises its measurements on
> `[count, paddingStart, scrollMargin, getItemKey, enabled]` plus the item-size cache, and `estimateSize`
> is **not in that key** — so a slot change must also call `virtualizer.measure()`, which swaps the size
> cache for a fresh `Map` and *is* in the key. Measured at 900 → 700 with the strip open: cells grew to
> 56px while the pitch stayed 52px, and the track stayed 5,044px against the 5,820px 97 pages then needed,
> leaving the last 776px unreachable.

Cell chrome: `border:2px solid X; display:flex; align-items:flex-end; justify-content:flex-start;
padding:3px; font-size:10px; tabular; cursor:pointer`. Current page → `X = var(--color-accent)`, number in
`--ink`, plus `aria-current="true"`; otherwise `X = var(--color-neutral-800)` and the number in
`var(--color-neutral-600)`. The number sits **inside** the tile over the thumbnail and carries a
`--scrim-cover` chip: the reference capture puts it over the striped placeholder, where neutral-600 is
legible, but over a real manga page — white paper more often than not — the same grey vanished.
`202 queued` and `422 thumb_unavailable` are **normal** (FR-THM-004) and must not break the row; both
render the empty bordered box with its number, and the state is exposed on `data-thumb-status`.

Auto-scroll the current thumb into view (`align: 'center'`) on every page change, including changes that
came from a key or a tap zone rather than from the strip — and again whenever the slot size changes, since
the re-measure moved every offset out from under the reader.

> **Virtualised**, not the prototype's 60-thumb cap (`overscan: 8`). The real collection has a 1,540-page
> volume, and mounting 1,540 cells means 1,540 lazily generated server-side thumbnails requested at once —
> precisely the stall AC-008 forbids (books run to 500+ pages).

Reference: [`viewer-thumbnail-strip-1440.png`](./ui-shots/viewer-thumbnail-strip-1440.png)

![Viewer thumbnail strip](./ui-shots/viewer-thumbnail-strip-1440.png)

**Control row** — `padding:8px 16px 12px; display:flex; flex-wrap:wrap; align-items:center; gap:16px`
(`flex-wrap` per **E-28**: below ~520px the slider takes its own row rather than being crushed):

1. Counter `font-size:13px; tabular; min-width:84px; letter-spacing:.04em` → `12 / 214`
2. Slider wrapper `flex:1; position:relative`
   - `<input type="range" min=1 max={max(1, totalPages)} step=1 value={dragging ? dragPage : page}>`,
     styled per §2.4, with `aria-label="페이지"` and an `aria-valuetext` of `{value} / {totalPages}`
   - **The hit box is a stylesheet rule, not an inline height** (**E-28**). §2.4 sizes *every* range input
     — `24px`, and `--touch-min` (44px) below 768 — with a 12×18 thumb (16×28 below 768) on a 2px track. A
     range with no height collapses onto that track and leaves a hit area two pixels tall. This slider used
     to set 44px inline at every width, which held §7's touch minimum but made the bottom bar **12px taller
     than the design on every desktop**
   - In the viewer the input also carries `.on-dark`, which lifts the track to `--color-neutral-600`:
     `--color-divider` on the reading ground is all but the background colour, so the thumb had nothing to
     travel along
   - **Committed on release only.** `mousedown`/`touchstart` sets `dragging`; the store holds `dragPage`
     and the stage does not move until `mouseup`/`touchend`/`blur`. Dragging across a 1,540-page book would
     otherwise fire a page load *and* a progress write for every intermediate value. A `change` with no
     drag in progress commits immediately, so arrow keys on a focused slider still work
   - **Drag preview** (while dragging): `position:absolute; bottom:24px; left:{pct}%;
     transform:translateX(-50%); width:68px; height:102px; border:2px solid var(--color-accent);
     display:flex; align-items:flex-end; padding:4px; font-size:11px; tabular; pointer-events:none`,
     showing that page's thumbnail with the number over it.
     See [`viewer-slider-drag-preview-1440.png`](./ui-shots/viewer-slider-drag-preview-1440.png)
   - `left` = `(page - 1) / (totalPages - 1) * 100`, and `0` for a book of one page
   - The auto-hide (§6.1) declines to fire while `dragging` — the preview lives in the bar it would take away
3. `.btn.btn-secondary` `썸네일 · T` — `border-color:var(--color-neutral-700); font-size:12px`,
   `aria-pressed` = whether the strip is open; `color: var(--color-accent-400)` when it is, else
   `var(--color-bg)`

---

## 7. Responsive rules

**The prototype implements none of this.** Its sidebar is `flex:0 0 240px` with no query, and the list
grid is a fixed 7-column template. Evidence of the breakage, for contrast:
[`library-list-768.png`](./ui-shots/library-list-768.png) (title column collapses to zero, columns 4–7 clip),
[`library-grid-400-broken.png`](./ui-shots/library-grid-400-broken.png) (sidebar consumes 60% of the viewport),
[`viewer-overlay-400-broken.png`](./ui-shots/viewer-overlay-400-broken.png) (the three `.seg` groups overflow
and their labels break to vertical). Build the layer below.

![Library list at 768px — the prototype's fixed grid clipping](./ui-shots/library-list-768.png)

| Breakpoint | Sidebar | Grid | List | Continue row | Viewer |
|---|---|---|---|---|---|
| **≥1440** | Fixed, 240px | `--grid-min: 152px` → **6 cols @1440, 8 @1760** | All 7 columns | 269px cards, horizontal scroll | Full chrome, all 3 `.seg` groups inline |
| **1024–1439** | Fixed, 240px | `--grid-min: 150px` → **4–5 cols** | All 7 columns | 269px cards, horizontal scroll | Full chrome ([`viewer-overlay-1024.png`](./ui-shots/viewer-overlay-1024.png)) |
| **768–1023** | **Collapsed** to a 56px icon rail; the scope name moves into the section header. Full sidebar opens as an overlay drawer from a hamburger in the top bar | `--grid-min: 224px` → **3 cols** | **Drop 수정일 + 용량** → `32px minmax(0,1fr) 66px 64px 120px`. Format tag stays (it is primary metadata) | **260px cards** — ⚠ **not built**, ships 269 | **All 3 `.seg` groups stay inline**; the bar wraps to a second row instead (**E-28**). Measured **108px @1024 · 103px @768** with 맞춤's fourth option (**E-44**; the old "~103px at 900" was a three-option figure at a width no project runs) ([`viewer-overlay-768.png`](./ui-shots/viewer-overlay-768.png) predates the ruling and shows the old overflow menu) |
| **<768** | **Off-canvas drawer** (`position:fixed; inset:0 auto 0 0; width:280px`) over a `--scrim-modal` backdrop. Closed by default | `--grid-min: 150px; gap: 12px` → **2 cols** | **Two-line row**: line 1 = title; line 2 = tag · 권 · 용량 · progress at 11px. Grid becomes `32px minmax(0,1fr)` | **Full-width cards, one per screen, snap scroll** — ⚠ **not built**, ships a 218px scroller | Touch-first (§8.3). **All 3 `.seg` groups stay inline and the top bar wraps to three rows** — measured **232px @400** with 맞춤's fourth option (**E-44**; the old "~151px at 500" was a three-option figure, and the growth here is the largest of the four widths — 맞춤 goes from ~235px to ~312px against a 368px content box, so the fourth button is what pushes this bar onto a further row). E-44's e2e asserts the bar's own `scrollWidth` does not exceed its `clientWidth` at every project width, which is the check `noHorizontalScroll` structurally cannot make: the viewer root is `fixed inset-0 overflow-hidden`, so a control off the end of this bar never reaches `documentElement`. (**E-28** deleted the `⋯` bottom sheet this row used to require.) The bottom bar's control row wraps too, and the page slider grows to a 44px box |

> **Amended by E-37 — only the top two 이어보기 cells changed. The bottom two are requirements this
> table is owed, and they are still open.**
>
> Read the two halves of that column differently, because they have different standing:
>
> - **≥1440 and 1024–1439 — the code is the target, so the number moves.** This table's job below 1024
>   is to specify a layer that does not exist; at and above it, it is describing the desktop layout the
>   product already has. `300px` there was never a requirement — it was the **prototype's** width,
>   copied into this cell, and the product has never once shipped it (272/336 from the first commit,
>   now **269**). Those two cells now read 269px, sourced from
>   `flex-[0_0_218px] md:flex-[0_0_269px]` in `web/src/features/library/ContinueCard.tsx` (§4.3).
> - **768–1023 (260px) and `<768` (full-width, one per screen, snap scroll) — requirements, NOT spec
>   errors, and both are UNBUILT.** They belong to the responsive layer this section opens by saying
>   *"The prototype implements none of this … Build the layer below"*, which §0.5 lists as a thing an
>   implementer must not get wrong, and which §0.2's own amendment tells you to read as the
>   specification with the stylesheet behind it — **not the other way round**. They are restored and
>   marked ⚠, not overwritten with what ships. **A previous edit of this note rewrote both cells to
>   match the code. That was backwards** and is recorded here rather than quietly reverted, because
>   "the code disagrees with the spec" resolving as "amend the spec" is the failure mode this whole
>   section exists to prevent.
>
> **The gap, measured.** `ContinueRow.tsx` is `flex gap-3 overflow-x-auto pb-4` at **every** width and
> `ContinueCard` has exactly one breakpoint, Tailwind's `md` (768). There is **no `scroll-snap`
> anywhere in the tree** — zero hits for `scroll-snap` / `snap-x` / `snap-center` across `web/src` and
> `web/e2e` — so `<768` shows most of two 218px cards on a 400px viewport instead of one, and 768–1023
> is 9px wide of its tier.
>
> **Why nobody noticed for ten sessions: `web/e2e/07-responsive.spec.ts` has zero 이어보기 coverage.**
> The file drives the sidebar, the grid, the list and the viewer through all four tiers and never once
> mentions the shelf, so every cell in this column has been unchecked since the day it was written. A
> check that does not look at the thing cannot report the thing missing — the §6.5 pattern again.
> Closing this gap means a test at 400 and at 900 first, then the CSS.

Implementation: drive `--grid-min` from a single media-query block in `tokens.css` rather than sprinkling
Tailwind breakpoint variants across the grid class. `gap` stays `16px` down to 768 and drops to `12px` below.

Other rules that hold at every width:

- The section header stays `position:sticky; top:0` with `background:var(--color-bg)`; it must be opaque or
  cards will bleed under it.
- Any container that can overflow horizontally (continue row, thumbnail strip, list at ≤768) gets
  `overflow-x:auto` on **itself**; `body` never scrolls horizontally.
- Touch targets: minimum 44×44 CSS px below 768px. The 36px `.btn-icon` and 7px-padded nav rows need
  `min-height:44px` at that breakpoint.
- `.dialog` widths are already `min(Npx, 100%)` and survive down to 320px, but Settings must switch its
  two-column 캐시/읽기 기본값 row to a single column below 768.

---

## 8. Keyboard and interaction map

### 8.1 Global

| Key | Action |
|---|---|
| `Ctrl/Cmd + K` | Toggle command palette, clearing its query. `preventDefault()`. Works from anywhere including the viewer |
| `Esc` | Close palette / shortcuts / settings if any is open; **else** close the viewer |
| `?` | Open the shortcuts dialog |

### 8.2 Viewer only

| Key | Action |
|---|---|
| `→` | Forward one screen when `L→R`, back one when `R→L`. `preventDefault()` |
| `←` | The inverse of `→` |
| `Space` | Forward one screen **whatever the reading direction** — it is "continue", not a direction key. `preventDefault()` |
| `T` | Toggle the thumbnail panel |
| `F` | Toggle real browser fullscreen (`requestFullscreen` / `exitFullscreen`) on the viewer root. *(The prototype stubbed this to a chrome wake; the shipped viewer does it for real.)* |
| `H` | Show / hide the chrome (**E-27**). With the chrome off the mouse and the viewer opening without it, a reader who never goes near the screen edges needs a key that summons it. *Known residual: pressing it to **dismiss** the chrome while the pointer sits inside a 44px edge strip does not put it away — the strip re-mounts under the pointer and wakes it again. §6.1* |
| `Esc` | Exit the viewer |
| `1` / `2` / `3` | Display mode 단면 / 양면 / 세로 |

These are **viewer-only** keys and are not in the global map: a bare `2` must not switch display mode while
the library has focus. `Ctrl`/`Cmd`/`Alt` chords are left to the browser and to §8.1. Every key here is
inert while a dialog is on top (palette, shortcuts, settings), so `Esc` closes the dialog rather than the
book and typing `2` into the palette does not switch to 양면.

**The page step is not a fixed number.** `→`/`←`/`Space` and the tap zones all resolve through
`nextPage`/`prevPage`, whose stride is however many pages are *actually* on the stage — so a landscape scan
rendered single inside 양면 (FR-VWR-004) does not put the book one page out of phase. Clamped to
`[1, totalPages]`; landing on `totalPages` raises the next-volume card (§6.5). Every turn sets `loading`.
They commit through the store's **`turnTo(page)`** — absolute, and deliberately so: the stride belongs to
`fit.ts`, not to the store. See §6.1 for why that signature is load-bearing.

`wake()`: show the chrome, then hide it again **2600 ms** later (**E-27**), held while the pointer is
inside a bar — a property of *where the pointer is*, answered on the viewer root rather than by a hover
handler on the bar, and never taken for a touch (**E-30**, §6.1) — and suspended while the slider is being
dragged. It is **not** bound to `mousemove` — nothing
in the reading path calls it. What calls it is listed in §6.1 and §8.3: the two screen edges, the centre
tap zone, `H`, and operating a control.

### 8.3 Pointer and touch

| Zone / gesture | Action |
|---|---|
| Mouse move anywhere in the viewer | Nothing to the chrome — **E-27** took the chrome off the mouse; only the cursor comes back. A move over the *stage* does **release** a hold if one is in force (**E-30**'s third release route), but it never summons the chrome and — because the release is idempotent — never pushes the auto-hide back |
| **Left 32%** of the stage — tap/click | Previous page in reading order (i.e. *next* page when RTL). `cursor:pointer` while the pointer is awake (E-28) |
| **Right 32%** of the stage — tap/click | Next page in reading order |
| **Centre 36%** — tap/click | Toggle chrome (FR-VWR-011, design.md 화면 3 모바일). **E-28** narrowed the centre from 40%: the two zones a reader aims at a hundred times a volume are the page turns |
| Horizontal swipe | Page turn, direction-aware; disabled in 세로 mode. **E-28**: ≥44px, `|dy| ≤ |dx|`, and thrown within 600ms |
| Vertical drag in 세로 mode | Native scroll |
| Slider `mousedown`/`touchstart` | `dragging = true` → show the drag preview |
| Slider `mouseup`/`touchend` | `dragging = false`, commit the page |
| Top / bottom **44px screen edge** — `mouseenter` or click | Wake the chrome (**E-27**). Rendered only while the chrome is away, and the click does not propagate to the tap zone underneath |
| Pointer **resting** inside either bar | Holds the auto-hide off while it is there, and lets go the moment it is not (**E-27**, mechanism per **E-30**). Answered from `pointerover`/`pointerout` on the viewer root, so it engages even when the bar lights up *underneath* a pointer that never moved — which is what a wake from a screen edge does. Released four independent ways; §6.1 |
| **Tap** inside either bar | Does **not** hold (**E-30**): the chrome auto-hides 2600 ms after the last wake exactly as it does elsewhere. `pointerType === 'touch'` is what tells a finger from a mouse — Chrome's compatibility mouse events do not, which is how the shipped build ended up pinning the chrome open for good after one tap in the bottom bar at 400px. There is no pointer *resting* on a control on a touch screen, and E-27's justification for the hold goes with it |
| Grid card hover | Reveal the action overlay. Mirror it on `:focus-within` so keyboard users get the same actions. A pointer that **cannot** hover gets nothing here, deliberately — ruling **E-29**, §4.5 |

### 8.4 Command palette

Reference: [`command-palette-recent-1440.png`](./ui-shots/command-palette-recent-1440.png)

![Command palette](./ui-shots/command-palette-recent-1440.png)

`.dialog-backdrop` with `z-index:80; place-items:start center; padding-top:12vh`.
`.dialog` `width:min(620px,100%); gap:0; padding:0`.

- **Query input** — a bare `<input>`, deliberately **not** `.input` (E-42): `border-bottom:2px solid
  var(--rule-strong); background:transparent; min-height:56px; font-size:17px; width:100%;
  padding-left:46px` (a `Search` glyph is absolutely positioned in that gutter) `; padding-right:16px;
  color:var(--ink); caret-color:var(--accent-text); placeholder:var(--ink-dim)`; placeholder
  `시리즈로 이동…`. Focus is the base layer's hot `:focus-visible` outline pulled **inside** the field
  with `outline-offset:-2px` — the field is the full width of a `p-0` panel with square corners, so any
  outset ring is drawn past the dialog's 6px radius and hangs a square red corner off it. No `autoFocus`
  attribute: `Dialog` focuses the first focusable itself, and React's `autoFocus` fires during commit,
  i.e. *before* `Dialog` captures the opener, which would make the palette restore focus to its own
  field on close.

  > **This bullet used to open "Query input — `.input` with `border:0; border-bottom:2px …;
  > background:transparent`".** That is the honest reading of what it was asking for — a big underlined
  > search line, not a field — and `.input` is now the opposite of that: a cream well with an inset shadow
  > and the absolute cream ink set (§2.3). It had shipped as `.input` plus four overrides that between
  > them cancelled every declaration of the class, and one of them was a defect: a Tailwind `bg-*` lands
  > after `@layer components`, so `bg-transparent` removed the well and left `--on-control` — the cream
  > set's ink — on the dialog's own surface, **1.40:1** in the dark theme (E-42 §7). Spelling the field
  > out is smaller than fighting a class whose every declaration has to be undone. `min-height` is 56px
  > rather than 52 because that clears NFR-CMP-002's 44px at *every* width, which `.input` only did below
  > 768 via a media query that no longer applies here.
- **Results** — `max-height:52vh; overflow-y:auto`.
  - Group label `padding:12px 16px 4px; font-size:10px; letter-spacing:.12em; uppercase; color:var(--ink-dim)`
    → `최근 항목` when the query is empty, `검색 결과` otherwise.
  - Row `display:flex; align-items:center; gap:12px; padding:7px 16px; cursor:pointer;
    background:{selected ? var(--nav-active) : transparent};
    border-left:3px solid {selected ? var(--color-accent) : transparent}`; hover `var(--hover-tint)`.
    Title `flex:1; min-width:0; ellipsis; nowrap; font-size:14px`; sub `font-size:11px; tabular;
    color:var(--ink-dim)` → `12권 · 3.4 GB`.
  - Empty → `padding:24px 16px; color:var(--ink-dim)` `검색 결과 없음`.
- **Footer** — `border-top:2px solid var(--rule-strong); padding:8px 16px; display:flex; gap:16px;
  font-size:11px; color:var(--ink-dim)` → `↵ 열기` · `esc 닫기` · `초성 검색 ㅎㅌㅂㅅㅋ`.

Behaviour: max 8 results. Empty query → series with progress > 0, most recent first. Non-empty →
substring match **or 초성 (initial-consonant) match** on the title. `↑`/`↓` move the selection (the first row
is preselected), `↵` opens, `Esc` closes.

**초성 search** (FR-LIB-006) — port this helper as-is:

```ts
const CHO = 'ㄱㄲㄴㄷㄸㄹㅁㅂㅃㅅㅆㅇㅈㅉㅊㅋㅌㅍㅎ';
export function chosung(s: string): string {
  let out = '';
  for (const ch of s) {
    const c = ch.charCodeAt(0) - 0xac00;
    out += (c >= 0 && c < 11172) ? CHO[Math.floor(c / 588)] : ch;
  }
  return out;
}
// match: title.toLowerCase().includes(q) || chosung(title).includes(q)
```

The same predicate powers the top-bar search field.
See [`command-palette-chosung-search-1440.png`](./ui-shots/command-palette-chosung-search-1440.png).

### 8.5 Shortcuts dialog

Reference: [`shortcuts-dialog-1440.png`](./ui-shots/shortcuts-dialog-1440.png)

`.dialog` `width:min(560px,100%)`.
Header `display:flex; align-items:baseline; gap:12px` → `.dialog-title` `키보드 단축키` +
`font-size:10px; letter-spacing:.12em; uppercase; color:var(--color-accent)` `뷰어`.
`<hr class="hr" style="margin:0">`.
Body `display:grid; grid-template-columns:1fr 1fr; gap:8px 24px`; each row
`display:flex; align-items:center; gap:12px; border-bottom:1px solid var(--rule); padding-bottom:6px`
with a key chip `min-width:52px; text-align:center; font-size:12px; padding:2px 7px;
background:var(--color-text); color:var(--color-bg)` and a `font-size:13px; color:var(--ink)` label.

Entries, in order: `← →` 이전 / 다음 페이지 · `Space` 다음 페이지 · `T` 썸네일 · `F` 전체화면 ·
`Esc` 뷰어 나가기 · `⌘K` 커맨드 팔레트 · `1 2 3` 단면 / 양면 / 세로 ·
**`H` 컨트롤 표시 / 숨기기** · **`좌 / 우 클릭` 이전 / 다음 페이지** · **`가운데 클릭` 컨트롤 토글** ·
`?` 키보드 단축키.

> The three bold rows are **E-27**'s, and are the ruling's own condition on itself: once the chrome stops
> appearing by itself, the ways to summon it have to be written down somewhere, and this sheet is the
> somewhere. Two consequences for the implementation. Key the rows on the **chord**, not the label — the
> mouse row says the same words as the arrow row, and two siblings with one React key is a bug waiting.
> And the palette chip is the one entry that is not literal: print `Ctrl K` off a Mac rather than a key
> the reader's keyboard does not have.

### 8.6 Settings dialog

Reference: [`settings-dialog-1440.png`](./ui-shots/settings-dialog-1440.png)

![Settings dialog](./ui-shots/settings-dialog-1440.png)

`.dialog` `width:min(760px,100%); max-height:88vh; overflow-y:auto; gap:24px`.
Header: `.dialog-title` `flex:1` `설정` + `.btn.btn-secondary` `font-size:12px` `esc`.

1. **루트 관리** — `<h6>` + one row per root:
   `display:flex; align-items:center; gap:12px; padding:8px 0; border-bottom:1px solid var(--rule)`.
   Name `13px`; path `11px; color:var(--ink-dim); ellipsis; nowrap`; stats `11px; tabular;
   color:var(--ink-muted)` → `21 · 4.9 TB`; `.btn.btn-secondary` `재스캔`; `.btn.btn-ghost` `제거`.
   Then `.btn.btn-secondary` `align-self:flex-start; margin-top:8px` → `+ 루트 추가`.
   - **스캔 진행 (new)** — between the `<h6>` and the first row, **present only while
     `ScanStatus.state !== "idle"`**. `display:flex; flex-direction:column; gap:4px`: a baseline row
     with `formatScanLabel` (`11px; color:var(--accent-text)` → `스캔 중 41 / 96`) and `scanPercent`
     (`11px; tabular; color:var(--ink-muted)` → `42%`), then `ProgressBar` `height:6`, then
     `` `${current_root} · ${current_item}` `` (`11px; color:var(--ink-dim); ellipsis; nowrap`), omitted
     when the wire sends `null` for both. Whole-run, never per row.
   - **스캔 실패 (new)** — `<p role="alert">` `11px; color:var(--accent-text)`, one per panel, carrying
     `POST /api/scan`'s refusal (arch §7.10: `400`, `404`, `409`, `503`).
   - `재스캔` is **disabled for the whole run**, not only while the `POST` is in flight.
   - **폴더 찾아보기 (new)** — the 루트 추가 form's path field gains a `.btn.btn-secondary` `찾아보기`
     beside its hint line, present whenever `Settings.server.root_editing_enabled` is true.
     **AMENDMENT A-12, ruling E-40.** It opens `FolderPicker` inline (not a nested dialog — this dialog
     already scrolls, and a modal over a modal has no place to put its scrim):
     `border:1px solid var(--rule); border-radius:4px; padding:8px; gap:8px`, with
     a crumb row (`11px; color:var(--ink-dim); ellipsis` — the listed path, or 탐색할 수 있는 폴더 at the
     top level) carrying `.btn.btn-ghost` `상위` and `처음으로`, a `max-height:220px; overflow-y:auto`
     list of one row per sub-directory (folder icon + name + chevron as one full-width button that
     **descends**, then `.btn.btn-secondary` `선택` that **picks**), and a footer rule above
     `.btn.btn-primary` `이 폴더 선택` + `.btn.btn-ghost` `취소`.
     - **Descending is always allowed; picking is gated.** An unselectable folder may still contain the
       one the user wants, so refusing to open an existing root would hide every sibling under it.
     - **The grey-out reason is printed inline, never only as a tooltip** (`11px; color:var(--ink-dim)`
       → 이미 등록된 루트 · 기존 루트의 상위·하위 폴더 · 앱 데이터·캐시 폴더가 안에 있음). The whole
       point of the server computing `BrowseEntry.selectable` is that the user learns it before the click.
     - **Picking fills the path field; it does not submit.** The label is still to be typed, and a picker
       that added the root on click would make the one irreversible-looking control here the one with no
       confirm step.
     - `truncated` renders `<p>` `11px; color:var(--accent-text)` telling the user to type the path.
       A capped list that does not say it was capped reads as complete (§6.5).
     - The typed field **stays and stays primary**: it is the only way to reach a directory outside
       `server.browse_bases`, and the picker is an accelerator over it rather than a replacement.
   - **추가한 루트는 즉시 읽힌다 (changed)** — the form's closing line is 추가하면 바로 읽기 시작합니다,
     not the old 서버를 다시 시작한 뒤 읽힙니다 (**E-40**). It is deliberately weaker than the promise it
     could make: the adoption can fail after the file write, and then the row itself falls back to
     재시작 후 적용. The row is computed from what the server actually did, so this line must not
     contradict it in advance.
2. **Two columns** `display:flex; gap:24px` (stack below 768px):
   - **캐시** — `<h6>` · value row `display:flex; align-items:baseline; gap:8px` with the number in
     **Archivo 800 32px tabular** and the unit in `13px; color:var(--ink-muted)` → `1.84` `GB / 4.00 GB` ·
     bar `height:4px` (`--fill-track` / accent) · note `11px; color:var(--ink-dim)` ·
     `.btn.btn-secondary` `전체 삭제` (FR-THM-008).
   - **읽기 기본값** — `<h6>` then rows `display:flex; align-items:center; gap:12px` with a
     `flex:1; font-size:13px` label:
     `읽기 방향` → `.seg` L→R / R→L ·
     `표시 모드` → `.seg` 단면 / 양면 / 세로 ·
     `프리페치 페이지` → `<input type=range min=0 max=12>` `width:130px` + value `width:20px; right; 13px; tabular` ·
     **`테마` → the prototype only prints the static text `다크 (뷰어 고정)`. Replace it with a real
     `.seg` of 라이트 / 다크 / 시스템 (NFR-CMP-003) that writes `data-theme` on `<html>`; keep a note beside it
     that the viewer stays dark regardless.**
3. **스캔 로그** — header `display:flex; align-items:baseline; gap:12px` with `<h6>` `flex:1` and a summary
   `11px; color:var(--accent-text)` → `경고 3 · 오류 2`.
   Body `border-top:2px solid var(--rule-strong); max-height:200px; overflow-y:auto; font-size:12px;
   letter-spacing:.01em`; each line `display:flex; gap:12px; padding:3px 0; border-bottom:1px solid var(--rule)`:
   timestamp (`tabular; color:var(--ink-dim)`), level (`width:48px; flex:0 0 48px; letter-spacing:.06em`,
   **INFO → `--ink-dim`, WARN → `--accent-text`, ERROR → `--color-accent`**),
   message (`min-width:0; ellipsis; nowrap; color:var(--ink)`).

> **Amended by E-38 (BINDING — 사용자 서명 2026-08-06; `docs/decisions.md` carries the text) — the dialog
> gains a scan progress block and a scan-failure alert, both inside 루트 관리.** The user's report was
> *"재스캔이 있을 경우, 처리진행 상태를 볼 수 있어야 하는데 안 보임"*, and re-measurement found the
> requirement it names, **FR-IDX-004**, already implemented **twice** — `TopBar` draws the 96×2px bar and
> `ScanIndicator` prints `스캔 중 {done} / {total}` in the sidebar footer (§4.1, §9 #11). Both of them sit
> **under `.dialog-backdrop`**, and the 재스캔 button that starts the run sits inside it. So the one screen
> a user can start a per-root scan from was the one screen that could not watch it, and the button
> produced no visible change of any kind.
>
> **Why it needs a ruling at all.** This section enumerates the dialog's contents, and §9 #11 catalogues
> `ScanIndicator` as `{ scanning, pct, label, onOpenLog }` with two states, "idle (grey dot)" and
> "scanning (accent dot + 96px bar **in the top bar**)". Neither text has a slot for a third site. The
> block above is therefore a new element and is written here **attributed**, not slipped in — the failure
> mode **E-36** §2 and **E-37** both diagnosed in this very file was a contract that outlived the ruling
> which retired it. It was drafted unsigned for exactly that reason and **has since been signed**, so the
> block and the code behind it now stand on a ruling rather than on a session report.
>
> **Two things it deliberately does not do.** It shows **no per-root progress**: `ScanStatus` has no
> per-root breakdown (`PerRoot` is dropped at the HTTP boundary and `Root` carries no scan state), so a
> bar on a root's row would be a claim the API cannot answer — `current_root` is printed as part of the
> current item's path instead. And it renders **nothing when idle**: `ScanIndicator` owns the
> `스캔 대기 — {n}분 전 완료` sentence, and a second copy here would be a second place to keep it in step.
>
> Transport is unchanged and is **D-16**'s: 1 s polling of `GET /api/scan/status`, never SSE. The numbers
> are `lib/format.ts`'s `formatScanLabel` / `scanPercent` — the sidebar's own, so the two cannot drift by
> a rounding rule — and the bar is `components/ds/ProgressBar` rather than the top bar's hand-rolled
> `<span>`, which carries no `role="progressbar"`.

---

## 9. Component inventory

One entry per component named in design.md's inventory, plus the shell pieces. Props are indicative
TypeScript, not final API.

### Shared domain types

```ts
type Format = 'zip' | 'folder' | 'pdf';
type ReadDirection = 'ltr' | 'rtl';
type DisplayMode = 'single' | 'double' | 'vertical';
type FitMode = 'width' | 'height' | 'original' | 'contain';  // C-2 — the wire value; the prototype's 'screen' is not it
type ViewMode = 'grid' | 'list';
type SortKey = 'name' | 'mtime' | 'read' | 'size' | 'vols';
type Scope = 'all' | 'reading' | 'added' | 'done' | RootId;

interface Series {
  id: string; title: string; short: string; format: Format;
  root: RootId; path: string;
  vols: number; sizeBytes: number; mtime: string;
  progress: number;          // 0..1
  coverUrl?: string;         // undefined ⇒ fallback cover
}
interface Volume {
  id: string; num: number; name: string; format: Format;
  pages: number; progress: number;
  error?: { kind: 'encrypted' | 'corrupt'; message: string };
}
```

| # | Component | Props (sketch) | States | Screenshot |
|---|---|---|---|---|
| 1 | `SeriesCard` (시리즈 카드 · grid) | `{ series: Series; onOpen(); onResume() }` | default · hover (action overlay) · focus-within · in-progress (4px bar) · finished (완독 badge) · no thumbnail (striped fallback) | [`library-grid-1440`](./ui-shots/library-grid-1440.png), [`library-grid-card-hover-1440`](./ui-shots/library-grid-card-hover-1440.png) |
| 2 | `SeriesRow` (시리즈 행 · list) | `{ series: Series; onOpen() }` | default · hover · in-progress · finished (bar turns `--ink`) · unread (`—`) | [`library-list-1440`](./ui-shots/library-list-1440.png) |
| 3 | `ContinueCard` (이어보기 카드) | `{ series: Series; volumeName: string; page: number; total: number; onResume() }` | default · hover (accent border) | [`library-grid-1440`](./ui-shots/library-grid-1440.png) |
| 4 | `VolumeTile` / `VolumeRow` (권 항목) | `{ volume: Volume; onOpen() }` | unread · in-progress · finished · **error (encrypted / corrupt, not clickable)** | [`series-detail-grid-1440`](./ui-shots/series-detail-grid-1440.png), [`series-detail-volume-error-badge-1440`](./ui-shots/series-detail-volume-error-badge-1440.png), [`series-detail-volume-list-1440`](./ui-shots/series-detail-volume-list-1440.png) |
| 5 | `ProgressBar` (진행 바) | `{ value: number; height?: 3\|4; track?: 'default'\|'over-art'; tone?: 'accent'\|'done' }` | 0% (hidden on cards) · partial · complete | all library shots |
| 6 | `FormatBadge` (형식 배지) | `{ format: Format; variant: 'corner' \| 'tag' }` | `corner` = ink field on the cover; `tag` = `.tag-neutral`(ZIP) / `.tag-accent`(FOLDER) / `.tag-outline`(PDF) | [`library-list-1440`](./ui-shots/library-list-1440.png) |
| 7 | `FallbackCover` (대체 커버) | `{ title: string; format: Format; size: 'card'\|'row'\|'hero' }` | always available beneath the image; stripe pitch 16px (card/hero) or 10px (row) | [`library-grid-1440`](./ui-shots/library-grid-1440.png) |
| 8 | `SidebarItem` (사이드바 항목) | `{ label: string; count?: number; active: boolean; onSelect() }` | default · hover · active (accent bar + tint) | [`library-sidebar-scope-active-1440`](./ui-shots/library-sidebar-scope-active-1440.png) |
| 9 | `SortSelect` (정렬 드롭다운) | `{ value: SortKey; onChange() }` | closed · open · plus the sortable list-header cells with ↑/↓ | [`library-list-sorted-size-desc-1440`](./ui-shots/library-list-sorted-size-desc-1440.png) |
| 10 | `ViewToggle` (뷰 토글) | `{ value: ViewMode; onChange() }` | grid selected · list selected (accent field) | [`library-grid-1440`](./ui-shots/library-grid-1440.png) / [`library-list-1440`](./ui-shots/library-list-1440.png) |
| 11 | `ScanIndicator` (스캔 인디케이터) | `{ scanning: boolean; pct: number; label: string; onOpenLog() }` — **`pct` is not a prop of the shipped component** (`components/shell/ScanIndicator.tsx` takes `{ scanning, label, onOpenLog, compact }`; the percentage is the top bar's, computed there from `scanPercent`). Flagged, not corrected — see E-36 §2 | idle (grey dot) · scanning (accent dot + 96px bar in the top bar) · **a third site: the 스캔 진행 block inside the settings dialog, which is not this component — §8.6 §1, E-38** | [`library-scanning-progress-1440`](./ui-shots/library-scanning-progress-1440.png) |
| 12 | `ViewerOverlayBar` (뷰어 오버레이 바) | `{ position:'top'\|'bottom'; visible: boolean; children }` | visible (`opacity:1; pointer-events:auto`) · hidden (`opacity:0; pointer-events:none`) | [`viewer-overlay-visible-1440`](./ui-shots/viewer-overlay-visible-1440.png), [`viewer-chromeless-base-1440`](./ui-shots/viewer-chromeless-base-1440.png) |
| 13 | `PageSlider` (페이지 슬라이더) | `{ page: number; total: number; onChange(); onDragStart(); onDragEnd() }` | idle · dragging (thumbnail preview above the thumb) | [`viewer-slider-drag-preview-1440`](./ui-shots/viewer-slider-drag-preview-1440.png) |
| 14 | `ThumbnailStrip` (썸네일 스트립) | `{ total: number; current: number; onJump(n) }` | closed · open · current-page highlighted (accent border) | [`viewer-thumbnail-strip-1440`](./ui-shots/viewer-thumbnail-strip-1440.png) |
| 15 | `CommandPalette` (커맨드 팔레트) | `{ open: boolean; query: string; items: Series[]; onQuery(); onPick() }` | recent (empty query) · results · empty · row 0 preselected | [`command-palette-recent-1440`](./ui-shots/command-palette-recent-1440.png), [`command-palette-chosung-search-1440`](./ui-shots/command-palette-chosung-search-1440.png) |
| 16 | `EmptyState` (빈 상태) | `{ title: string; body?: string; action?: {label, onClick} }` | no search results · no roots (onboarding variant) | [`library-empty-search-1440`](./ui-shots/library-empty-search-1440.png), [`onboarding-no-roots-1440`](./ui-shots/onboarding-no-roots-1440.png) |
| 17 | `GridSkeleton` (스켈레톤) | `{ count?: number }` | shimmer, staggered `(i % 6) * 0.12s` | [`library-skeleton-loading-1440`](./ui-shots/library-skeleton-loading-1440.png) |
| — | `PageStage` | `{ pages: Page[]; mode: DisplayMode; fit: FitMode; dir: ReadDirection }` | single · double (RTL reverses) · vertical · loading · error | [`viewer-mode-double-spread-1440`](./ui-shots/viewer-mode-double-spread-1440.png), [`viewer-mode-vertical-scroll-1440`](./ui-shots/viewer-mode-vertical-scroll-1440.png), [`viewer-page-loading-indicator-1440`](./ui-shots/viewer-page-loading-indicator-1440.png), [`viewer-page-load-error-1440`](./ui-shots/viewer-page-load-error-1440.png) |
| — | `NextVolumeCard` | `{ volume: Volume; onNext(); onBackToSeries() }` | shown at `page === total` and mode ≠ vertical | [`viewer-next-volume-card-1440`](./ui-shots/viewer-next-volume-card-1440.png) |
| — | `SettingsDialog` | `{ roots; cache; defaults; log }` | — | [`settings-dialog-1440`](./ui-shots/settings-dialog-1440.png) |
| — | `ShortcutsDialog` | `{ open }` | — | [`shortcuts-dialog-1440`](./ui-shots/shortcuts-dialog-1440.png) |

### Text catalogue (ko)

The prototype ships a full ko/en string table. Keep the Korean copy verbatim.

| Key | ko | Key | ko |
|---|---|---|---|
| `roots` | 루트 | `lists` | 목록 |
| `settings` | 설정 | `library` | 라이브러리 |
| `searchPh` | 시리즈 검색 (초성 가능) | `sort` | Sort |
| `sName / sMtime / sRead / sSize / sVols` | 이름 / 수정일 / 최근 읽은 순 / 용량 / 권 수 | `grid / list` | 그리드 / 리스트 |
| `continue` | 이어보기 | `detail` | 상세 |
| `doneBadge` | 완독 | `cTitle / cFormat / cVols / cSize / cMtime / cProgress` | 시리즈명 / 형식 / 권 / 용량 / 수정일 / 진행률 |
| `noResults` | 검색 결과 없음 | `noResultsSub` | 초성 검색도 지원합니다. 다른 표기를 시도해 보세요. |
| `clear` | 검색 지우기 | `volumes` | 권 목록 |
| `readFirst` | 처음부터 읽기 | `resume / start` | 이어 읽기 / 읽기 시작 |
| `rescan / rescanShort / remove` | 이 시리즈 재스캔 / 재스캔 / 제거 | `dir / back` | 읽기 방향 / 뒤로 |
| `single / double / vertical` | 단면 / 양면 / 세로 | `fitW / fitH / fitS / fitO` | 너비 / 높이 / 화면 / 원본 (**E-27** deleted `fitS` 화면; **E-44** put it back, third — §6.2, §6.6. `fitS` is the prototype's **string-table key**; the wire value is `contain` and the prototype's `screen` never reaches the product — **C-2**) |
| `thumbs` | 썸네일 | `loading` | 페이지 로딩 |
| `chromeHint` (**E-27**) | 좌·우 클릭으로 페이지 · 중앙 클릭 또는 상하 가장자리로 컨트롤 | `chromeToggle / tapPage / tapChrome` (**E-27**, §8.5) | 컨트롤 표시 / 숨기기 · 이전 / 다음 페이지 · 컨트롤 토글 |
| `loadFail / retry` | 이미지 로드 실패 / 다시 시도 | `volEnd` | 권의 마지막 페이지 |
| `nextVol / backToSeries` | 다음 권 읽기 / 시리즈로 | `palettePh` | 시리즈로 이동… |
| `openHint / closeHint / chosungHint` | 열기 / 닫기 / 초성 검색 ㅎㅌㅂㅅㅋ | `shortcuts / viewer` | 키보드 단축키 / 뷰어 |
| `rootMgmt / cache / clearCache` | 루트 관리 / 캐시 / 전체 삭제 | `cacheNote` | 썸네일 · 압축 해제 페이지 캐시 |
| `readDefaults / displayMode / prefetch / theme` | 읽기 기본값 / 표시 모드 / 프리페치 페이지 / 테마 | `scanLog` | 스캔 로그 |
| `onbTitle` | 읽을 폴더를 등록하세요 | `addRoot` | 루트 추가 |
| `onbBody` | ZIP · 폴더 · PDF가 담긴 루트를 지정하면 압축을 풀지 않고 그대로 훑어 시리즈로 정리합니다. | `recent / allSeries / reading / done / added` | 최근 항목 / 전체 시리즈 / 읽는 중 / 완독 / 최근 추가 |
| `scanIdle` | 스캔 대기 — {n}분 전 완료 | `scanRun` | 스캔 중 |

Formatting conventions: sizes → `X.X GB` when ≥1 GB else `NNN MB`; volume counts → `NN권`;
page counters → `N / Mp` (continue card) or `N / M` (viewer); dates → `YYYY-MM-DD`; percentages → `NN%`;
finished → `완독`; unread → `—`. Every numeric cell carries `font-variant-numeric: tabular-nums`.

---

## 10. Gaps between the prototype and the requirements

Things the prototype does **not** do that the URD/handoff require. Build them.

| # | Gap | Requirement |
|---|---|---|
| 1 | No responsive layer at all — fixed 240px sidebar, fixed 7-col list grid, overflowing viewer chrome | design.md 반응형 기준, NFR-CMP-002. See §7 |
| 2 | No light/dark theme switch (Settings prints static text) | NFR-CMP-003. See §1.4 + §8.6 |
| 3 | No left/right tap zones or swipe in the viewer — only a centre tap that toggles chrome | FR-VWR-011, design.md 화면 3 모바일. See §8.3 |
| 4 | `F` does not enter real fullscreen | FR-VWR-007 |
| 5 | No virtualisation — the list and grid render every item; the thumbnail strip caps at 60 | FR-LIB-007, NFR-PRF-003, AC-008 |
| 6 | No landscape auto-split in 양면 mode | FR-VWR-004 |
| 7 | Root filtering exists, but no explicit "manual mark as read/unread" control | FR-VWR-012 |
| 8 | DS stylesheet `@import`s Google Fonts | NFR-OPS-001/002. See §2.1 |
| 9 | Cover images are drag-and-drop `image-slot` stubs; real covers come from `GET /api/series/{sid}/cover` | FR-THM-003 |
| 10 | No progress export/import UI | FR-STT-004 (optional) |

---

## 11. Screenshot index

All files live in [`docs/ui-shots/`](./ui-shots/). Captured live from the running prototype in Chrome at
2× DPR; the number in each filename is the **CSS** viewport width.

| File | Shows |
|---|---|
| `library-grid-1440.png` | Home / library, grid mode, full chrome — sidebar, top bar, 이어보기 row, 6-col grid |
| `library-grid-card-hover-1440.png` | Series card hover: 72% ink scrim + two flush-left stacked buttons (읽기 시작 / 상세) |
| `library-list-1440.png` | List mode, all 7 columns, row hover, the three `.tag` variants, finished-row dark bar |
| `library-list-sorted-size-desc-1440.png` | Sortable header: active column in `--ink` with a `↓` arrow |
| `library-sidebar-scope-active-1440.png` | Sidebar active scope (읽는 중): accent 8% fill + 3px accent left border; scope name in the section header |
| `library-scanning-progress-1440.png` | Scan in progress: 96×2px bar + `63%` in the top bar, accent dot in the sidebar footer |
| `library-empty-search-1440.png` | No-results band between two 2px rules, with 검색 지우기 |
| `library-skeleton-loading-1440.png` | Shimmer skeleton grid, 이어보기 row suppressed, no layout shift |
| `onboarding-no-roots-1440.png` | First-run onboarding: accent kicker, 42px h1, 2px rules, primary/secondary pair, path hints |
| `series-detail-grid-1440.png` | Series detail header (176×264 cover, path, 4 stats, action row, 읽기 방향 seg) + volume grid |
| `series-detail-volume-error-badge-1440.png` | Scrolled volume grid — volume 9 encrypted: accent-900@82% scrim, 암호화 badge, reason text |
| `series-detail-volume-list-1440.png` | Volume list mode: 5-column rows, per-volume progress, `ERR` state |
| `viewer-chromeless-base-1440.png` | Viewer base state — nothing but the page on #201e1d, cursor hidden |
| `viewer-overlay-visible-1440.png` | Viewer with both overlays: back, title/volume, three `.seg` groups, counter, slider, 썸네일 · T |
| `viewer-thumbnail-strip-1440.png` | Thumbnail strip open; current page bordered in accent, toggle label in accent-400 |
| `viewer-mode-double-spread-1440.png` | 양면 mode under `R→L` — page 13 left, page 12 right, 2px gutter |
| `viewer-mode-vertical-scroll-1440.png` | 세로 (webtoon) mode — 4 stacked pages, 12px gaps, stage scrolls |
| `viewer-page-loading-indicator-1440.png` | Loading: previous page retained, small spinner + `페이지 로딩` bottom-right |
| `viewer-page-load-error-1440.png` | Page failure: 이미지 로드 실패 badge, `page_044.jpg — CRC mismatch`, 다시 시도 |
| `viewer-next-volume-card-1440.png` | End of volume: 92% scrim + light 380px card with kicker, next volume name, meta, actions |
| `viewer-slider-drag-preview-1440.png` | Slider drag: 68×102 accent-bordered preview above the thumb, page number bottom-left |
| `command-palette-recent-1440.png` | Palette with empty query — 최근 항목, row 0 preselected, hint footer |
| `command-palette-chosung-search-1440.png` | Palette filtered by the 초성 query `ㅎㅌ` |
| `shortcuts-dialog-1440.png` | Shortcuts dialog, 2-column grid of ink key chips |
| `settings-dialog-1440.png` | Settings: 루트 관리, 캐시 (32px figure + bar), 읽기 기본값, 스캔 로그 with WARN/ERROR colouring |
| `focus-visible-ring-1440.png` | 2px accent `:focus-visible` ring with 2px offset on the sidebar `?` button |
| `library-grid-1024.png` | Grid at 1024 — 4 columns, sidebar still fixed |
| `library-list-1024.png` | List at 1024 — all 7 columns still fit |
| `viewer-overlay-1024.png` | Viewer chrome at 1024 — all three `.seg` groups still inline |
| `library-grid-768.png` | Grid at 768 — 3 columns, but the sidebar is not yet collapsing |
| `library-list-768.png` | **Breakage evidence**: list at 768 — title column collapses, trailing columns clip |
| `viewer-overlay-768.png` | Viewer chrome at 768 — the fit `.seg` is crowding |
| `library-grid-400-broken.png` | **Breakage evidence**: at 400 the fixed 240px sidebar consumes 60% of the viewport |
| `viewer-overlay-400-broken.png` | **Breakage evidence**: at 400 the viewer's three `.seg` groups overflow and labels wrap vertically |
