import { cloudflareTest } from "@cloudflare/vitest-pool-workers";
import { defineConfig } from "vitest/config";

// Two projects: the pure code runs under plain Node; the Durable Object class runs in workerd (test/do).
export default defineConfig({
  test: {
    // workerd reports an exception thrown inside a Durable Object as an unhandled rejection even when the caller catches
    // it; the malformed-request test and the failing `ask` calls throw on purpose, and only those errors are let through.
    onUnhandledError: (error) => (/^(limiter: |timeout$|link lost$)/.test(error.message) ? false : undefined),
    projects: [
      { test: { name: "node", include: ["test/*.test.ts"] } },
      {
        plugins: [cloudflareTest({ main: "./test/do/worker.ts", wrangler: { configPath: "./test/do/wrangler.test.toml" } })],
        test: { name: "workers", include: ["test/do/*.test.ts"] },
      },
    ],
  },
});
