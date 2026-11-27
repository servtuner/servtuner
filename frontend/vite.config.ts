import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

// Test fixtures are the shared schema examples and bench's golden analyses
// (src/test/bundles.ts); only the dev and test server reads them.
const fixtureDirs = ["../schema/examples", "../bench/testdata/analysis"].map((dir) =>
  fileURLToPath(new URL(dir, import.meta.url)),
);

// SERVTUNER_DEV_API=http://127.0.0.1:8000 proxies the dev server's /api to a
// running api (which serves at its root), as nginx does in the image. The
// Playwright smoke test (e2e/smoke.py) uses it.
const devApi = process.env.SERVTUNER_DEV_API;

export default defineConfig({
  base: "./",
  plugins: [react()],
  server: {
    fs: { allow: [".", ...fixtureDirs] },
    proxy: devApi ? { "/api": { target: devApi, rewrite: (path) => path.replace(/^\/api/, "") } } : undefined,
  },
  test: {
    environment: "jsdom",
    setupFiles: ["src/test/setup.ts"],
    css: false,
    exclude: ["e2e/**", "node_modules/**"],
  },
});
