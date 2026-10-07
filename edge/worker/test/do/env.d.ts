declare namespace Cloudflare {
  interface Env {
    LIMITER: DurableObjectNamespace<import("../../src/limiter").Limiter>;
    NODELINK: DurableObjectNamespace<import("../../src/nodelink").NodeLink>;
  }
}
