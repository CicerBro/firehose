import { defineConfig, devices } from '@playwright/test';

// E2E (§13.5): the built binary (repo root) serves the embedded UI in --dev mode. The page runs on
// http://localhost:8080 while transfers go to http://127.0.0.1:8080, so they are cross-origin
// (credentialed CORS) exactly like production. Build first: `make build` (or `make e2e`).
// Overridable for local runs when :8080 is taken: E2E_PORT=18080 FIREHOSE_BIN=/path/to/firehose.
const port = process.env.E2E_PORT ?? '8080';
const bin = process.env.FIREHOSE_BIN ?? '../firehose';

export default defineConfig({
  testDir: 'tests/e2e',
  timeout: 90_000,
  fullyParallel: false,
  workers: 1, // the server allows one active session (newest wins): run engines one after another
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? 'github' : 'list',
  use: {
    baseURL: `http://localhost:${port}`,
    trace: 'retain-on-failure',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
    { name: 'firefox', use: { ...devices['Desktop Firefox'] } },
    { name: 'webkit', use: { ...devices['Desktop Safari'] } },
  ],
  webServer: {
    command: `${bin} serve --dev --listen 127.0.0.1:${port} --public-url http://localhost:${port} --test-hosts http://127.0.0.1:${port}`,
    url: `http://127.0.0.1:${port}/healthz`,
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
    stdout: 'ignore',
    stderr: 'pipe',
  },
});
