import type { Config } from 'tailwindcss'

/**
 * The ui-spec §1.3 mapping.
 *
 * Two rules travel with it and are the whole point of the file:
 *
 *  1. `borderRadius` is **overridden, not extended** (decisions.md D-40). After
 *     this config `rounded-sm|md|lg|xl|2xl|3xl` do not exist as utilities at
 *     all — the class silently doing nothing is not good enough, the build has
 *     to be unable to produce it. `rounded-full` survives for the radio dot and
 *     the viewer spinner, the only two circles in the product.
 *
 *  2. Every colour resolves to a CSS custom property rather than a literal, so
 *     flipping `data-theme` re-themes every utility with no rebuild (ui-spec
 *     §1.4). The consequence is that Tailwind's opacity modifiers (`bg-ink/50`)
 *     do **not** work on themed colours, because the values are opaque hexes
 *     and pre-composed `rgb(… / α)` strings rather than `<alpha-value>` channel
 *     triples. That is intentional and matches the DS: every tint the design
 *     needs is already a named semantic token in tokens.css. Add one there
 *     rather than inventing `/40` at a call site.
 */
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],

  // The dark ramp is scoped by a bare attribute selector, not `:root[…]`, so a
  // nested `<div data-theme="dark">` (the viewer) re-scopes the tokens inside
  // it in both app themes. See tokens.css.
  darkMode: ['selector', '[data-theme="dark"]'],

  theme: {
    // Zero corner radius, everywhere. Overriding rather than extending is what
    // makes `rounded-lg` un-writable.
    borderRadius: { none: '0px', DEFAULT: '0px', full: '9999px' },

    extend: {
      // The four widths the responsive layer of ui-spec §7 is specified
      // against. `md` and `lg` match Tailwind's defaults; `xl` is moved from
      // 1280 to 1440 so the desktop tier is the one the design describes.
      screens: {
        md: '768px',
        lg: '1024px',
        xl: '1440px',
      },

      colors: {
        bg: 'var(--color-bg)',
        surface: 'var(--color-surface)',
        ink: 'var(--color-text)',
        accent: {
          DEFAULT: 'var(--color-accent)',
          100: 'var(--color-accent-100)',
          200: 'var(--color-accent-200)',
          300: 'var(--color-accent-300)',
          400: 'var(--color-accent-400)',
          500: 'var(--color-accent-500)',
          600: 'var(--color-accent-600)',
          700: 'var(--color-accent-700)',
          800: 'var(--color-accent-800)',
          900: 'var(--color-accent-900)',
        },
        'accent-2': {
          DEFAULT: 'var(--color-accent-2)',
          100: 'var(--color-accent-2-100)',
          200: 'var(--color-accent-2-200)',
          300: 'var(--color-accent-2-300)',
          400: 'var(--color-accent-2-400)',
          500: 'var(--color-accent-2-500)',
          600: 'var(--color-accent-2-600)',
          700: 'var(--color-accent-2-700)',
          800: 'var(--color-accent-2-800)',
          900: 'var(--color-accent-2-900)',
        },
        neutral: {
          100: 'var(--color-neutral-100)',
          200: 'var(--color-neutral-200)',
          300: 'var(--color-neutral-300)',
          400: 'var(--color-neutral-400)',
          500: 'var(--color-neutral-500)',
          600: 'var(--color-neutral-600)',
          700: 'var(--color-neutral-700)',
          800: 'var(--color-neutral-800)',
          900: 'var(--color-neutral-900)',
        },
        divider: 'var(--color-divider)',

        // Semantic, theme-flipping. Prefer these over raw ramp steps: the ramps
        // are an absolute lightness scale and do not change when the theme does
        // (ui-spec §1.4).
        'ink-muted': 'var(--ink-muted)',
        'ink-dim': 'var(--ink-dim)',
        'ink-faint': 'var(--ink-faint)',
        'ink-label': 'var(--ink-label)',
        'ink-meta': 'var(--ink-meta)',
        'ink-th': 'var(--ink-th)',
        rule: 'var(--rule)',
        'rule-strong': 'var(--rule-strong)',
        'control-border': 'var(--control-border)',
        'fill-subtle': 'var(--fill-subtle)',
        'fill-track': 'var(--fill-track)',
        'fill-track-2': 'var(--fill-track-2)',
        'hover-tint': 'var(--hover-tint)',
        'press-tint': 'var(--press-tint)',
        'row-hover': 'var(--row-hover)',
        'row-hover-table': 'var(--row-hover-table)',
        'nav-hover': 'var(--nav-hover)',
        'nav-active': 'var(--nav-active)',
        'scrim-cover': 'var(--scrim-cover)',
        'scrim-modal': 'var(--scrim-modal)',
        'scrim-volume-end': 'var(--scrim-volume-end)',
        'scrim-broken': 'var(--scrim-broken)',
        'accent-hover': 'var(--accent-hover)',
        'accent-press': 'var(--accent-press)',
        'accent-text': 'var(--accent-text)',
      },

      borderColor: { DEFAULT: 'var(--color-divider)' },

      // Keys 1,2,3,4,6,8 — the DS has no 5 or 7. Tailwind's own numeric scale
      // survives for everything else because this is an `extend`.
      spacing: {
        1: 'var(--space-1)',
        2: 'var(--space-2)',
        3: 'var(--space-3)',
        4: 'var(--space-4)',
        6: 'var(--space-6)',
        8: 'var(--space-8)',
      },

      fontFamily: {
        sans: 'var(--font-body)',
        heading: 'var(--font-heading)',
      },

      fontSize: {
        // The interface sizes the prototype actually uses.
        '2xs': ['9px', { lineHeight: '1.2' }],
        '3xs': ['10px', { lineHeight: '1.2' }],
        xs: ['11px', { lineHeight: '1.35' }],
        sm: ['12px', { lineHeight: '1.35' }],
        base: ['13px', { lineHeight: '1.45' }],
        md: ['14px', { lineHeight: '1.45' }],
        lg: ['15px', { lineHeight: '1.55' }],
        h6: ['13px', { lineHeight: '1.12' }],
        h5: ['16px', { lineHeight: '1.12' }],
        h4: ['20px', { lineHeight: '1.12' }],
        h3: ['25px', { lineHeight: '1.12' }],
        h2: ['32px', { lineHeight: '1.12' }],
        h1: ['42px', { lineHeight: '1.12' }],
      },

      boxShadow: {
        sm: 'var(--shadow-sm)',
        md: 'var(--shadow-md)',
        lg: 'var(--shadow-lg)',
      },

      zIndex: {
        content: '0',
        sticky: '2',
        viewer: '60',
        overlay: '80',
      },

      keyframes: {
        shimmer: { '0%': { opacity: '.3' }, '50%': { opacity: '.7' }, '100%': { opacity: '.3' } },
        spin: { to: { transform: 'rotate(360deg)' } },
      },

      animation: {
        shimmer: 'shimmer 1.6s ease-in-out infinite',
        spin: 'spin .7s linear infinite',
      },
    },
  },
} satisfies Config
