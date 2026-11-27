import { defineConfig } from "@playwright/test";

// The smoke test runs against the real
// api with bench's null engine, started by ../e2e/smoke.py, which passes the
// dev server's URL. It drives the installed Google Chrome by default, so no
// browser download is needed; PLAYWRIGHT_CHANNEL="" uses Playwright's own
// Chromium (after `npx playwright install chromium`).
const channel = process.env.PLAYWRIGHT_CHANNEL ?? "chrome";

export default defineConfig({
  testDir: "e2e",
  testMatch: "*.pw.ts",
  timeout: 120_000, // a run goes through launch, bench and analysis
  retries: 0,
  reporter: "list",
  use: {
    baseURL: process.env.SERVTUNER_SMOKE_URL ?? "http://127.0.0.1:5173/",
    channel: channel || undefined,
    trace: "retain-on-failure",
    // A click or fill that cannot happen fails in seconds with a trace, instead of waiting out the
    // whole test (the cluster spec's limit is over an hour); expect timeouts are set per step.
    actionTimeout: 15_000,
  },
});
