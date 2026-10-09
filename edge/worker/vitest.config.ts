import { cloudflareTest } from "@cloudflare/vitest-pool-workers";
import { defineConfig } from "vitest/config";

// Two projects: the pure code runs under plain Node; the Durable Object class runs in workerd (test/do).
export default defineConfig({
  test: {
    // workerd reports an exception thrown inside a Durable Object as an unhandled rejection even when the caller catches
    // it; the malformed-request test and the failing `ask` calls throw on purpose. Only an error that came out of a
    // Durable Object (`remote`; one thrown by the test code itself has no such mark) with one of those messages is let
    // through. workerd gives no way to say "this one is expected" per call, so this is as narrow as it gets.
    onUnhandledError: (error) =>
      (error.remote === true && /^(limiter: |timeout$|link lost$|duplicate request id$)/.test(error.message)) ||
      // workerd.test.ts resets an object on purpose (ctx.abort) and reports that as an unhandled rejection too.
      (error.durableObjectReset === true && error.message === "reset by the test")
        ? false
        : undefined,
    projects: [
      { test: { name: "node", include: ["test/*.test.ts"] } },
      {
        plugins: [cloudflareTest({ main: "./test/do/worker.ts", wrangler: { configPath: "./test/do/wrangler.test.toml" } })],
        test: { name: "workers", include: ["test/do/*.test.ts"] },
      },
    ],
  },
});
