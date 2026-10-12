declare namespace Cloudflare {
  interface Env {
    LIMITER: DurableObjectNamespace<import("../../src/limiter").Limiter>;
    NODELINK: DurableObjectNamespace<import("../../src/nodelink").NodeLink>;
    PROVISIONER: DurableObjectNamespace<import("../../src/provisioner").Provisioner>;
    AUX_PROVISIONER: DurableObjectNamespace<import("../../src/provisioner").Provisioner>;
  }
}
