import { existsSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import react from '@vitejs/plugin-react'
import { defineConfig, type Plugin } from 'vite'

const appEntry = fileURLToPath(new URL('./src/main.tsx', import.meta.url))
const distGitkeep = fileURLToPath(new URL('./dist/.gitkeep', import.meta.url))

/**
 * Lets `pnpm build` succeed before WP-05 has created `src/main.tsx`.
 *
 * WP-00 owns index.html but deliberately does NOT own src/main.tsx, so on a
 * fresh checkout the HTML references a module that is not there yet and Vite
 * would fail to resolve it. Rather than commit a placeholder entry that WP-05
 * would then have to delete — and that a merge could silently resurrect — the
 * entry resolves to an empty virtual module while the real file is missing.
 *
 * The moment `src/main.tsx` exists this plugin becomes inert: it only ever
 * intercepts when `existsSync` says the file is absent, and the check runs per
 * resolution rather than once at config load so a dev server started before
 * WP-05 lands picks the real entry up on the next request.
 */
function tolerateMissingEntry(): Plugin {
  const virtualId = '\0shelf:missing-app-entry'
  return {
    name: 'shelf:tolerate-missing-app-entry',
    enforce: 'pre',
    resolveId(source) {
      if (!source.endsWith('/src/main.tsx') && source !== 'src/main.tsx') return null
      if (existsSync(appEntry)) return null
      return virtualId
    },
    load(id) {
      if (id !== virtualId) return null
      return [
        '// Placeholder entry: web/src/main.tsx does not exist yet (WP-05 owns it).',
        'export {}',
        '',
      ].join('\n')
    },
  }
}

/**
 * Restores `dist/.gitkeep` after every build.
 *
 * `emptyOutDir` wipes dist/, including the committed .gitkeep that guarantees
 * `//go:embed all:dist` in web/embed.go always has something to match. Without
 * this, the sequence `pnpm build` then `git clean`/checkout of dist/ leaves the
 * Go package uncompilable with "pattern dist: no matching files found".
 */
function keepDistPlaceholder(): Plugin {
  return {
    name: 'shelf:keep-dist-placeholder',
    apply: 'build',
    closeBundle() {
      writeFileSync(distGitkeep, '')
    },
  }
}

export default defineConfig({
  plugins: [react(), tolerateMissingEntry(), keepDistPlaceholder()],

  // Relative asset URLs. The Go server can then mount the same build at "/" or
  // under any base_path (NFR-SEC-003) without rewriting asset references —
  // only the <base href> in index.html changes.
  base: './',

  build: {
    // web/dist is what //go:embed all:dist swallows (arch §2.1).
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    // **Never inline an asset as a `data:` URI.** Vite inlines anything under
    // 4096 bytes by default, and the product's own CSP is `default-src 'self'`
    // with no `font-src` or `img-src` of its own (arch §8.4) — so a `data:`
    // URI is refused by the page that asked for it.
    //
    // This was not theoretical. E-46 vendors the 藏 of the 낙관 as its own
    // 2 148-byte subset, precisely so the mark is not a tofu box on a machine
    // with no CJK serif installed; Vite inlined it, the CSP blocked it, and the
    // seal rendered from a *system* font on the one machine that had one. The
    // e2e console guard is what surfaced it — "Loading the font 'data:font/…'
    // violates the following Content Security Policy directive" — and nothing
    // on screen looked wrong, which is the whole reason that guard exists.
    //
    // `0` rather than a threshold: any inlined asset is the same defect, and a
    // number invites the next small file to sit under it.
    assetsInlineLimit: 0,
    // The library and viewer are both virtualised; chunks stay small. Warn
    // early rather than discover a 2 MB bundle in the 20 MB binary budget.
    chunkSizeWarningLimit: 600,
    rollupOptions: {
      output: {
        // Hashed filenames only. The server serves everything under assets/
        // with `Cache-Control: immutable`, which is only honest if the name
        // changes when the bytes do.
        entryFileNames: 'assets/[name]-[hash].js',
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash][extname]',
      },
    },
  },

  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      // `pnpm dev` talks to `make dev` on 8790.
      '/api': {
        target: 'http://localhost:8790',
        changeOrigin: false,
      },
    },
  },
})
