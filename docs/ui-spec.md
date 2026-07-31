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

## 0. Five things an implementer must not get wrong

1. **Zero corner radius. Everywhere.** `--radius-sm/md/lg` are all `0px` *on purpose*. The only two curved
   things in the entire product are the radio `.dot` and the viewer's loading spinner (both true circles).
   No rounded cards, no rounded buttons, no rounded badges, no rounded inputs.
2. **Structure is drawn with rules, not with whitespace or shadows.** Major section boundaries are
   **2px** `--color-divider`; row boundaries are **1px**. Never soften them to hairlines and never replace
   a rule with padding. Only three things carry a shadow: `.dialog`, the viewer's next-volume card, and the
   `.elev-*` utilities.
3. **Button labels are flush left, never centered** — including inside wide buttons (`.btn-block`
   sets `justify-content: flex-start`). Headings and body copy are flush left too. See the grid card hover
   overlay ([`library-grid-card-hover-1440.png`](./ui-shots/library-grid-card-hover-1440.png)): both stacked
   buttons start their text at the left padding edge.
4. **The viewer is a dark ground built from the same tokens, not a second palette.** Viewer background is
   literally `var(--color-text)` (#201e1d) and its foreground is `var(--color-bg)` (#f3f2f2). §1.4 turns this
   into a proper theme. The viewer is dark **regardless of the app theme** (NFR-CMP-003).
5. **The prototype is desktop-only — the responsive layer does not exist yet and must be built.**
   Below ~1024px the fixed 240px sidebar and the 7-column list grid overflow and clip
   ([`library-list-768.png`](./ui-shots/library-list-768.png),
   [`library-grid-400-broken.png`](./ui-shots/library-grid-400-broken.png),
   [`viewer-overlay-400-broken.png`](./ui-shots/viewer-overlay-400-broken.png)).
   §7 specifies the responsive behaviour that must be added. Do not copy the prototype's fixed widths down
   the breakpoints.

---

## 1. Design tokens

### 1.1 Complete token table (light ground — verbatim from `styles.css`)

#### Roles

| Token | Value | Role |
|---|---|---|
| `--color-bg` | `#f3f2f2` | Page ground |
| `--color-surface` | `#eae9e9` | Cards, inputs, sidebar, dialogs |
| `--color-text` | `#201e1d` | Ink. **Also the viewer ground.** |
| `--color-accent` | `#ec3013` | The single accent (red) |
| `--color-accent-2` | `#e15b47` | Machine-derived stand-in. **Mono scheme — treat as one role with accent.** |
| `--color-divider` | `color-mix(in srgb, #201e1d 40%, transparent)` → `rgb(32 30 29 / 0.4)` | All rules and control borders |

#### Neutral ramp

| Token | Value | | Token | Value |
|---|---|---|---|---|
| `--color-neutral-100` | `#f8f4f4` | | `--color-neutral-600` | `#7d7979` |
| `--color-neutral-200` | `#eae7e7` | | `--color-neutral-700` | `#605d5d` |
| `--color-neutral-300` | `#d7d3d3` | | `--color-neutral-800` | `#444141` |
| `--color-neutral-400` | `#bab6b6` | | `--color-neutral-900` | `#2d2b2b` |
| `--color-neutral-500` | `#9b9797` | | | |

#### Accent ramp

| Token | Value | | Token | Value |
|---|---|---|---|---|
| `--color-accent-100` | `#fff2ef` | | `--color-accent-600` | `#dd2b0f` |
| `--color-accent-200` | `#ffe0d9` | | `--color-accent-700` | `#ae1800` |
| `--color-accent-300` | `#ffc4b8` | | `--color-accent-800` | `#7c1405` |
| `--color-accent-400` | `#ff9783` | | `--color-accent-900` | `#4d170e` |
| `--color-accent-500` | `#ff563c` | | | |

#### Accent-2 ramp (mono stand-in — kept only so both sets resolve)

| Token | Value | | Token | Value |
|---|---|---|---|---|
| `--color-accent-2-100` | `#fff2ef` | | `--color-accent-2-600` | `#c94b39` |
| `--color-accent-2-200` | `#ffe0da` | | `--color-accent-2-700` | `#9e3526` |
| `--color-accent-2-300` | `#ffc4b9` | | `--color-accent-2-800` | `#71261b` |
| `--color-accent-2-400` | `#ff9784` | | `--color-accent-2-900` | `#471d16` |
| `--color-accent-2-500` | `#ef6853` | | | |

#### Type, space, radius, elevation

| Token | Value |
|---|---|
| `--font-heading` | `"Archivo", system-ui, sans-serif` |
| `--font-heading-weight` | `800` |
| `--font-body` | `"Archivo", system-ui, sans-serif` |
| `--space-1` … `--space-8` | `4px · 8px · 12px · 16px · 24px · 32px` (keys 1,2,3,4,6,8 — **no 5 or 7**) |
| `--radius-sm` / `-md` / `-lg` | `0px` / `0px` / `0px` |
| `--shadow-sm` | `0 1px 2px color-mix(in srgb, #2d2b2b 14%, transparent)` |
| `--shadow-md` | `0 3px 10px color-mix(in srgb, #2d2b2b 16%, transparent)` |
| `--shadow-lg` | `0 12px 32px color-mix(in srgb, #2d2b2b 22%, transparent)` |

#### Base type scale (from the component layer)

| Element | Size | Notes |
|---|---|---|
| `body` | 15px / 1.55 / 400 | The prototype's app shell overrides to **14px** |
| `h1` | 42px | Archivo 800, lh 1.12, ls −0.015em |
| `h2` | 32px | " |
| `h3` | 25px | " |
| `h4` | 20px | " |
| `h5` | 16px | " |
| `h6` | 13px | " **+ `letter-spacing: .08em; text-transform: uppercase`** — this is the section-label style used all over the app |
| `figcaption` / `.text-muted` | 11px / — | `color-mix(in srgb, var(--color-text) 55%, transparent)` |

#### Derived tint values used repeatedly by the prototype (resolve these once as semantic tokens)

| Purpose | Expression | Resolved |
|---|---|---|
| Divider / control border | `text @ 40%` | `rgb(32 30 29 / .40)` |
| Muted body text | `text @ 55%` | `rgb(32 30 29 / .55)` |
| Field label | `text @ 70%` | `rgb(32 30 29 / .70)` |
| Row hover (table) | `text @ 4%` | `rgb(32 30 29 / .04)` |
| Row hover (list/volume) | `text @ 5%` | `rgb(32 30 29 / .05)` |
| Sidebar item hover | `text @ 6%` | `rgb(32 30 29 / .06)` |
| Secondary btn hover / palette item hover | `text @ 7%` | `rgb(32 30 29 / .07)` |
| Secondary btn pressed | `text @ 14%` | `rgb(32 30 29 / .14)` |
| Grid-card hover scrim | `text @ 72%` | `rgb(32 30 29 / .72)` |
| Viewer volume-end scrim | `text @ 92%` | `rgb(32 30 29 / .92)` |
| Selected nav / palette row fill | `accent @ 8%` | `rgb(236 48 19 / .08)` |
| Ghost btn hover / pressed | `accent @ 10%` / `@ 18%` | — |
| Dialog backdrop | `neutral-900 @ 50%` | `rgb(45 43 43 / .50)` |
| Broken-volume scrim | `accent-900 @ 82%` | `rgb(77 23 14 / .82)` |
| `::selection` | `accent @ 30%` | — |
| Disabled control | `opacity: .45` | — |

### 1.2 Theme file (`src/styles/tokens.css`)

Ship the tokens as CSS custom properties in one global file. **Do not hardcode any hex in a component.**

```css
/* src/styles/tokens.css — imported once from main.tsx, before Tailwind's layers */

:root,
:root[data-theme="light"] {
  color-scheme: light;

  /* roles */
  --color-bg: #f3f2f2;
  --color-surface: #eae9e9;
  --color-text: #201e1d;
  --color-accent: #ec3013;
  --color-accent-2: #e15b47;
  --color-divider: rgb(32 30 29 / 0.40);

  /* semantic, theme-flipping (see §1.4) */
  --ink:            var(--color-text);
  --ink-muted:      rgb(32 30 29 / 0.55);
  --ink-dim:        #7d7979;   /* neutral-600 */
  --ink-faint:      #9b9797;   /* neutral-500 */
  --rule:           var(--color-divider);      /* 1px hairlines  */
  --rule-strong:    var(--color-divider);      /* 2px section rules */
  --control-border: var(--color-divider);
  --fill-subtle:    #eae7e7;   /* neutral-200 */
  --fill-track:     #d7d3d3;   /* neutral-300 — progress-bar troughs */
  --fill-track-2:   #bab6b6;   /* neutral-400 — troughs on top of imagery */
  --hover-tint:     rgb(32 30 29 / 0.07);
  --press-tint:     rgb(32 30 29 / 0.14);
  --row-hover:      rgb(32 30 29 / 0.05);
  --nav-active:     rgb(236 48 19 / 0.08);
  --scrim-cover:    rgb(32 30 29 / 0.72);
  --scrim-modal:    rgb(45 43 43 / 0.50);
  --accent-hover:   #dd2b0f;   /* accent-600 — one step past base on a LIGHT ground */
  --accent-press:   #ae1800;   /* accent-700 */
  --accent-text:    #ae1800;   /* accent-700 — accent at paragraph size (see §2.5) */

  /* ramps — constant across themes */
  --color-neutral-100:#f8f4f4; --color-neutral-200:#eae7e7; --color-neutral-300:#d7d3d3;
  --color-neutral-400:#bab6b6; --color-neutral-500:#9b9797; --color-neutral-600:#7d7979;
  --color-neutral-700:#605d5d; --color-neutral-800:#444141; --color-neutral-900:#2d2b2b;
  --color-accent-100:#fff2ef; --color-accent-200:#ffe0d9; --color-accent-300:#ffc4b8;
  --color-accent-400:#ff9783; --color-accent-500:#ff563c; --color-accent-600:#dd2b0f;
  --color-accent-700:#ae1800; --color-accent-800:#7c1405; --color-accent-900:#4d170e;
  --color-accent-2-100:#fff2ef; --color-accent-2-200:#ffe0da; --color-accent-2-300:#ffc4b9;
  --color-accent-2-400:#ff9784; --color-accent-2-500:#ef6853; --color-accent-2-600:#c94b39;
  --color-accent-2-700:#9e3526; --color-accent-2-800:#71261b; --color-accent-2-900:#471d16;

  /* type */
  --font-heading: "Archivo", "Pretendard Variable", Pretendard, "Apple SD Gothic Neo",
                  "Noto Sans KR", system-ui, sans-serif;
  --font-heading-weight: 800;
  --font-body: var(--font-heading);

  /* space + radius */
  --space-1:4px; --space-2:8px; --space-3:12px; --space-4:16px; --space-6:24px; --space-8:32px;
  --radius-sm:0px; --radius-md:0px; --radius-lg:0px;

  /* elevation — soft ink-tinted shadows on a light ground */
  --shadow-sm: 0 1px 2px  rgb(45 43 43 / 0.14);
  --shadow-md: 0 3px 10px rgb(45 43 43 / 0.16);
  --shadow-lg: 0 12px 32px rgb(45 43 43 / 0.22);
}
```

### 1.3 Tailwind mapping

`tailwind.config.ts` — map the CSS variables through `theme.extend` so `bg-surface`, `text-accent`,
`border-divider`, `p-4`, `gap-6` all resolve to the tokens, and so a theme swap on `:root[data-theme]`
re-themes every utility with no rebuild.

```ts
import type { Config } from 'tailwindcss';

export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  darkMode: ['class', ':root[data-theme="dark"]'],
  theme: {
    // radius is zero by design — kill the defaults so `rounded-*` cannot creep in
    borderRadius: { none: '0px', DEFAULT: '0px', full: '9999px' },
    extend: {
      colors: {
        bg:      'var(--color-bg)',
        surface: 'var(--color-surface)',
        ink:     'var(--color-text)',
        accent: {
          DEFAULT: 'var(--color-accent)',
          100:'var(--color-accent-100)', 200:'var(--color-accent-200)', 300:'var(--color-accent-300)',
          400:'var(--color-accent-400)', 500:'var(--color-accent-500)', 600:'var(--color-accent-600)',
          700:'var(--color-accent-700)', 800:'var(--color-accent-800)', 900:'var(--color-accent-900)',
        },
        neutral: {
          100:'var(--color-neutral-100)', 200:'var(--color-neutral-200)', 300:'var(--color-neutral-300)',
          400:'var(--color-neutral-400)', 500:'var(--color-neutral-500)', 600:'var(--color-neutral-600)',
          700:'var(--color-neutral-700)', 800:'var(--color-neutral-800)', 900:'var(--color-neutral-900)',
        },
        divider:  'var(--color-divider)',
        // semantic, theme-flipping
        'ink-muted':'var(--ink-muted)', 'ink-dim':'var(--ink-dim)', 'ink-faint':'var(--ink-faint)',
        'fill-subtle':'var(--fill-subtle)', 'fill-track':'var(--fill-track)',
        'fill-track-2':'var(--fill-track-2)', 'nav-active':'var(--nav-active)',
        'accent-hover':'var(--accent-hover)', 'accent-press':'var(--accent-press)',
        'accent-text':'var(--accent-text)',
      },
      borderColor: { DEFAULT: 'var(--color-divider)' },
      spacing: {
        1:'var(--space-1)', 2:'var(--space-2)', 3:'var(--space-3)',
        4:'var(--space-4)', 6:'var(--space-6)', 8:'var(--space-8)',
      },
      fontFamily: { sans: 'var(--font-body)', heading: 'var(--font-heading)' },
      fontSize: {
        // the interface sizes the prototype actually uses
        '2xs':['9px',{lineHeight:'1.2'}],   '3xs':['10px',{lineHeight:'1.2'}],
        xs:['11px',{lineHeight:'1.35'}],    sm:['12px',{lineHeight:'1.35'}],
        base:['13px',{lineHeight:'1.45'}],  md:['14px',{lineHeight:'1.45'}],
        lg:['15px',{lineHeight:'1.55'}],    h6:['13px',{lineHeight:'1.12'}],
        h5:['16px',{lineHeight:'1.12'}],    h4:['20px',{lineHeight:'1.12'}],
        h3:['25px',{lineHeight:'1.12'}],    h2:['32px',{lineHeight:'1.12'}],
        h1:['42px',{lineHeight:'1.12'}],
      },
      boxShadow: { sm:'var(--shadow-sm)', md:'var(--shadow-md)', lg:'var(--shadow-lg)' },
      keyframes: {
        shimmer:{ '0%':{opacity:'.3'},'50%':{opacity:'.7'},'100%':{opacity:'.3'} },
        spin:{ to:{ transform:'rotate(360deg)' } },
      },
      animation: { shimmer:'shimmer 1.6s ease-in-out infinite', spin:'spin .7s linear infinite' },
    },
  },
} satisfies Config;
```

**Two rules that come with this mapping.**

- Because the color values are opaque hexes / pre-composed `rgb(… / α)` strings and *not*
  `<alpha-value>`-aware channel triples, **Tailwind opacity modifiers (`bg-ink/50`) will not work on
  themed colors.** That is intentional and matches the DS: *"prefer ramp steps over ad-hoc `color-mix()`."*
  Every tint the design actually needs is already a named semantic token in §1.2. If you need a new one,
  add it to `tokens.css` — do not invent `/40` at the call site.
- `borderRadius` is overridden (not extended) so `rounded-lg` etc. simply do not exist. `rounded-full`
  survives for the radio dot and the spinner.

### 1.4 Dark theme (required by NFR-CMP-003)

The Modernist DS ships a light ground only. The prototype's **viewer** already demonstrates the intended
dark treatment, and the dark ramp below is derived from it so the two are literally the same surface.
The rule is: **swap `--color-bg` and `--color-text`, take structure from the top of the neutral ramp
instead of the bottom, keep the accent constant, and move the hover/press step to `accent-400`** (exactly
what the DS readme prescribes for a dark ground).

```css
:root[data-theme="dark"] {
  color-scheme: dark;

  --color-bg: #201e1d;        /* was --color-text  */
  --color-surface: #2d2b2b;   /* neutral-900       */
  --color-text: #f3f2f2;      /* was --color-bg    */
  --color-accent: #ec3013;    /* brand constant — 3.95:1 on #201e1d, OK for chrome + large text */
  --color-accent-2: #e15b47;
  --color-divider: #444141;   /* neutral-800, SOLID — exactly what the viewer chrome uses */

  --ink:            #f3f2f2;
  --ink-muted:      rgb(243 242 242 / 0.62);
  --ink-dim:        #9b9797;  /* neutral-500 reads as "dim" on a dark ground */
  --ink-faint:      #7d7979;  /* neutral-600 */
  --rule:           #444141;  /* neutral-800 */
  --rule-strong:    #444141;
  --control-border: #605d5d;  /* neutral-700 — the viewer's button/seg borders */
  --fill-subtle:    #2d2b2b;  /* neutral-900 */
  --fill-track:     #444141;  /* neutral-800 */
  --fill-track-2:   #605d5d;  /* neutral-700 */
  --hover-tint:     rgb(243 242 242 / 0.08);
  --press-tint:     rgb(243 242 242 / 0.16);
  --row-hover:      rgb(243 242 242 / 0.06);
  --nav-active:     rgb(236 48 19 / 0.14);   /* 8% is invisible on dark — bumped to 14% */
  --scrim-cover:    rgb(0 0 0 / 0.72);
  --scrim-modal:    rgb(0 0 0 / 0.60);
  --accent-hover:   #ff563c;  /* accent-500 */
  --accent-press:   #ff9783;  /* accent-400 — "one step past base on a dark ground" */
  --accent-text:    #ff9783;  /* accent-400 — accent at paragraph size on dark */

  /* elevation on a dark ground = hairline edge + ambient darkness (per the DS readme) */
  --shadow-sm: 0 0 0 1px #444141, 0 1px 2px  rgb(0 0 0 / 0.50);
  --shadow-md: 0 0 0 1px #444141, 0 3px 10px rgb(0 0 0 / 0.55);
  --shadow-lg: 0 0 0 1px #444141, 0 12px 32px rgb(0 0 0 / 0.60);
}
```

**Semantics that must be preserved when the theme flips.** The raw ramps (`--color-neutral-*`,
`--color-accent-*`) do **not** change — they are an absolute lightness scale. What changes is which end of
the ramp each *role* points at. So: never write `bg-neutral-200` for "a subtle fill"; write
`bg-fill-subtle`. Never write `text-accent-700` for "accent-coloured text"; write `text-accent-text`.
The prototype's raw-ramp references map onto semantic tokens like this:

| Prototype (light) | Semantic token | Dark resolves to |
|---|---|---|
| `neutral-200` / `neutral-300` cover stripes | `--fill-subtle` / `--fill-track` | `#2d2b2b` / `#444141` |
| `neutral-300` progress trough | `--fill-track` | `#444141` |
| `neutral-400` progress trough over art | `--fill-track-2` | `#605d5d` |
| `neutral-500` disabled/idle numerals | `--ink-faint` | `#7d7979` |
| `neutral-600` labels, counts | `--ink-dim` | `#9b9797` |
| `neutral-700` secondary body text | `--ink-muted` | `rgb(243 242 242 / .62)` |
| `neutral-800` strong body text | `--ink` | `#f3f2f2` |
| `accent-700` accent text | `--accent-text` | `#ff9783` |

**Theme scope.** `data-theme` on `<html>` follows the user setting (light / dark / system, §5.4). The viewer
root **always** renders in the dark palette regardless — implement it as
`<div data-theme="dark" className="viewer-root">` so it re-scopes the same tokens rather than hardcoding
colors. That is what makes the light-theme viewer and the dark-theme viewer identical, which is what
`--theme: 다크 (뷰어 고정)` in the prototype's settings panel is telling you.

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

### 2.3 Component class contracts (verbatim from the DS)

Rebuild these as React components, but keep the *exact* geometry.

| Class | Contract |
|---|---|
| `.btn` | `inline-flex; align-items:center; justify-content:center; gap:6px; font-family:var(--font-heading); font-weight:800; font-size:14px; line-height:1.2; color:var(--color-text); background:transparent; border:1px solid transparent; padding: var(--space-2) calc(var(--space-3) * 1.2)` → **8px 14.4px**; `border-radius:0`. `.btn svg{display:block}`. `:disabled{opacity:.45;cursor:not-allowed}` |
| `.btn-primary` | `background:var(--color-accent); color:var(--color-bg)`; hover `--accent-hover`; active `--accent-press` |
| `.btn-secondary` | `border-color:var(--color-divider)`; hover `--hover-tint`; active `--press-tint` |
| `.btn-ghost` | `color:var(--color-accent); padding-inline:var(--space-1)`; hover accent@10%; active accent@18% |
| `.btn-icon` | `width:36px; height:36px; padding:0` |
| `.btn-block` | `width:100%; margin-top:var(--space-2); justify-content:flex-start; text-align:left` ← **the flush-left rule** |
| `.tag` | `inline-flex; align-items:center; font-size:11px; letter-spacing:.02em; padding:3px 10px; border-radius:0` |
| `.tag-accent` | `bg accent-100 / fg accent-800` |
| `.tag-accent-2` | `bg accent-2-100 / fg accent-2-800` |
| `.tag-neutral` | `bg neutral-100 / fg neutral-800` |
| `.tag-outline` | `border:1px solid var(--color-accent); color:var(--color-accent)` |
| `.field > label` | `display:block; font-size:12px; margin-bottom:5px; color:text@70%` |
| `.input` | `width:100%; min-height:36px; padding:6px 10px; font-size:14px; color:var(--color-text); caret-color:var(--color-accent); background:var(--color-surface); border:1px solid var(--color-divider); border-radius:0`. hover `border-color: text@45%`. `:focus-visible{border-color:var(--color-accent); outline-offset:0}` |
| `.radio` | `inline-flex; align-items:center; gap:8px; font-size:14px`. `.dot` = `16×16; border-radius:50%; border:1.5px solid var(--color-divider)`. checked → `border+bg accent; box-shadow: inset 0 0 0 4px var(--color-bg)` |
| `.seg` | `inline-flex; overflow:hidden; border:1px solid var(--color-divider); border-radius:0` |
| `.seg-opt` | `inline-flex; align-items:center; gap:6px; padding:7px 12px; font-size:13px; cursor:pointer`. `+ .seg-opt { border-left:1px solid var(--color-divider) }`. **checked → `background:var(--color-accent); color:var(--color-bg)`**. unchecked hover → `--hover-tint`. focus-visible → `outline:2px solid accent; outline-offset:-2px` |
| `.card` | `flex column; gap:var(--space-2); padding:var(--space-3); background:var(--color-surface); border-radius:0` |
| `.card-kicker` | `10px; ls .1em; uppercase; color:var(--color-accent)` |
| `.card-title` | Archivo 800 17px / 1.2 |
| `.card-body` | `13px; opacity:.8; flex:1` |
| `.card-meta` | `flex; gap:6px; 11px; color: text@50%` |
| `.elev-sm/md/lg` | `box-shadow: var(--shadow-sm/md/lg)` |
| `.nav` | `flex; align-items:center; gap:var(--space-4); padding:var(--space-3) var(--space-4); border-bottom:2px solid var(--color-divider)` |
| `.nav-brand` | Archivo 800 18px; `margin-right:auto` |
| `.table` | `width:100%; border-collapse:collapse; font-size:14px`. `th` → `text-align:left; 11px; ls .08em; uppercase; color:text@60%; padding:var(--space-2); border-bottom:2px solid divider`. `td` → `padding:var(--space-2); border-bottom:1px solid divider`. `tbody tr:hover` → `text@4%` |
| `.dialog-backdrop` | `position:fixed; inset:0; display:grid; place-items:center; padding:var(--space-4); background: neutral-900@50%` |
| `.dialog` | `width:min(440px,100%); flex column; gap:var(--space-3); padding:var(--space-4); background:var(--color-surface); box-shadow:var(--shadow-lg); border-radius:0` |
| `.dialog-title` / `-body` / `-actions` | Archivo 800 20px / `14px opacity .85` / `flex; justify-content:flex-end; gap:var(--space-2); margin-top:var(--space-2)` |
| `.hr` | `height:2px; border:0; margin:var(--space-4) 0; background:var(--color-divider)` |
| `.grayscale` | `filter: grayscale(1) contrast(1.08)` |

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

`padding:16px; border-bottom:2px solid rule`

- Header: `display:flex; align-items:baseline; gap:8px; margin-bottom:12px`.
  `<h6>이어보기</h6>` + count `font-size:11px; tabular-nums; color:var(--ink-dim)` (e.g. `5개`).
- Track: `display:flex; gap:12px; overflow-x:auto; padding-bottom:4px`. Max 5 cards.
- Card: `flex:0 0 300px; display:flex; gap:12px; padding:12px; background:var(--color-surface);
  cursor:pointer; border:1px solid var(--rule)`; hover → `border-color:var(--color-accent)`.
  Click → resume that book at its saved page.
  - Thumb `flex:0 0 66px; height:99px` (2:3); `overflow:hidden; background:var(--fill-track)`.
  - Body `flex:1; min-width:0; display:flex; flex-direction:column; gap:5px`:
    - Title — Archivo 800 13px / 1.2, `-webkit-line-clamp:2`.
    - Volume name — 11px `color:var(--ink-muted)`, ellipsis nowrap.
    - Spacer `flex:1`.
    - Page counter — 12px tabular, `color:var(--accent-text)`, format `` `${page} / ${total}p` ``.
    - Bar — `height:3px; background:var(--fill-track)`, fill `var(--color-accent)`.

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
| Hover overlay | `position:absolute; inset:0; opacity:0; transition:opacity .12s; display:flex; flex-direction:column; justify-content:flex-end; gap:4px; padding:8px; background:var(--scrim-cover)`. On hover **and on keyboard focus-within** → `opacity:1`. Contains `.btn.btn-primary.btn-block` (`margin:0; font-size:12px`) labelled `이어 읽기` when in progress else `읽기 시작`, and a second `.btn.btn-block` with `background:var(--color-bg); color:var(--color-text); margin:0; font-size:12px` labelled `상세` |
| Progress bar | Only when `0 < progress < 1`. `position:absolute; left:0; right:0; bottom:0; height:4px; background:var(--fill-track-2)`, fill `width:{pct}%; background:var(--color-accent)` |

**Below the cover** — title `font-size:12px; line-height:1.3; -webkit-line-clamp:2`; meta row
`display:flex; gap:8px; font-size:11px; tabular-nums; color:var(--ink-dim)` → `22권` `4.4 GB`.

Hover state: [`library-grid-card-hover-1440.png`](./ui-shots/library-grid-card-hover-1440.png)

![Grid card hover overlay](./ui-shots/library-grid-card-hover-1440.png)

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

**Reading-direction default** (FR-VWR-002): `dirs[seriesId] ?? (root is a manga root ? 'rtl' : 'ltr')`.
Persist per series. The prototype defaults root #1 (`01. mangga`) to RTL — see the screenshot, `R→L` is the
lit segment.

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

`position:fixed; inset:0; z-index:60; background:var(--color-text); color:var(--color-bg);
display:flex; flex-direction:column; overflow:hidden; cursor: {chrome ? 'default' : 'none'}`

Render it inside `<div data-theme="dark">` (§1.4) so every token inside resolves to the dark ramp
automatically, in both app themes.

### 6.1 Base state — chromeless

Reference: [`viewer-chromeless-base-1440.png`](./ui-shots/viewer-chromeless-base-1440.png)

![Viewer, chromeless base state](./ui-shots/viewer-chromeless-base-1440.png)

design.md principle 2: **while reading, there is no UI.** Nothing but the page on a near-black ground.
The cursor is hidden (`cursor:none`). Chrome auto-hides **2200 ms** after the last wake.

### 6.2 Stage

`flex:1; min-height:0; display:flex; align-items:center; justify-content:center`

| Property | single / double | vertical (webtoon) |
|---|---|---|
| `overflow` | `hidden` | `auto` |
| `padding` | `20px` (`0` when fit = 원본) | `0` |
| flow `flex-direction` | `row` (LTR) / **`row-reverse` (RTL)** | `column` |
| flow `gap` | `2px` | `12px` |

Page frame: `position:relative; flex:0 0 auto` plus the fit rule:

| Fit mode | Label | Sizing |
|---|---|---|
| `width` | 너비 | `width:100%; height:auto` |
| `height` | 높이 | `height:100%; width:auto` — **default** |
| `original` | 원본 | intrinsic size, stage padding drops to 0, stage scrolls |
| `screen` | 화면 | `max-width:100%; max-height:100%` (contain) |

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

Shown when `page >= totalPages` **and** mode ≠ vertical.
Scrim `position:absolute; inset:0; background: rgb(32 30 29 / .92); display:flex; align-items:center;
justify-content:center; padding:16px`.
Card `width:380px; max-width:100%; background:var(--color-bg); color:var(--color-text); padding:16px;
display:flex; flex-direction:column; gap:12px; box-shadow:var(--shadow-lg)`.

> Note this card deliberately flips back to the **light** palette — it is a surface floating above the
> reading ground, and the contrast is the point. Wrap it in `data-theme="light"` when the app theme is light,
> or keep it token-driven off the app theme rather than the viewer theme.

Contents: kicker `10px; ls .12em; uppercase; color:var(--color-accent)` → `권의 마지막 페이지` ·
title Archivo 800 20px / 1.15 (next volume name) · meta `12px; tabular; color:var(--ink-muted)` →
`214p · FOLDER` · `<hr class="hr" style="margin:0">` ·
actions `display:flex; gap:8px` → `.btn.btn-primary` `flex:1; justify-content:flex-start` `다음 권 읽기`
+ `.btn.btn-secondary` `시리즈로`.

> **Amended by E-28.** The scrim covers the stage but **not the chrome**: both bars carry `z-chrome` (3) and
> the scrim none, so 뒤로, the slider, the display controls and the strip stay live at the end of a volume.
> And 다음 권 읽기 is a *continuation* — it changes the book and the page and leaves the chrome, the strip and
> the already-answered opening hint exactly as the reader left them.

### 6.6 Top overlay

Reference: [`viewer-overlay-visible-1440.png`](./ui-shots/viewer-overlay-visible-1440.png)

![Viewer with overlays visible](./ui-shots/viewer-overlay-visible-1440.png)

`position:absolute; top:0; left:0; right:0; opacity:{chrome ? 1 : 0}; pointer-events:{chrome ? auto : none};
transition: opacity .18s; background:var(--color-text); border-bottom:2px solid var(--color-neutral-800);
padding:8px 16px; display:flex; align-items:center; gap:12px`

Left → right:

1. `.btn.btn-secondary` `← 뒤로` — `color:var(--color-bg); border-color:var(--color-neutral-700); font-size:12px`
2. Title block `min-width:0; display:flex; flex-direction:column`:
   series title Archivo 800 13px, ellipsis nowrap; volume name 11px `color:var(--color-neutral-500)`, ellipsis nowrap
3. Spacer `flex:1`
4. Three `.seg` groups, each `color:var(--color-bg); border-color:var(--color-neutral-700)`, with each
   `.seg-opt + .seg-opt` overriding `border-left-color: var(--color-neutral-700)`:

| Group | Options |
|---|---|
| Display mode | `단면` / `양면` / `세로` |
| Reading direction | `L→R` / `R→L` |
| Fit | `너비` / `높이` / `원본` / `화면` |

**Opacity, not `display:none`.** The bars stay mounted and fade — this is what makes the 180 ms wake feel
instant and avoids reflow on every mouse move.

### 6.7 Bottom overlay

Same fade/`pointer-events` contract. `position:absolute; bottom:0; left:0; right:0;
background:var(--color-text); border-top:2px solid var(--color-neutral-800)`.

**Thumbnail strip** (when open) sits *above* the control row inside the same bar:
`display:flex; gap:4px; overflow-x:auto; padding:12px 16px; border-bottom:1px solid var(--color-neutral-800)`.
Each thumb: `flex:0 0 auto; width:48px; height:72px` — **56×84 below 768** (E-28), and the strip's slot
pitch and track height follow it (60/52 and 84/72) or the cells overlap and clip — `border:2px solid X;
display:flex; align-items:flex-end; justify-content:flex-start; padding:3px; font-size:10px; tabular;
cursor:pointer`. Current page → `X = var(--color-accent)`, number `color:var(--color-bg)`;
otherwise `X = var(--color-neutral-800)`, number `color:var(--color-neutral-600)`.
Auto-scroll the current thumb into view on page change.

> The prototype renders at most 60 thumbs. **Virtualise the strip** — books run to 500+ pages (AC-008)
> and thumbnails are lazily generated server-side (FR-THM-004).

Reference: [`viewer-thumbnail-strip-1440.png`](./ui-shots/viewer-thumbnail-strip-1440.png)

![Viewer thumbnail strip](./ui-shots/viewer-thumbnail-strip-1440.png)

**Control row** — `padding:8px 16px 12px; display:flex; flex-wrap:wrap; align-items:center; gap:16px`
(`flex-wrap` per **E-28**: below ~520px the slider takes its own row rather than being crushed):

1. Counter `font-size:13px; tabular; min-width:84px; letter-spacing:.04em` → `12 / 214`
2. Slider wrapper `flex:1; position:relative`
   - `<input type="range" min=1 max={totalPages} value={page}>`, styled per §2.4.
     **E-28**: the element is `height:24px` (44px below 768), the thumb 12×18 (16×28 below 768), and in the
     viewer it carries `.on-dark` — the track lifts to `--color-neutral-600`, because `--color-divider` on
     the reading ground is all but the background colour
   - **Drag preview** (while dragging): `position:absolute; bottom:24px; left:{pct}%;
     transform:translateX(-50%); width:68px; height:102px; border:2px solid var(--color-accent);
     display:flex; align-items:flex-end; padding:4px; font-size:11px; tabular; color:var(--color-bg)`,
     showing the thumbnail for the hovered page.
     See [`viewer-slider-drag-preview-1440.png`](./ui-shots/viewer-slider-drag-preview-1440.png)
   - `left` = `(page - 1) / max(totalPages - 1, 1) * 100`
3. `.btn.btn-secondary` `썸네일 · T` — `border-color:var(--color-neutral-700); font-size:12px`;
   `color: var(--color-accent-400)` when the panel is open, else `var(--color-bg)`

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
| **≥1440** | Fixed, 240px | `--grid-min: 152px` → **6 cols @1440, 8 @1760** | All 7 columns | 300px cards, horizontal scroll | Full chrome, all 3 `.seg` groups inline |
| **1024–1439** | Fixed, 240px | `--grid-min: 150px` → **4–5 cols** | All 7 columns | 300px cards | Full chrome ([`viewer-overlay-1024.png`](./ui-shots/viewer-overlay-1024.png)) |
| **768–1023** | **Collapsed** to a 56px icon rail; the scope name moves into the section header. Full sidebar opens as an overlay drawer from a hamburger in the top bar | `--grid-min: 224px` → **3 cols** | **Drop 수정일 + 용량** → `32px minmax(0,1fr) 66px 64px 120px`. Format tag stays (it is primary metadata) | 260px cards | **All 3 `.seg` groups stay inline**; the bar wraps to a second row instead (**E-28**). ~103px at 900 ([`viewer-overlay-768.png`](./ui-shots/viewer-overlay-768.png) predates the ruling and shows the old overflow menu) |
| **<768** | **Off-canvas drawer** (`position:fixed; inset:0 auto 0 0; width:280px`) over a `--scrim-modal` backdrop. Closed by default | `--grid-min: 150px; gap: 12px` → **2 cols** | **Two-line row**: line 1 = title; line 2 = tag · 권 · 용량 · progress at 11px. Grid becomes `32px minmax(0,1fr)` | Full-width cards, one per screen, snap scroll | Touch-first (§8.3). **All 3 `.seg` groups stay inline and the top bar wraps to three rows** — ~151px at 500 (**E-28**, which deleted the `⋯` bottom sheet this row used to require). The bottom bar's control row wraps too, and the page slider grows to a 44px box |

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
| `→` | `step(dir === 'rtl' ? −1 : +1)` |
| `←` | `step(dir === 'rtl' ? +1 : −1)` |
| `Space` | `step(+1)`, `preventDefault()` |
| `T` | Toggle the thumbnail panel |
| `F` | Toggle browser fullscreen (`requestFullscreen` / `exitFullscreen`) — **the prototype stubs this to a chrome wake; implement it for real** |
| `Esc` | Exit the viewer |
| `1` / `2` / `3` | Display mode 단면 / 양면 / 세로 |

`step(d)`: increment is **2 in 양면 mode**, 1 otherwise. Clamp to `[1, totalPages]`; landing on `totalPages`
raises the next-volume card (§6.5). Every step sets `loading` and calls `wake()`.

`wake()`: show chrome, then hide it again after **2200 ms** of no pointer movement and no key press.
Bound to the viewer's `mousemove`.

### 8.3 Pointer and touch

| Zone / gesture | Action |
|---|---|
| Mouse move anywhere in the viewer | Nothing — **E-27** took the chrome off the mouse; only the cursor comes back |
| **Left 32%** of the stage — tap/click | Previous page in reading order (i.e. *next* page when RTL). `cursor:pointer` while the pointer is awake (E-28) |
| **Right 32%** of the stage — tap/click | Next page in reading order |
| **Centre 36%** — tap/click | Toggle chrome (FR-VWR-011, design.md 화면 3 모바일). **E-28** narrowed the centre from 40%: the two zones a reader aims at a hundred times a volume are the page turns |
| Horizontal swipe | Page turn, direction-aware; disabled in 세로 mode. **E-28**: ≥44px, `|dy| ≤ |dx|`, and thrown within 600ms |
| Vertical drag in 세로 mode | Native scroll |
| Slider `mousedown`/`touchstart` | `dragging = true` → show the drag preview |
| Slider `mouseup`/`touchend` | `dragging = false`, commit the page |
| Grid card hover | Reveal the action overlay. Mirror it on `:focus-within` so keyboard users get the same actions |

### 8.4 Command palette

Reference: [`command-palette-recent-1440.png`](./ui-shots/command-palette-recent-1440.png)

![Command palette](./ui-shots/command-palette-recent-1440.png)

`.dialog-backdrop` with `z-index:80; place-items:start center; padding-top:12vh`.
`.dialog` `width:min(620px,100%); gap:0; padding:0`.

- **Query input** — `.input` with `border:0; border-bottom:2px solid var(--rule-strong); min-height:52px;
  font-size:17px; background:transparent`; placeholder `시리즈로 이동…`. Autofocus on open.
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
`Esc` 뷰어 나가기 · `⌘K` 커맨드 팔레트 · `1 2 3` 단면 / 양면 / 세로 · `?` 키보드 단축키.

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

---

## 9. Component inventory

One entry per component named in design.md's inventory, plus the shell pieces. Props are indicative
TypeScript, not final API.

### Shared domain types

```ts
type Format = 'zip' | 'folder' | 'pdf';
type ReadDirection = 'ltr' | 'rtl';
type DisplayMode = 'single' | 'double' | 'vertical';
type FitMode = 'width' | 'height' | 'original' | 'screen';
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
| 11 | `ScanIndicator` (스캔 인디케이터) | `{ scanning: boolean; pct: number; label: string; onOpenLog() }` | idle (grey dot) · scanning (accent dot + 96px bar in the top bar) | [`library-scanning-progress-1440`](./ui-shots/library-scanning-progress-1440.png) |
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
| `single / double / vertical` | 단면 / 양면 / 세로 | `fitW / fitH / fitO / fitS` | 너비 / 높이 / 원본 / 화면 |
| `thumbs` | 썸네일 | `loading` | 페이지 로딩 |
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
