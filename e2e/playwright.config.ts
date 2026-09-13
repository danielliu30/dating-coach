import { defineConfig, devices } from '@playwright/test';

const port = process.env.NGINX_PORT ?? '80';

/**
 * The suite drives one shared docker compose stack and several specs stop and
 * start containers, so it runs strictly serially: one worker, no parallel
 * files, no retries (a retry would hide a flaky stack rather than a flaky
 * test). Readiness is checked once in global-setup.ts.
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
    baseURL: `http://localhost:${port}`,
    trace: 'retain-on-failure',
    video: 'retain-on-failure',
    screenshot: 'only-on-failure',
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
});
