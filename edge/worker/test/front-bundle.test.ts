import { build } from "esbuild";
import { describe, expect, it } from "vitest";

// The new entries bundle the way build-bundle.mjs bundles the single script (same esbuild options, wasm and glue left out), so
// that step 4 can add them to the release without finding a surprise. FRONT has no wasm at all and must stay small: its CI
// limit is 1 MiB (plan 5, the per-role limits).

// vitest runs from the package directory (edge/worker)
const root = process.cwd();

async function bundle(entry: string) {
  const wasmImports: string[] = [];
  const result = await build({
    absWorkingDir: root,
    entryPoints: [entry],
    outdir: "out",
    write: false,
    bundle: true,
    format: "esm",
    platform: "neutral",
    target: "es2022",
    conditions: ["workerd", "worker", "browser"],
    mainFields: ["browser", "module", "main"],
    external: ["cloudflare:*", "node:*"],
    legalComments: "none",
    logLevel: "silent",
    plugins: [
      {
        name: "edge-bundle-inputs",
        setup(b) {
          b.onResolve({ filter: /(^|\/)dist\/[a-z]+\.wasm$/ }, (args) => {
            const file = args.path.split("/").pop()!;
            wasmImports.push(file);
            return { path: `./${file}`, external: true };
          });
          b.onResolve({ filter: /(^|\/)dist\/wasm_exec\.js$/ }, () => ({ path: "wasm_exec.js", namespace: "go" }));
          b.onLoad({ filter: /.*/, namespace: "go" }, () => ({ contents: "globalThis.Go = class {};", loader: "js" }));
        },
      },
    ],
  });
  const file = result.outputFiles[0]!;
  return { bytes: file.contents.length, text: file.text, wasmImports };
}

/** The names a bundle exports: the `export { a, b as c }` clause esbuild ends an ES module with. */
const exported = (text: string) =>
  [...text.matchAll(/export\s*\{([^}]*)\}/g)].flatMap((m) => m[1]!.split(",").map((part) => part.trim().split(/\s+as\s+/).pop()!)).filter(Boolean);

describe("the bundles of the split edition", () => {
  it("FRONT has no wasm, is far under its 1 MiB limit, and exports the two Durable Objects and a handler, no entrypoint", async () => {
    const front = await bundle("src/front.ts");
    expect(front.wasmImports).toEqual([]);
    expect(front.bytes).toBeLessThan(1024 * 1024);
    expect(exported(front.text).sort()).toEqual(["Limiter", "NodeLink", "default"]);
  });

  it.each(["public", "admin", "link", "jobs", "provision"])("the %s role bundles with exactly its own wasm", async (role) => {
    const out = await bundle(`src/roles/${role}.ts`);
    expect(out.wasmImports).toEqual([`${role}.wasm`]);
  });

  it("the link role exports the PanelLink entrypoint; no other role does", async () => {
    expect(exported((await bundle("src/roles/link.ts")).text)).toContain("PanelLink");
    for (const role of ["public", "admin", "jobs"]) expect(exported((await bundle(`src/roles/${role}.ts`)).text)).not.toContain("PanelLink");
  });

  it("the provision role exports the Provisioner object", async () => {
    expect(exported((await bundle("src/roles/provision.ts")).text)).toContain("Provisioner");
  });

  it("the single script still bundles with panel.wasm, as before", async () => {
    const index = await bundle("src/index.ts");
    expect(index.wasmImports).toEqual(["panel.wasm"]);
    expect(exported(index.text)).toEqual(expect.arrayContaining(["Limiter", "NodeLink", "PanelLink"]));
    expect((await bundle("src/provision-index.ts")).wasmImports).toEqual(["panel.wasm"]);
  });
});
