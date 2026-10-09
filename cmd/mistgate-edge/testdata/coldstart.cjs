"use strict";

// Cold-start numbers of the edge panel (design/cloudflare-edition/AGENT-LINK.md §7.7), in Node on the fake D1:
//   node cmd/mistgate-edge/testdata/coldstart.cjs <panel.wasm> <wasm_exec.js>
// Per isolate: instantiate the module, run the Go program until it publishes mgPanel, mgPanel.init on an empty database
// (migrations) and on a migrated one (what a cold isolate of a live panel does), then the first request. CPU is
// process.cpuUsage (user + system), so it includes the fake SQLite; "db" is the part spent inside it. Indicative only:
// the numbers of a Worker are the 6g measurements.

const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { randomBytes } = require("node:crypto");

const [wasmPath, wasmExecPath] = process.argv.slice(2);
if (!wasmPath || !wasmExecPath) throw new Error("usage: coldstart.cjs <panel.wasm> <wasm_exec.js>");

const dir = fs.mkdtempSync(path.join(os.tmpdir(), "mg-coldstart-"));
process.env.MISTGATE_BRIDGE_D1_PATH = path.join(dir, "d1.sqlite");
require(path.resolve(__dirname, "../../../edge/d1driver/testdata/fake-d1.cjs"));
require(path.resolve(wasmExecPath));

// the text of every statement prepared while a count is open (printed for the init of a migrated database)
let queryLog = [];
const prepareD1 = globalThis.__d1.prepare.bind(globalThis.__d1);
globalThis.__d1.prepare = (query) => {
  queryLog.push(query.replace(/\s+/g, " ").trim().slice(0, 110));
  return prepareD1(query);
};

async function measure(fn) {
  const cpu0 = process.cpuUsage();
  const t0 = performance.now();
  const value = await fn();
  const cpu = process.cpuUsage(cpu0);
  return { value, wall: performance.now() - t0, cpu: (cpu.user + cpu.system) / 1000 };
}

async function counted(label, fn) {
  globalThis.__d1.__beginQueryCount(label);
  let out;
  try {
    out = await measure(fn);
  } finally {
    out = { ...out, queries: globalThis.__d1.__endQueryCount() };
  }
  return out;
}

async function startIsolate(module, previous) {
  const go = new Go();
  const inst = await measure(() => WebAssembly.instantiate(module, go.importObject));
  const started = await measure(async () => {
    void go.run(inst.value).catch((error) => { throw error; });
    while (!globalThis.mgPanel || globalThis.mgPanel === previous) await new Promise((resolve) => setImmediate(resolve));
    return globalThis.mgPanel;
  });
  return { panel: started.value, memory: inst.value.exports.mem, instantiate: inst, run: started };
}

const masterKey = new Uint8Array(randomBytes(32));
const options = () => ({
  d1: globalThis.__d1,
  masterKey,
  publicURL: "https://de1.example.com",
  adminPrefix: "/test-admin/",
  subPrefix: "/test-sub/",
  assets: async () => null,
  limit: async () => ({ ok: true, retryAfterMs: 0, remaining: 100, first: true }),
  nodeLink: { ask: async () => ({ error: "lost" }), close: async () => {}, poke: async () => 0 },
});

const fmt = (n) => n.toFixed(1).padStart(8);
const median = (values) => [...values].sort((a, b) => a - b)[Math.floor(values.length / 2)];

async function main() {
  const bytes = fs.readFileSync(wasmPath);
  const compiled = await measure(() => WebAssembly.compile(bytes));
  console.log(`wasm file ${bytes.length} bytes; WebAssembly.compile (a Worker gets the module precompiled): wall ${fmt(compiled.wall)} ms, cpu ${fmt(compiled.cpu)} ms`);
  let previous;
  const rows = [];
  for (let i = 0; i < 6; i++) {
    const iso = await startIsolate(compiled.value, previous);
    previous = iso.panel;
    const initLabel = i === 0 ? "init, empty database (migrations)" : "init, migrated database";
    queryLog = [];
    const init = await counted(initLabel, () => iso.panel.init(options()));
    const initQueries = queryLog;
    const req = await counted("first GET /", async () => {
      const r = await iso.panel.fetch({ method: "GET", url: "https://de1.example.com/", headers: [], body: null });
      if (r.waitUntil) await r.waitUntil;
      return r;
    });
    rows.push({ i, iso, init, req });
    console.log(`isolate ${i}: ${initLabel}`);
    console.log(`  instantiate   wall ${fmt(iso.instantiate.wall)} ms  cpu ${fmt(iso.instantiate.cpu)} ms`);
    console.log(`  go.run->publish wall ${fmt(iso.run.wall)} ms  cpu ${fmt(iso.run.cpu)} ms`);
    console.log(`  mgPanel.init  wall ${fmt(init.wall)} ms  cpu ${fmt(init.cpu)} ms  db ${fmt(init.queries.dbMillis)} ms  D1 calls ${init.queries.sequentialQueries} (batches ${init.queries.batchCalls}, statements ${init.queries.prepareExecutions})`);
    console.log(`  first GET /   wall ${fmt(req.wall)} ms  cpu ${fmt(req.cpu)} ms  D1 calls ${req.queries.sequentialQueries}`);
    console.log(`  wasm memory   ${(iso.memory.buffer.byteLength / 1048576).toFixed(1)} MB`);
    if (i === 1) initQueries.forEach((q, n) => console.log(`    init D1 statement ${n + 1}: ${q}`));
  }
  const warm = rows.slice(1);
  console.log(`median of ${warm.length} isolates on a migrated database: instantiate cpu ${fmt(median(warm.map((r) => r.iso.instantiate.cpu)))} ms, ` +
    `go.run->publish cpu ${fmt(median(warm.map((r) => r.iso.run.cpu)))} ms, init cpu ${fmt(median(warm.map((r) => r.init.cpu)))} ms ` +
    `(db ${fmt(median(warm.map((r) => r.init.queries.dbMillis)))} ms), init D1 calls ${warm[0].init.queries.sequentialQueries}, ` +
    `first GET / cpu ${fmt(median(warm.map((r) => r.req.cpu)))} ms`);
  fs.rmSync(dir, { recursive: true, force: true });
  process.exit(0);
}

main().catch((error) => {
  console.error(error);
  fs.rmSync(dir, { recursive: true, force: true });
  process.exit(1);
});
