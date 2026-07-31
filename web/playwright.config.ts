import { defineConfig, devices } from '@playwright/test'

// The E2E server that scripts/e2e.sh starts (impl-plan §5.5: 8791, verified
// free). Override with PLAYWRIGHT_BASE_URL to point at `pnpm dev` or a
// long-running instance.
const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? 'http://127.0.0.1:8791'

// The four CSS viewport widths the responsive layer is specified against
// (ui-spec §7): fixed 240px sidebar, 56px icon rail, tablet, off-canvas drawer.
// The reference screenshots in docs/ui-shots/ were captured at 2x DPR, so the
// projects match that too — a 1x capture will not diff against them.
const VIEWPORTS = [
  { name: 'desktop-1440', width: 1440, height: 900 },
  { name: 'laptop-1024', width: 1024, height: 768 },
  { name: 'tablet-768', width: 768, height: 1024 },
  { name: 'mobile-400', width: 400, height: 860 },
] as const

export default defineConfig({
  testDir: './e2e',
  outputDir: './test-results',
  // Serial, on purpose, and not a performance oversight.
  //
  // All four projects drive **one** server — `scripts/e2e.sh` starts it in step
  // 4 and Playwright is step 11 — and a good deal of what these specs assert is
  // persisted *server-side*: `library_view`, `library_sort`, `library_order`,
  // `library_scope` and `theme` in `Settings` (arch §7.8, A-5), reading progress
  // and the per-book `reading_direction`/`display_mode` overrides (arch §7.6).
  // `useLibrarySettingsSync` hydrates the first three into the store on every
  // load, so two workers means one test's 리스트 toggle deciding what another
  // test's "the grid renders ten cards" sees. Nothing in the product is wrong
  // with that — it is a single-user application whose settings are global by
  // design — so the suite runs one test at a time instead.
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: [['list'], ['html', { open: 'never', outputFolder: './playwright-report' }]],
  // The 1 540-page archive of §6.3 row 9 is a 1.34 GB deflate stream on a cold
  // cache; 60 s was the wave-0 guess and is not enough for its first thumbnail
  // pass on the real collection.
  timeout: 120_000,
  expect: { timeout: 10_000 },

  use: {
    baseURL,
    // The system Chrome at /usr/bin/google-chrome. Combined with
    // PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 this keeps the ~500 MB browser
    // download out of the build (impl-plan §6.3 step 6).
    channel: 'chrome',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'off',
    locale: 'ko-KR',
    timezoneId: 'Asia/Seoul',
  },

  projects: VIEWPORTS.map(({ name, width, height }) => ({
    name,
    use: {
      ...devices['Desktop Chrome'],
      channel: 'chrome',
      viewport: { width, height },
      deviceScaleFactor: 2,
      // Below 768 the app is a touch layout: tap zones turn pages and the
      // sidebar becomes an off-canvas drawer (ui-spec §7, D-42).
      hasTouch: width < 768,
      isMobile: false,
    },
  })),
})
