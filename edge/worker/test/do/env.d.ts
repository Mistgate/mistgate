declare namespace Cloudflare {
  interface Env {
    LIMITER: DurableObjectNamespace<import("../../src/limiter").Limiter>;
  }
}
