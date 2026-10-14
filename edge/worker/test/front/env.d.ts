declare namespace Cloudflare {
  // FRONT's own bindings (test/front/wrangler.test.toml), for the tests that call it through the pool's `env` and `exports`.
  interface Env {
    DB: D1Database;
    PUBLIC: Fetcher;
    ADMIN: Fetcher;
    LINK: Fetcher;
    LIMITER: DurableObjectNamespace<import("../../src/limiter").Limiter>;
    NODELINK: DurableObjectNamespace<import("../../src/nodelink").NodeLink>;
  }
}
