// Bundles the two Worker scripts for the signed edge bundle (`mistgate release build mistgate-edge.tar.gz`):
//   node build-bundle.mjs <wasm_exec.js> <outdir>   ->   <outdir>/worker.js, <outdir>/provision.js
// Both are ES modules that import `./panel.wasm` as a separate CompiledWasm module (the file itself is put next to them
// by the caller). wasm_exec.js is the copy that belongs to the Go toolchain that built panel.wasm. The output depends
// only on the sources, the pinned esbuild version (package.json) and wasm_exec.js: no paths, no timestamps, no source maps.
import { build } from "esbuild";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const [wasmExec, outDir] = process.argv.slice(2);
if (!wasmExec || !outDir) {
  console.error("usage: node build-bundle.mjs <wasm_exec.js> <outdir>");
  process.exit(2);
}
const root = dirname(fileURLToPath(import.meta.url));

// src/panel.ts imports ../dist/panel.wasm and ../dist/wasm_exec.js (what scripts/build-edge.ps1 puts there for wrangler dev).
// Here the wasm stays a separate module of the script and wasm_exec.js comes from the toolchain of this build.
const redirect = {
  name: "edge-bundle-inputs",
  setup(b) {
    b.onResolve({ filter: /(^|\/)dist\/panel\.wasm$/ }, () => ({ path: "./panel.wasm", external: true }));
    // A virtual module, so that the bundle holds no path of the machine that built it.
    b.onResolve({ filter: /(^|\/)dist\/wasm_exec\.js$/ }, () => ({ path: "wasm_exec.js", namespace: "go" }));
    b.onLoad({ filter: /.*/, namespace: "go" }, () => ({ contents: readFileSync(wasmExec), loader: "js" }));
  },
};

const result = await build({
  absWorkingDir: root,
  entryPoints: { worker: "src/index.ts", provision: "src/provision-index.ts" },
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
  logLevel: "warning",
  plugins: [redirect],
});

mkdirSync(outDir, { recursive: true });
for (const file of result.outputFiles) {
  const name = file.path.split(/[\\/]/).pop();
  writeFileSync(resolve(outDir, name), file.contents);
}
