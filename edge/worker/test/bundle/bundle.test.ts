import { readFileSync } from "node:fs";
import { join } from "node:path";
import { convertV4MiniflareOptions, Miniflare, type V4WorkerOptions as WorkerOptions } from "miniflare";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

// Loads the two scripts of the signed edge bundle exactly as they were built: worker.js / provision.js as ES modules and
// panel.wasm as a CompiledWasm module, with the bindings edge.json describes. MISTGATE_EDGE_BUNDLE is the directory the
// tarball was extracted to (scripts/test-edge-bundle.ps1 and the release workflow set it); without it the suite is skipped,
// so a plain `pnpm test` stays fast and needs no Go build.
const dir = process.env.MISTGATE_EDGE_BUNDLE;

interface Script {
  role: string;
  default_name: string;
  modules: { name: string; type: string }[];
  durable_objects: { binding: string; class_name: string }[];
  durable_objects_external: { binding: string; class_name: string; script_role: string }[];
  d1: { binding: string }[];
  assets?: { directory: string; binding: string };
}
interface EdgeJson {
  compatibility_date: string;
  compatibility_flags: string[];
  scripts: Script[];
}

const MASTER_KEY = "0123456789abcdef".repeat(4);

describe.skipIf(!dir)("the built edge bundle in workerd", () => {
  let mf: Miniflare;
  let edge: EdgeJson;
  let log: string[];

  const script = (role: string) => edge.scripts.find((s) => s.role === role)!;

  /** One worker of the Miniflare instance, from the bundle's own edge.json: modules, Durable Objects, D1, assets. */
  function worker(s: Script): WorkerOptions {
    const byRole = (role: string) => script(role).default_name;
    return {
      name: s.default_name,
      modulesRoot: dir!,
      modules: s.modules.map((m) => ({ type: m.type === "esm" ? "ESModule" : "CompiledWasm", path: join(dir!, m.name) })),
      compatibilityDate: edge.compatibility_date,
      compatibilityFlags: edge.compatibility_flags,
      bindings: { MASTER_KEY },
      d1Databases: { DB: "mistgate-test" },
      durableObjects: {
        ...Object.fromEntries(s.durable_objects.map((d) => [d.binding, { className: d.class_name, useSQLite: true }])),
        ...Object.fromEntries(s.durable_objects_external.map((d) => [d.binding, { className: d.class_name, scriptName: byRole(d.script_role) }])),
      },
      ...(s.assets
        ? {
            assets: {
              directory: join(dir!, s.assets.directory),
              binding: s.assets.binding,
              // what wrangler.example.toml says: every request goes to the Worker first, only an exact file path is a hit
              run_worker_first: true,
              routerConfig: { has_user_worker: true, invoke_user_worker_ahead_of_assets: true },
              assetConfig: { html_handling: "none" as const, not_found_handling: "none" as const },
            },
          }
        : {}),
    };
  }

  beforeAll(async () => {
    edge = JSON.parse(readFileSync(join(dir!, "edge.json"), "utf8"));
    log = [];
    mf = new Miniflare(
      convertV4MiniflareOptions({
        // main first: its D1 and objects are the ones the Provisioner script binds
        workers: [worker(script("main")), worker(script("provision"))],
        handleStructuredLogs: (entry) => log.push(entry.message),
      }),
    );
    await mf.ready;
  }, 120_000);

  afterAll(async () => {
    await mf?.dispose();
  });

  it("provision.js answers 404 on its own fetch without starting Go", async () => {
    const provision = await mf.getWorker(script("provision").default_name);
    const response = await provision.fetch("https://provision.invalid/");
    expect(response.status).toBe(404);
    expect(await response.text()).toBe("Not Found");
    // Go has not run in that isolate: a started panel logs at init (main's does, below), and nothing was logged here.
    expect(log).toEqual([]);
  }, 60_000);

  it("worker.js starts the panel and answers the decoy with 200", async () => {
    const response = await mf.dispatchFetch("https://panel.example.test/");
    const body = await response.text();
    expect(response.status, body.slice(0, 500)).toBe(200);
    expect(response.headers.get("content-type")).toMatch(/text\/html/);
    expect(body.length).toBeGreaterThan(100);
    // the decoy is a plain site: nothing of the panel shows
    expect(body).not.toMatch(/setup|admin|sign in/i);
  }, 120_000);

  it("the Provisioner script still answers 404 after the panel ran in the main script", async () => {
    const provision = await mf.getWorker(script("provision").default_name);
    expect((await provision.fetch("https://provision.invalid/anything")).status).toBe(404);
  }, 60_000);
});
