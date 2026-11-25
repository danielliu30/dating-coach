import { defineConfig, devices } from '@playwright/test';

/**
 * Drives the real public URL (Cloudflare quick tunnel -> nginx :80) instead of
 * localhost. Test plumbing (verification codes, DB asserts, admin API calls)
 * still hits the local stack through helpers/stack.ts, which keeps BASE_URL
 * pointed at localhost.
 *
 * Video + slowMo are for watchability: every browser interaction is throttled
 * by slowMo ms so the journey plays at human pace, and each test records a
 * video under test-results/.
 */
export default defineConfig({
  testDir: './tests',
  globalSetup: './global-setup.ts',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: !!process.env.CI,
  timeout: 3 * 60_000,
  expect: { timeout: 15_000 },
  reporter: [['list'], ['html', { open: 'never', outputFolder: 'playwright-report' }]],
  outputDir: 'test-results',
  use: {
    baseURL: process.env.TUNNEL_URL ?? 'https://mai-cordless-been-photo.trycloudflare.com',
    trace: 'retain-on-failure',
    video: 'on',
    screenshot: 'only-on-failure',
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
    launchOptions: { slowMo: Number(process.env.SLOW_MO ?? 400) },
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
});
