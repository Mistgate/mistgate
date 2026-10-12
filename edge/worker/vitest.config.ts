import { cloudflareTest } from "@cloudflare/vitest-pool-workers";
import { defineConfig } from "vitest/config";

// What the Provisioner's script looks like to the main Worker, as plain JavaScript (an auxiliary worker is not bundled): one
// Durable Object that answers a request with what it received and counts kicks. test/do/provision-forward.test.ts sends the
// main Worker's forwarded request to it across the script boundary, the way the binding in wrangler.example.toml does.
const provisionAux = `
import { DurableObject } from "cloudflare:workers";
export class AuxProvisioner extends DurableObject {
  async fetch(request) {
    const path = new URL(request.url).pathname;
    if (path.endsWith("/see-other")) {
      // what the installer page answers a form post with: a 303 to the job's page (relative, like provision/ui.go's)
      return new Response(null, { status: 303, headers: { Location: "?job=prv_1&cancelled=1" } });
    }
    if (path.endsWith("/gzip")) {
      const gzip = new Blob([JSON.stringify({ hello: "gzip" })]).stream().pipeThrough(new CompressionStream("gzip"));
      const bytes = await new Response(gzip).arrayBuffer();
      return new Response(bytes, { status: 200, headers: { "Content-Encoding": "gzip", "Content-Type": "application/json" }, encodeBody: "manual" });
    }
    const body = [...new Uint8Array(await request.arrayBuffer())];
    return Response.json({ method: request.method, url: request.url, headers: [...request.headers], body, kicks: (await this.ctx.storage.get("kicks")) ?? 0 }, { status: 201 });
  }
  async kick() {
    await this.ctx.storage.put("kicks", ((await this.ctx.storage.get("kicks")) ?? 0) + 1);
  }
}
export default { fetch: () => new Response("Not Found", { status: 404 }) };
`;

// Three projects: the pure code runs under plain Node; the Durable Object classes run in workerd (test/do); the built bundle
// runs in a workerd of its own (test/bundle).
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
      // The scripts of a built edge bundle in workerd (Miniflare); skipped unless MISTGATE_EDGE_BUNDLE names the extracted tarball.
      { test: { name: "bundle", include: ["test/bundle/*.test.ts"] } },
      {
        plugins: [
          cloudflareTest({
            main: "./test/do/worker.ts",
            wrangler: { configPath: "./test/do/wrangler.test.toml" },
            miniflare: {
              workers: [
                {
                  name: "provision-aux",
                  modules: true,
                  script: provisionAux,
                  compatibilityDate: "2026-08-01",
                  durableObjects: { AUX: { className: "AuxProvisioner", useSQLite: true } },
                },
              ],
            },
          }),
        ],
        test: { name: "workers", include: ["test/do/*.test.ts"] },
      },
    ],
  },
});
