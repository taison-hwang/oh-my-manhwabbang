import { existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

// A shared setup file (jest-dom matchers, MSW server lifecycle) is optional:
// no work package owns one yet, and referencing a file that does not exist
// makes every vitest run fail before it starts. Wire it in when it appears.
const setupFile = fileURLToPath(new URL('./src/test/setup.ts', import.meta.url))

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
    // WP-00 ships no tests of its own; an empty run is a pass, not a failure.
    passWithNoTests: true,
    ...(existsSync(setupFile) ? { setupFiles: [setupFile] } : {}),
    restoreMocks: true,
    css: false,
  },
})
