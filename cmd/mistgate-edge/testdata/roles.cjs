"use strict";

// Smoke test of the edge role entries (cmd/mistgate-edge-{public,link,jobs,admin,provision}) in Node, on one fake D1 that all
// of them share, as the Workers share the real one:
//   node roles.cjs <wasm_exec.js> <dir holding public.wasm link.wasm jobs.wasm admin.wasm provision.wasm>
// Each role is instantiated in its own isolate and has to
//   - lack the mgPanel members of the other roles (a call would be a TypeError in the Worker),
//   - refuse to start on a database nobody has initialised, except the admin, which creates the instance,
//   - then, on the initialised database, answer one call of its own.

const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { randomBytes } = require("node:crypto");

const [wasmExecPath, wasmDir] = process.argv.slice(2);
if (!wasmExecPath || !wasmDir) throw new Error("usage: roles.cjs <wasm_exec.js> <dir with the five role wasm files>");

const dir = fs.mkdtempSync(path.join(os.tmpdir(), "mg-roles-"));
process.env.MISTGATE_BRIDGE_D1_PATH = path.join(dir, "d1.sqlite");
require(path.resolve(__dirname, "../../../edge/d1driver/testdata/fake-d1.cjs"));
require(path.resolve(wasmExecPath));

const HOST = "https://roles.example.com";
const ADMIN = "/admin-4f2a/";
const SUB = "/sub-9c1d/";
const masterKey = new Uint8Array(randomBytes(32));

// What each role exposes, besides init (which all have).
const members = {
  public: ["fetch"],
  link: ["fetch", "link"],
  jobs: ["cron"],
  admin: ["fetch"],
  provision: ["fetch", "provision"],
};
const everyMember = ["fetch", "cron", "link", "provision"];

function options(role) {
  const out = {
    d1: globalThis.__d1,
    masterKey,
    publicURL: HOST,
    adminPrefix: ADMIN,
    subPrefix: SUB,
    assets: async () => null,
    limit: async () => ({ ok: true, retryAfterMs: 0, remaining: 100, first: true }),
    nodeLink: { ask: async () => ({ error: "lost" }), send: async () => false, close: async () => {}, poke: async () => 0 },
  };
  if (role === "admin" || role === "jobs") out.provision = { role: "forward", kick: async () => {} };
  if (role === "provision") {
    out.provision = { role: "serve", kick: async () => {}, sshConnect: async () => { throw new Error("no sockets in this test"); } };
  }
  return out;
}

let previous;
async function start(role) {
  const go = new Go();
  const bytes = fs.readFileSync(path.join(wasmDir, `${role}.wasm`));
  const { instance } = await WebAssembly.instantiate(bytes, go.importObject);
  void go.run(instance).catch((error) => { throw error; });
  while (!globalThis.mgPanel || globalThis.mgPanel === previous) await new Promise((resolve) => setImmediate(resolve));
  previous = globalThis.mgPanel;
  return { panel: globalThis.mgPanel, size: bytes.length };
}

async function get(panel, url, headers = []) {
  const response = await panel.fetch({ method: "GET", url, headers, body: null });
  if (response.waitUntil) await response.waitUntil;
  return { status: response.status, body: Buffer.from(response.body).toString("utf8") };
}

function memberNames(panel) {
  return everyMember.filter((name) => typeof panel[name] === "function");
}

async function main() {
  const roles = Object.keys(members);

  // 1. the members, and the refusal on a database without an instance
  for (const role of roles) {
    const { panel, size } = await start(role);
    assert.equal(typeof panel.init, "function", `${role}: no init`);
    assert.deepEqual(memberNames(panel), members[role], `${role}: mgPanel members`);
    if (role === "admin") continue;
    await assert.rejects(panel.init(options(role)), /not initialised/, `${role}: init on an empty database`);
    console.log(`${role}: ${size} bytes, members [${memberNames(panel)}], refuses an empty database`);
  }

  // 2. the admin creates the instance
  const admin = await start("admin");
  await admin.panel.init(options("admin"));
  const adminApi = await get(admin.panel, `${HOST}${ADMIN}api/mistgate.admin.v1.NodeService/ListNodes`);
  assert.equal(adminApi.status, 401, `admin: a call without a session = ${adminApi.status} ${adminApi.body.slice(0, 80)}`);
  assert.equal((await get(admin.panel, `${HOST}${SUB}any-token`)).status, 404, "admin: the subscription path is the decoy");
  console.log("admin: created the instance, answers the admin API (401 without a session), decoy on the subscription path");

  // 3. every other role starts on it and answers once
  const { panel: pub } = await start("public");
  await pub.init(options("public"));
  assert.equal((await get(pub, `${HOST}${SUB}any-token`)).status, 404, "public: an unknown token is the decoy");
  assert.equal((await get(pub, `${HOST}${ADMIN}`)).status, 404, "public: the admin path is the decoy");
  assert.equal((await get(pub, `${HOST}${ADMIN}api/mistgate.admin.v1.NodeService/ListNodes`)).status, 404, "public: the admin API is the decoy");
  console.log("public: instantiates, answers the decoy on an unknown token and on the admin paths");

  const { panel: link } = await start("link");
  await link.init(options("link"));
  const challenge = await link.link("challenge", { audience: "node.example.com" });
  assert.ok(challenge.nonce instanceof Uint8Array && challenge.nonce.length > 0 && challenge.frame instanceof Uint8Array, "link: challenge");
  assert.equal((await get(link, `${HOST}${ADMIN}`)).status, 404, "link: the admin path is the decoy");
  assert.equal((await get(link, `${HOST}${SUB}any-token`)).status, 404, "link: the subscription path is the decoy");
  console.log("link: instantiates, answers a link challenge, decoy on the other surfaces");

  const { panel: jobs } = await start("jobs");
  await jobs.init(options("jobs"));
  const tick = await jobs.cron({ at: Date.now() });
  if (tick.waitUntil) await tick.waitUntil;
  console.log("jobs: instantiates, runs a cron tick");

  const { panel: prov } = await start("provision");
  await prov.init(options("provision"));
  const pass = await prov.provision();
  assert.equal(pass.more, false, "provision: nothing queued");
  const refused = await get(prov, `${HOST}${ADMIN}api/mistgate.admin.v1.ProvisioningService/GetNodeProvision`);
  assert.equal(refused.status, 503, "provision: a request without the build header");
  assert.match(refused.body, /panel_updating/);
  console.log("provision: instantiates, runs an empty install pass, refuses a request of another build");

  console.log("roles: ok");
  cleanUp();
  process.exit(0);
}

// the database file is still open on Windows: leaving it behind in the temp directory is fine
function cleanUp() {
  try {
    fs.rmSync(dir, { recursive: true, force: true });
  } catch {}
}

main().catch((error) => {
  console.error(error);
  cleanUp();
  process.exit(1);
});
