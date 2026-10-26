import { defineConfig, devices } from '@playwright/test'

// Item `bb`. Under Node 24.16 the zip reader Playwright 1.50 bundles (yauzl /
// fd-slicer) stalls without an error on any trace entry over 64 KiB — a failure
// screenshot, a woff2 — so every failed test spent the whole 120 s tracing slot
// in 'stop tracing' and left a trace.zip with no end-of-central-directory.
// Measured: 24.16 stalls between 65 000 and 70 000 B; 24.20 merges the same
// trace in 196 ms. 24.17–24.19 were not measured, so they are warned about too.
// A warning and not a refusal: results are still right, only a failure costs
// 120 s more and its trace is unreadable. scripts/e2e.sh puts
// SHELF_E2E_NODE_DIR first on PATH for step 11.
{
  const [major = 0, minor = 0] = process.versions.node.split('.').map(Number)
  if (major === 24 && minor >= 16 && minor < 20) {
    console.warn(
      `item bb: Node ${process.versions.node} stalls Playwright 1.50's trace merge — every failed ` +
        'test will wait out the 120 s tracing slot and leave a half-written trace.zip. ' +
        'Use Node >= 24.20 (scripts/e2e.sh takes SHELF_E2E_NODE_DIR).',
    )
  }
}

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
    // Item `bb`. Without this an action whose target never becomes actionable —
    // a click on a control a defect has hidden or covered — waits out the whole
    // 120 s test timeout above before failing, and a mutation round pays that per
    // test. 30 s rather than the assertion's 10 s: the real round's first
    // thumbnail pass over a cold cache is legitimately slow to make things
    // clickable, and an action timeout that fires on a good run is worse than
    // none. The other 120 s a failed test used to pay — after a plain `expect`
    // failure too, with the test reported at 8 s — was not an action and not the
    // `consoleGuard` teardown: it was the trace merge stalling under Node 24.16,
    // outside the test's own duration (the guard at the top of this file).
    actionTimeout: 30_000,
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
