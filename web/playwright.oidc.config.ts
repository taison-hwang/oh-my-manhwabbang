import { defineConfig, devices } from '@playwright/test'

// The oidc round of ruling E-74, started by scripts/e2e/e2e-oidc.sh: one server in
// `auth.mode: oidc` and the fake uni-auth. Only 14-oidc.spec.ts runs here, and
// only here — playwright.config.ts ignores it. One viewport: what this round
// checks is the session, not the layout.
export default defineConfig({
  testDir: './e2e',
  testMatch: '14-oidc.spec.ts',
  outputDir: './test-results-oidc',
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: [['list'], ['html', { open: 'never', outputFolder: './playwright-report-oidc' }]],
  timeout: 120_000,
  expect: { timeout: 10_000 },
  use: {
    ...devices['Desktop Chrome'],
    baseURL: process.env.PLAYWRIGHT_BASE_URL ?? 'http://127.0.0.1:8792',
    actionTimeout: 30_000,
    channel: 'chrome',
    viewport: { width: 1440, height: 900 },
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'off',
    locale: 'ko-KR',
    timezoneId: 'Asia/Seoul',
  },
})
