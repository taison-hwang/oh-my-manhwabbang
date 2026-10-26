import { fileURLToPath } from 'node:url'

import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

// The shared setup file makes an MSW request no handler answers fail its test
// (src/test/msw.ts). It is unconditional on purpose: a missing setup file must
// fail the run — every suite then fails to load — not quietly drop the strict
// check while every test stays green. That it actually ran is checked by
// src/test/msw.test.ts ('is installed by the shared setup file').
const setupFile = fileURLToPath(new URL('./src/test/setup.ts', import.meta.url))

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
    // An empty run is a failure. It was a pass while WP-00 had no tests; kept,
    // a broken `include` or a filter that matches nothing would report green.
    // The only caller, `pnpm test` (make web-gates), passes no filter.
    passWithNoTests: false,
    setupFiles: [setupFile],
    restoreMocks: true,
    css: false,
  },
})
