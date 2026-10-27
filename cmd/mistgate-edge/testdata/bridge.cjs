"use strict";

const assert = require("node:assert/strict");
const { spawn } = require("node:child_process");
const { randomBytes } = require("node:crypto");
const fs = require("node:fs");
const http = require("node:http");
const net = require("node:net");
const path = require("node:path");
const crypto = require("node:crypto");
const { createHmac } = crypto;
const { pathToFileURL } = require("node:url");
require("./sshconnect.cjs"); // globalThis.__sshConnect: the Worker's sshConnect over node:net, to the fake SSH server

let wasmFailure;
let rejectWasmFailure;
const securityLimits = new Map();
let limitMath;
let liveProjectionQueries = 0;
let nodePokeCalls = 0;
let provisionKicks = 0;
const nodePokeIDs = [];
// The linear memory of every wasm instance (one per modelled isolate). A Worker isolate has 128 MB in all; what the Go
// heap may use is capped here so that about 32 MB stay for the JavaScript side.
const wasmMemories = [];
const WASM_MEMORY_BUDGET_MIB = 96;

const [wasmPath, wasmExecPath, oraclePath, dataDir] = process.argv.slice(2);
if (!wasmPath || !wasmExecPath || !oraclePath || !dataDir) {
  throw new Error("usage: bridge.cjs <panel.wasm> <wasm_exec.js> <vps-oracle> <temp-dir>");
}

const database = path.join(dataDir, "shared.sqlite");
process.env.MISTGATE_BRIDGE_D1_PATH = database;
require(path.resolve(__dirname, "../../../edge/d1driver/testdata/fake-d1.cjs"));
require(wasmExecPath);

const prepareD1 = globalThis.__d1.prepare.bind(globalThis.__d1);
globalThis.__d1.prepare = (query) => {
  if (query.includes("n.liveness_timeout_s AS liveness_timeout_s") && query.includes("l.session AS session")) {
    liveProjectionQueries++;
  }
  return prepareD1(query);
};

function reservePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      server.close((error) => error ? reject(error) : resolve(port));
    });
  });
}

function startOracle(binary, database, port, masterKey) {
  const child = spawn(binary, [database, `127.0.0.1:${port}`], {
    stdio: ["pipe", "pipe", "pipe"],
  });
  child.stdin.end(masterKey);
  const ready = new Promise((resolve, reject) => {
    let output = "";
    let errorOutput = "";
    child.stdout.setEncoding("utf8");
    child.stdout.on("data", (chunk) => {
      output += chunk;
      if (output.includes("READY\n")) resolve();
    });
    child.stderr.setEncoding("utf8");
    child.stderr.on("data", (chunk) => { errorOutput += chunk; });
    child.once("error", reject);
    child.once("exit", (code) => {
      if (code !== 0) reject(new Error(`VPS handler oracle exited with ${code}: ${errorOutput.trim()}`));
    });
  });
  return { child, ready };
}

function oracleRequest(port, request) {
  return new Promise((resolve, reject) => {
    const headers = Object.fromEntries(request.headers);
    headers.host = "example.com";
    const outgoing = http.request({
      hostname: "127.0.0.1",
      port,
      path: new URL(request.url).pathname + new URL(request.url).search,
      method: request.method,
      headers,
    }, (response) => {
      const chunks = [];
      response.on("data", (chunk) => chunks.push(chunk));
      response.on("end", () => resolve({ status: response.statusCode, headers: response.headers, body: Buffer.concat(chunks) }));
    });
    outgoing.once("error", reject);
    if (request.body) outgoing.write(request.body);
    outgoing.end();
  });
}

async function bridgeRequestFor(panel, url, init = {}) {
  const request = new Request(url, init);
  const body = request.body === null ? null : new Uint8Array(await request.arrayBuffer());
  return Promise.race([panel.fetch({
    method: request.method,
    url: request.url,
    headers: Array.from(request.headers.entries()),
    body,
  }), wasmFailure]);
}

async function bridgeRequest(url, init = {}) {
  return bridgeRequestFor(globalThis.mgPanel, url, init);
}

async function countedRequest(label, fn) {
  globalThis.__d1.__beginQueryCount(label);
  try {
    const response = await fn();
    const queries = globalThis.__d1.__endQueryCount();
    if (response && response.waitUntil) await response.waitUntil;
    return { response, queries };
  } catch (error) {
    globalThis.__d1.__endQueryCount();
    throw error;
  }
}

async function countedJob(label, fn) {
  globalThis.__d1.__beginQueryCount(label);
  try {
    const output = await fn();
    if (output && output.waitUntil) await output.waitUntil;
    const queries = globalThis.__d1.__endQueryCount();
    return { output, queries };
  } catch (error) {
    globalThis.__d1.__endQueryCount();
    throw error;
  }
}

function assertQueryBudget(queries) {
  console.log(`D1 query count ${JSON.stringify(queries)}`);
  // Target 6 (README Â§3.1). The AWG formats (Mihomo, .conf) measure 7: one extra round for the AWG scopes; phase 4.
  const budget = /^(mihomo|AWG)/.test(queries.label) ? 7 : 6;
  assert.ok(queries.sequentialQueries <= budget, `${queries.label} used ${queries.sequentialQueries} sequential D1 queries (budget ${budget})`);
}

async function compareHandlerBytes(label, panel, port, url, init = {}) {
  const request = new Request(url, init);
  const { response: edge, queries } = await countedRequest(label, () => bridgeRequestFor(panel, url, init));
  assertQueryBudget(queries);
  const vps = await oracleRequest(port, {
    method: request.method,
    url: request.url,
    headers: Array.from(request.headers.entries()),
    body: request.body === null ? null : Buffer.from(await request.clone().arrayBuffer()),
  });
  if (edge.status !== vps.status || !Buffer.from(edge.body).equals(vps.body)) {
    throw new Error(`${label} edge/VPS response bytes differ`);
  }
  return { edge, vps };
}

async function startIsolate(bytes, previousPanel) {
  const go = new Go();
  const { instance } = await WebAssembly.instantiate(bytes, go.importObject);
  wasmMemories.push(instance.exports.mem);
  void go.run(instance).catch(rejectWasmFailure);
  return Promise.race([(async () => {
    while (!globalThis.mgPanel || globalThis.mgPanel === previousPanel) {
      await new Promise((resolve) => setImmediate(resolve));
    }
    return globalThis.mgPanel;
  })(), wasmFailure]);
}

function decodeBase32(secret) {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = 0;
  let value = 0;
  const result = [];
  for (const character of secret.toUpperCase().replace(/=+$/, "")) {
    const digit = alphabet.indexOf(character);
    if (digit < 0) throw new Error("setup returned an invalid authenticator secret");
    value = (value << 5) | digit;
    bits += 5;
    if (bits >= 8) {
      result.push((value >>> (bits - 8)) & 0xff);
      bits -= 8;
    }
  }
  return Buffer.from(result);
}

function totpCode(secret) {
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000)));
  const digest = createHmac("sha1", decodeBase32(secret)).update(counter).digest();
  const offset = digest[digest.length - 1] & 0x0f;
  const value = ((digest[offset] & 0x7f) << 24) | (digest[offset + 1] << 16) |
    (digest[offset + 2] << 8) | digest[offset + 3];
  return String(value % 1000000).padStart(6, "0");
}

async function securityLimit(input, now = Date.now()) {
  const request = limitMath.parseRequest(input);
  const id = `${input.name}\0${input.key}`;
  const saved = securityLimits.get(id) || {};
  if (request.operation === "reset") {
    delete saved.window;
    if (saved.bucket) securityLimits.set(id, saved);
    else securityLimits.delete(id);
    return { ok: true, retryAfterMs: 0, remaining: 0, first: false };
  }
  if (request.operation === "take") {
    const result = limitMath.takeBucket(saved.bucket, now, request.burst, request.refillMs);
    saved.bucket = result.state;
    securityLimits.set(id, saved);
    return result.reply;
  }
  if (request.operation === "peek") {
    return limitMath.peekWindow(saved.window, now, request.limit, request.spanMs);
  }
  const result = limitMath.recordWindow(saved.window, now, request.limit, request.spanMs, request.lockoutMs);
  saved.window = result.state;
  securityLimits.set(id, saved);
  return result.reply;
}

async function assertSharedLimitVectors() {
  const vectorPath = path.resolve(__dirname, "../../../internal/panel/securitylimit/testdata/vectors.json");
  const vectors = JSON.parse(fs.readFileSync(vectorPath, "utf8"));
  for (const scenario of vectors.scenarios) {
    if (scenario.go_only) continue;
    securityLimits.clear();
    for (const [index, operation] of scenario.operations.entries()) {
      const input = { operation: operation.op, name: operation.name, key: operation.key };
      if (operation.bucket) {
        input.burst = operation.bucket.burst;
        input.refillMs = operation.bucket.refill_ms;
      }
      if (operation.window) {
        input.limit = operation.window.limit;
        input.spanMs = operation.window.span_ms;
        input.lockoutMs = operation.window.lockout_ms;
      }
      const got = await securityLimit(input, operation.at_ms);
      const want = {
        ok: operation.want.allowed,
        retryAfterMs: operation.want.retry_after_ms,
        remaining: operation.want.remaining,
        first: operation.want.first,
      };
      assert.deepEqual(got, want, `${scenario.name}, operation ${index} (${operation.op})`);
    }
  }
  securityLimits.clear();
}

async function connectRPC(panel, method, body, cookie = "", clientIP = "127.0.0.1") {
  const headers = {
    "Content-Type": "application/json",
    "Connect-Protocol-Version": "1",
    "CF-Connecting-IP": clientIP,
    Origin: "https://example.com",
  };
  if (cookie) headers.Cookie = cookie;
  const response = await bridgeRequestFor(panel, `https://example.com/test-admin/api/mistgate.admin.v1.${method}`, {
    method: "POST",
    headers,
    body: JSON.stringify(body),
  });
  const text = Buffer.from(response.body).toString("utf8");
  let message;
  try {
    message = JSON.parse(text);
  } catch {
    throw new Error(`${method} returned a non-JSON response`);
  }
  return { response, message };
}

// protobuf: a length-delimited field (field numbers < 16, payloads < 128 bytes: one tag byte, one length byte)
function pbField(field, bytes) {
  return Buffer.concat([Buffer.from([(field << 3) | 2, bytes.length]), Buffer.from(bytes)]);
}

function pbFields(frame) {
  const out = {};
  for (let i = 0; i < frame.length;) {
    const field = frame[i] >> 3;
    out[field] = Buffer.from(frame.subarray(i + 2, i + 2 + frame[i + 1]));
    i += 2 + frame[i + 1];
  }
  return out;
}

// The panel reads the time through Date (wall clock) and performance.now (monotonic): moving both forward ages the
// per-isolate caches without a sleep. The skew only grows, so no clock runs backwards.
let clockSkewMs = 0;
function advanceClock(ms) {
  if (ms === 0) return;
  if (clockSkewMs === 0) {
    const RealDate = Date;
    const realNow = performance.now.bind(performance);
    globalThis.Date = class extends RealDate {
      constructor(...args) {
        if (args.length === 0) super(RealDate.now() + clockSkewMs);
        else super(...args);
      }
      static now() { return RealDate.now() + clockSkewMs; }
    };
    Object.defineProperty(performance, "now", { configurable: true, value: () => realNow() + clockSkewMs });
  }
  clockSkewMs += ms;
}

// The edge half of the signed agent link (cmd/mistgate-edge/link_js.go): mgPanel.link challenge and accept, with the
// agent played by node:crypto. agentlink signs the SHA-256 digest of "domain NUL node NUL audience NUL nonce(s)", which is
// plain ECDSA-SHA256 over that preimage, in IEEE P1363 r||s form.
async function assertLinkHandshake(panel) {
  const nodeID = "nod_bridge";
  const audience = "link.example.com";
  const link = (op, args) => Promise.race([panel.link(op, args), wasmFailure]);
  const nodeKey = crypto.generateKeyPairSync("ec", { namedCurve: "P-256" });
  const issueResponse = await bridgeRequestFor(panel, "https://example.com/__edge_bridge_test__/issue-node-cert", {
    method: "POST",
    headers: { "Content-Type": "application/json", "CF-Connecting-IP": "127.0.0.1" },
    body: JSON.stringify({ nodeId: nodeID, spki: nodeKey.publicKey.export({ type: "spki", format: "der" }).toString("base64") }),
  });
  assert.equal(issueResponse.status, 200, "the test hook issues a node certificate from the panel CA");
  const issued = JSON.parse(Buffer.from(issueResponse.body).toString("utf8"));
  await globalThis.__d1.prepare("INSERT INTO node_cert (serial, node_id, ca_id, pem, not_before, not_after, issued_at) VALUES (?, ?, ?, ?, ?, ?, ?)")
    .bind(issued.serial, nodeID, issued.caId, issued.pem, issued.notBefore, issued.notAfter, issued.issuedAt).run();
  const caKey = new crypto.X509Certificate(issued.caPem).publicKey;

  const challenge = await countedRequest("link challenge", () => link("challenge", { audience }));
  assert.equal(challenge.queries.sequentialQueries, 0, "a challenge reads nothing");
  assert.ok(challenge.response.nonce instanceof Uint8Array && challenge.response.nonce.length === 32);
  const challengeFields = pbFields(challenge.response.frame);
  assert.deepEqual(challengeFields[1], Buffer.from(challenge.response.nonce), "the challenge frame carries the nonce");
  assert.equal(challengeFields[2].toString("utf8"), audience, "the challenge frame names the audience");
  const other = await link("challenge", { audience });
  assert.notDeepEqual(Buffer.from(other.nonce), Buffer.from(challenge.response.nonce), "every challenge has its own nonce");
  await assert.rejects(link("challenge", { audience: "" }), "an empty audience gets no challenge");

  const panelNonce = Buffer.from(challenge.response.nonce);
  const agentNonce = randomBytes(32);
  const authFrame = (id, key, nonce, signOver = { audience, panelNonce }) => {
    const preimage = Buffer.concat([Buffer.from(`mistgate-agent-link/1\0${id}\0${signOver.audience}\0`), signOver.panelNonce]);
    const signature = crypto.createSign("sha256").update(preimage).sign({ key, dsaEncoding: "ieee-p1363" });
    return Buffer.concat([pbField(1, Buffer.from(id)), pbField(2, nonce), pbField(3, signature)]);
  };
  const accept = (auth) => link("accept", {
    nodeId: nodeID,
    audience,
    nonce: new Uint8Array(panelNonce),
    auth: new Uint8Array(auth),
    until: Date.now() + 20_000,
  });

  const accepted = await countedRequest("link accept", () => accept(authFrame(nodeID, nodeKey.privateKey, agentNonce)));
  console.log(`D1 query count ${JSON.stringify(accepted.queries)}`);
  assert.equal(accepted.response.ok, true, "the node's own proof is accepted");
  assert.equal(accepted.queries.sequentialQueries, 1, "an accept costs exactly one sequential D1 query");
  assert.equal(accepted.queries.prepareExecutions, 1, "and one statement");
  assert.equal(accepted.response.certSerial, issued.serial);
  assert.equal(accepted.response.certNotAfterUnix, issued.notAfter);
  const signature = pbFields(accepted.response.frame)[1];
  assert.equal(signature.length, 64);
  const panelPreimage = Buffer.concat([Buffer.from(`mistgate-panel-link/1\0${nodeID}\0${audience}\0`), panelNonce, agentNonce]);
  assert.ok(
    crypto.createVerify("sha256").update(panelPreimage).verify({ key: caKey, dsaEncoding: "ieee-p1363" }, signature),
    "the accept frame verifies against the panel CA key",
  );

  const stranger = crypto.generateKeyPairSync("ec", { namedCurve: "P-256" });
  for (const [label, auth] of [
    ["a wrong key", authFrame(nodeID, stranger.privateKey, agentNonce)],
    ["a wrong node id", authFrame("nod_other", nodeKey.privateKey, agentNonce)],
    ["a 31-byte nonce", authFrame(nodeID, nodeKey.privateKey, agentNonce.subarray(0, 31))],
    ["a 33-byte nonce", authFrame(nodeID, nodeKey.privateKey, Buffer.concat([agentNonce, Buffer.from([0])]))],
    ["a signature over another audience", authFrame(nodeID, nodeKey.privateKey, agentNonce, { audience: "other.example.com", panelNonce })],
    ["a signature over another nonce", authFrame(nodeID, nodeKey.privateKey, agentNonce, { audience, panelNonce: randomBytes(32) })],
    ["garbage", Buffer.from([0xff, 0xff])],
  ]) {
    assert.deepEqual(await accept(auth), { ok: false }, `${label} is refused`);
  }
  await assert.rejects(link("accept", { nodeId: nodeID, audience }), /invalid arguments/, "accept without its bytes rejects");
  const at = Date.now();
  const opened = await link("step", {
    nodeId: nodeID,
    state: null,
    until: at + 10_000,
    event: { kind: "open", at, generation: 1, certSerial: issued.serial, certNotAfterUnix: issued.notAfter },
  });
  assert.equal(JSON.parse(opened.state).o, 1, "the open step stores the accepted generation");
  assert.deepEqual(opened.frames, [], "an open step sends no frames");
  assert.ok(opened.alarmAt > at, "the open step returns the Hello deadline");
  await opened.waitUntil;

  // A Hello on a bare node (no inbounds) costs 5 D1 calls. Its update cost is the per-isolate "updating" cache (10 s): a
  // stale cache adds one read of the active rollout, and a second one for its steps when there is a rollout. nod_bridge has
  // inbounds, so its own Hello is dominated by their reads. The clock is moved forward instead of sleeping, and each node
  // and rollout is removed afterwards, so later counts and the connected-node lists are unchanged.
  const helloCost = async (id, { advanceMs = 0, rollout = false } = {}) => {
    await globalThis.__d1.prepare("INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, 'active', 1)")
      .bind(id, id.replace("_", "-"), "hello.example.com").run();
    const key = crypto.generateKeyPairSync("ec", { namedCurve: "P-256" });
    const issuedResponse = await bridgeRequestFor(panel, "https://example.com/__edge_bridge_test__/issue-node-cert", {
      method: "POST",
      headers: { "Content-Type": "application/json", "CF-Connecting-IP": "127.0.0.1" },
      body: JSON.stringify({ nodeId: id, spki: key.publicKey.export({ type: "spki", format: "der" }).toString("base64") }),
    });
    const cert = JSON.parse(Buffer.from(issuedResponse.body).toString("utf8"));
    await globalThis.__d1.prepare("INSERT INTO node_cert (serial, node_id, ca_id, pem, not_before, not_after, issued_at) VALUES (?, ?, ?, ?, ?, ?, ?)")
      .bind(cert.serial, id, cert.caId, cert.pem, cert.notBefore, cert.notAfter, cert.issuedAt).run();
    if (rollout) {
      await globalThis.__d1.prepare("INSERT INTO update_rollout (id, status, to_version, to_built, manifest, signature, batch_size, created_at) VALUES ('rol_bridge', 'running', 'v9.9.9', 1, x'00', x'00', 1, 1)").run();
      await globalThis.__d1.prepare("INSERT INTO update_step (rollout_id, node_id, node_name, stage, state) VALUES ('rol_bridge', ?, ?, 0, 'pending')")
        .bind(id, id.replace("_", "-")).run();
    }
    try {
      const openedAt = Date.now();
      const open = await link("step", {
        nodeId: id,
        state: null,
        until: openedAt + 10_000,
        event: { kind: "open", at: openedAt, generation: 1, certSerial: cert.serial, certNotAfterUnix: cert.notAfter },
      });
      await open.waitUntil;
      advanceClock(advanceMs);
      const helloBody = Buffer.concat([
        pbField(1, Buffer.from(id)),
        Buffer.from([0x18, 1]), // Hello.api_version = fleet.APIVersion
        pbField(4, Buffer.from(`instance-${id}`)),
      ]);
      const step = await countedRequest(`link Hello ${id}`, () => link("step", {
        nodeId: id,
        state: open.state,
        until: Date.now() + 10_000,
        event: { kind: "frame", at: Date.now(), frame: new Uint8Array(pbField(10, helloBody)) }, // ConnectRequest.hello
      }));
      console.log(`D1 query count ${JSON.stringify(step.queries)}`);
      return step.queries.sequentialQueries;
    } finally {
      await globalThis.__d1.prepare("DELETE FROM update_rollout WHERE id = 'rol_bridge'").run();
      await globalThis.__d1.prepare("DELETE FROM node_cert WHERE node_id = ?").bind(id).run();
      await globalThis.__d1.prepare("DELETE FROM node WHERE id = ?").bind(id).run(); // node_live, node_sent and the facts cascade
    }
  };
  assert.equal(await helloCost("nod_hello_a", { advanceMs: 10_100 }), 6, "a Hello after the Updating cache went stale costs the 5 baseline calls and one read of the active rollout");
  assert.equal(await helloCost("nod_hello_b"), 5, "a Hello inside the 10 s cache costs the 5 baseline calls");
  assert.equal(await helloCost("nod_hello_c", { advanceMs: 10_100, rollout: true }), 7, "with an active rollout a stale cache also reads its steps");
  await assert.rejects(link("bogus", {}), /unknown operation/);

  const linkPrefix = await globalThis.__d1.prepare("SELECT v FROM setting WHERE k = 'link_prefix'").first("v");
  assert.match(linkPrefix, /^\/[a-z2-7]+\/$/, "the edge stores a secret link prefix");
  const markerURL = `https://example.com${linkPrefix}link/${nodeID}`;
  const upgrade = await panel.fetch({ method: "GET", url: markerURL, headers: [["Upgrade", "websocket"], ["Connection", "Upgrade"], ["CF-Connecting-IP", "127.0.0.1"]], body: null });
  assert.equal(upgrade.status, 204);
  assert.deepEqual(upgrade.headers.filter(([name]) => name.toLowerCase() === "x-mistgate-link"), [["X-Mistgate-Link", nodeID]]);
  const plain = await panel.fetch({ method: "GET", url: markerURL, headers: [["CF-Connecting-IP", "127.0.0.1"]], body: null });
  assert.equal(plain.status, 404, "a request without an upgrade gets no marker");

  // The node's release download, /update/<node>/<file> with the step's bearer token. Without a token it is the decoy 404 and
  // reads nothing; a token is checked against one stored digest, so a wrong one costs one D1 read and is the same 404.
  const assetURL = `https://example.com${linkPrefix}update/${nodeID}/mistgate-node-linux-amd64`;
  const assetGet = (label, extra) => countedRequest(label, () =>
    panel.fetch({ method: "GET", url: assetURL, headers: [["CF-Connecting-IP", "127.0.0.1"], ...extra], body: null }));
  const noToken = await assetGet("update download without a token", []);
  console.log(`D1 query count ${JSON.stringify(noToken.queries)}`);
  assert.equal(noToken.response.status, 404, "a download without a token is the decoy");
  assert.equal(noToken.queries.sequentialQueries, 0, "and reads nothing");
  const badToken = await assetGet("update download with a wrong token", [["Authorization", "Bearer not-the-step-token"]]);
  console.log(`D1 query count ${JSON.stringify(badToken.queries)}`);
  assert.equal(badToken.response.status, 404, "a wrong token is the decoy");
  assert.equal(badToken.queries.sequentialQueries, 1, "and costs one read of the node's step");
  assert.equal(badToken.response.headers.some(([name]) => name.toLowerCase() === "x-mistgate-asset"), false, "no asset marker for a wrong token");

  const enrollURL = `https://example.com${linkPrefix}mistgate.agent.v1.EnrollmentService/Enroll`;
  const enroll = await panel.fetch({
    method: "POST",
    url: enrollURL,
    headers: [["Content-Type", "application/json"], ["Connect-Protocol-Version", "1"], ["CF-Connecting-IP", "127.0.0.1"]],
    body: new Uint8Array(Buffer.from("{}")),
  });
  // Connect sends failed_precondition as HTTP 400; the JSON code proves the Enroll handler answered, not the decoy.
  assert.equal(enroll.status, 400, "the link enrol POST reaches EnrollmentService.Enroll");
  assert.equal(JSON.parse(Buffer.from(enroll.body).toString("utf8")).code, "failed_precondition");
  return { linkPrefix, nodeID };
}

// The Provisioner role (SSH installs on the edge). The main Worker's panel does not serve the ProvisioningService or the
// install page: behind the sign-in check it answers a marker (X-Mistgate-Forward), which the Worker turns into a request
// to the Provisioner object. The Provisioner's panel (role "serve") is the same wasm with sshConnect: here the Node
// stand-in of the Worker's socket, to the fake SSH server of scripts/fakesshd.ps1.
async function assertProvisioner(bytes, forwardPanel, adminCookie, initOptions, baseCronDate) {
  const fake = Object.fromEntries(["ADDR", "FP", "USER", "PASS"].map((name) => [name, process.env[`MG_FAKESSHD_${name}`]]));
  assert.ok(fake.ADDR && fake.FP && fake.USER && fake.PASS, "run this through scripts/test-edge-entry.ps1: it starts the fake SSH server");
  const api = "https://example.com/test-admin/api/mistgate.admin.v1.ProvisioningService/";
  const call = (panel, method, body, headers = {}) => bridgeRequestFor(panel, api + method, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Connect-Protocol-Version": "1", "CF-Connecting-IP": "127.0.0.1", Origin: "https://example.com", ...headers },
    body: JSON.stringify(body),
  });
  const page = (panel, cookie) => bridgeRequestFor(panel, "https://example.com/test-admin/nodes/install", {
    headers: { "CF-Connecting-IP": "127.0.0.1", ...(cookie ? { Cookie: cookie } : {}) },
  });
  const header = (response, name) => response.headers.find(([key]) => key.toLowerCase() === name)?.[1];
  const json = (response) => JSON.parse(Buffer.from(response.body).toString("utf8"));
  const signedIn = { Cookie: adminCookie };

  // The forwarding panel: a marker for the signed-in owner, the ordinary refusal for anyone else.
  let build = "";
  for (const method of ["GetSSHFingerprint", "CheckSSH", "StartNodeProvision", "GetNodeProvision", "ListNodeProvisions"]) {
    const marked = await call(forwardPanel, method, {}, signedIn);
    assert.equal(marked.status, 204, `${method} is marked for the Provisioner`);
    const value = header(marked, "x-mistgate-forward");
    assert.match(value, /^provision;v=\S+$/, `${method} carries the build in the marker`);
    build = value.slice("provision;v=".length);
    const anonymous = await call(forwardPanel, method, {});
    assert.notEqual(anonymous.status, 204, `${method} is not marked for a visitor who is not signed in`);
    assert.equal(header(anonymous, "x-mistgate-forward"), undefined, `${method} leaks no marker to a visitor`);
  }
  const markedPage = await page(forwardPanel, adminCookie);
  assert.equal(markedPage.status, 204, "the install page is marked for the Provisioner");
  assert.equal(header(markedPage, "x-mistgate-forward"), `provision;v=${build}`);
  const anonymousPage = await page(forwardPanel);
  assert.notEqual(anonymousPage.status, 204, "the install page is not marked for a visitor");
  assert.equal(header(anonymousPage, "x-mistgate-forward"), undefined);
  await assert.rejects(forwardPanel.provision(), /serve role/, "a forwarding panel runs no installs");

  // The cron pass wakes the Provisioner while an install is waiting, running or being cancelled, and costs one D1 query for it.
  const insertJob = (id, state) => globalThis.__d1.prepare(
    `INSERT INTO node_provision_job (id, node_id, name, address, country_code, location, provider, ssh_host, ssh_port, host_fingerprint, secret, state, phase, error_code, created_by, created_at, updated_at)
     VALUES (?, ?, ?, '198.51.100.9', '', '', '', '45.67.89.10', 22, 'SHA256:pin', X'01', ?, 'queued', '', 'adm_test', 1, 1)`,
  ).bind(id, `nod_${id}`, id.replace("_", "-"), state).run();
  const tick = async (minute) => {
    await globalThis.__d1.prepare("UPDATE job_lease SET expires_at = 0 WHERE name = 'health-eval'").run();
    const at = new Date(baseCronDate);
    at.setUTCMinutes(minute, 0, 0);
    return countedJob("cron tick with installs", () => Promise.race([forwardPanel.cron({ at: at.getTime() }), wasmFailure]));
  };
  await insertJob("prv_done", "failed");
  // 6 for the sweeps and settings, one read of the active rollout for the updates job, one Telegram pending-code check, one
  // cheburcheck and one bsbord settings read while off and 11 for a warm evaluator pass (see "cron tick" below); nothing in an install wakes more.
  assert.equal((await tick(3)).queries.sequentialQueries, 21);
  assert.equal(provisionKicks, 0, "a finished install wakes nothing");
  for (const [i, state] of ["queued", "running", "cancel_requested"].entries()) {
    await globalThis.__d1.prepare("DELETE FROM node_provision_job WHERE id = 'prv_open'").run();
    await insertJob("prv_open", state);
    assert.equal((await tick(3)).queries.sequentialQueries, 21, "waking costs no query beyond the look");
    assert.equal(provisionKicks, i + 1, `a ${state} install wakes the Provisioner`);
  }
  await globalThis.__d1.prepare("DELETE FROM node_provision_job").run();
  provisionKicks = 0;

  // The Provisioner's panel.
  const serveKicks = { n: 0 };
  const servePanel = await startIsolate(bytes, forwardPanel);
  await assert.rejects(Promise.race([
    servePanel.init({ d1: globalThis.__d1, masterKey: initOptions.masterKey, provision: { role: "serve", kick: async () => {}, sshConnect: null } }),
    wasmFailure,
  ]), /provision must be/, "a serve role without sshConnect is refused");
  await Promise.race([servePanel.init({
    d1: globalThis.__d1,
    limit: securityLimit,
    masterKey: initOptions.masterKey, // no public URL, no admin path, no assets: all of it is read from D1
    nodeLink: initOptions.nodeLink,
    provision: { role: "serve", kick: async () => { serveKicks.n++; }, sshConnect: globalThis.__sshConnect },
  }), wasmFailure]);
  const asBuild = (value) => ({ "X-Mistgate-Build": value });

  const noBuild = await call(servePanel, "ListNodeProvisions", {}, signedIn);
  assert.equal(noBuild.status, 503, "a request that names no build is not served");
  assert.match(Buffer.from(noBuild.body).toString("utf8"), /panel_updating/);
  assert.equal((await call(servePanel, "ListNodeProvisions", {}, { ...signedIn, ...asBuild("0.0.0-old") })).status, 503, "nor is one of another build");
  const listed = await call(servePanel, "ListNodeProvisions", {}, { ...signedIn, ...asBuild(build) });
  assert.equal(listed.status, 200, "the Provisioner serves the ProvisioningService for this build");
  const stranger = await call(servePanel, "ListNodeProvisions", {}, asBuild(build));
  assert.equal(stranger.status, 401, "and checks the sign-in again");
  assert.equal((await page(servePanel, adminCookie)).status, 503, "the page needs the build too");
  const servedPage = await bridgeRequestFor(servePanel, "https://example.com/test-admin/nodes/install", {
    headers: { "CF-Connecting-IP": "127.0.0.1", Cookie: adminCookie, ...asBuild(build) },
  });
  assert.equal(servedPage.status, 200, "the install page is rendered by the Provisioner");
  assert.ok(!header(servedPage, "x-mistgate-forward"));

  // Through the real stack: host key over a socket, the platform's refusal, and a job each way a login can go.
  const asOwner = { ...signedIn, ...asBuild(build) };
  const fingerprint = await call(servePanel, "GetSSHFingerprint", { host: "45.67.89.10", port: 22 }, asOwner);
  assert.equal(fingerprint.status, 200);
  assert.equal(json(fingerprint).fingerprint, fake.FP, "the host key arrives through sshConnect");
  const refused = await call(servePanel, "GetSSHFingerprint", { host: "45.67.89.11", port: 22 }, asOwner);
  assert.equal(json(refused).message, "ssh_target_refused", "the platform's refusal has its own code");

  const rightPassword = fake.PASS;
  const wrongPassword = `wrong-${randomBytes(8).toString("hex")}`;
  const jobs = [
    ["right-login", "45.67.89.10", rightPassword, "ssh_preflight_failed"], // logs in; the fake server has no preflight script
    ["wrong-login", "45.67.89.10", wrongPassword, "ssh_authentication_failed"],
    ["no-route", "45.67.89.11", rightPassword, "ssh_target_refused"],
  ];
  for (const [index, [name, host, password]] of jobs.entries()) {
    const started = await call(servePanel, "StartNodeProvision", {
      confirmInstall: true, name, address: `198.51.100.${20 + index}`, countryCode: "NL", sshHost: host, sshPort: 22,
      fingerprint: fake.FP, password, sshUsername: fake.USER,
    }, asOwner);
    assert.equal(started.status, 200, `StartNodeProvision ${name}: ${Buffer.from(started.body).toString("utf8")}`);
  }
  assert.equal(serveKicks.n, jobs.length, "each queued install wakes the Provisioner");

  const passes = [];
  for (let i = 0; i < jobs.length; i++) {
    const pass = await Promise.race([servePanel.provision(), wasmFailure]);
    await pass.waitUntil;
    passes.push([pass.more, pass.busy]);
  }
  assert.deepEqual(passes, [[true, false], [true, false], [false, false]], "a pass runs one install and says whether another waits");
  const rows = (await globalThis.__d1.prepare("SELECT name, state, error_code, length(secret) AS secret_bytes FROM node_provision_job").all()).results;
  for (const [name, , , code] of jobs) {
    const row = rows.find((r) => r.name === name);
    assert.ok(row, `job ${name}`);
    assert.equal(row.state, "failed", `${name} ended`);
    assert.equal(row.error_code, code, `${name} ended with its own code`);
    assert.equal(Number(row.secret_bytes), 0, `${name}: the sealed login is erased when the job ends`);
  }
  const idle = await servePanel.provision();
  assert.deepEqual([idle.more, idle.busy], [false, false], "a pass with nothing queued does nothing");
  await idle.waitUntil;

  // Nothing the job stored or wrote down holds a password.
  const stored = JSON.stringify([
    (await globalThis.__d1.prepare("SELECT * FROM node_provision_job").all()).results,
    (await globalThis.__d1.prepare("SELECT * FROM node_provision_event").all()).results,
  ]);
  for (const password of [rightPassword, wrongPassword]) assert.ok(!stored.includes(password), "a login password was stored");
  await globalThis.__d1.prepare("DELETE FROM node_provision_event").run();
  await globalThis.__d1.prepare("DELETE FROM node_provision_job").run();
  return servePanel;
}

async function run() {
  limitMath = await import(pathToFileURL(path.resolve(__dirname, "../../../edge/worker/src/limitmath.ts")).href);
  await assertSharedLimitVectors();
  const port = await reservePort();
  const masterKey = new Uint8Array(randomBytes(32));
  const oracle = startOracle(oraclePath, database, port, masterKey);
  try {
    await oracle.ready;

    const bytes = fs.readFileSync(wasmPath);
    wasmFailure = new Promise((_, reject) => { rejectWasmFailure = reject; });
    const firstPanel = await startIsolate(bytes, null);

    const logs = [];
    const consoleLog = console.log;
    console.log = (...args) => logs.push(args);
    let firstInstance;
    let secondPanel;
    let secondInstance;
    let thirdPanel;
    const assetCalls = [];
    const assetFiles = {
      "index.html": Buffer.from('<!doctype html><base href="/"><title>asset-fixture</title>'),
      "assets/app-1a2b3c.js": Buffer.from("console.log(1)"),
    };
    const initOptions = {
      d1: globalThis.__d1,
      limit: securityLimit,
      assets: async (name) => {
        assetCalls.push(name);
        const file = assetFiles[name];
        return file ? new Uint8Array(file) : null;
      },
      masterKey,
      publicURL: "https://example.com",
      adminPrefix: "/test-admin/",
      subPrefix: "/test-sub/",
      provision: { role: "forward", kick: async () => { provisionKicks++; } },
      nodeLink: {
        ask: async () => ({ error: "lost" }),
        send: async () => false,
        close: async () => {},
        poke: async (nodeIDs) => {
          nodePokeCalls++;
          nodePokeIDs.push([...nodeIDs]);
          return 0;
        },
      },
    };
    try {
      await assert.rejects(Promise.race([
        firstPanel.init({ ...initOptions, publicURL: "https://192.0.2.10" }), // WebAuthn refuses an IP-address RP ID
        wasmFailure,
      ]), "invalid WebAuthn RP settings must fail before first-run settings are stored");
      // The VPS oracle shares this database; its own start writes the subscription-rules markers (subsettings.MigrateM3, MigrateSingbox)
      // and the link device limit mode (subsettings.MigrateLinkDeviceLimit).
      const settingsAfterFailure = await globalThis.__d1.prepare("SELECT count(*) AS n FROM setting WHERE k NOT IN ('sub_rules_m3', 'sub_rules_singbox', 'link_device_limit')").first("n");
      assert.equal(Number(settingsAfterFailure), 0, "failed first init leaves no settings behind");

      // A setup token hash from the deployer (SETUP_TOKEN_SHA256): the panel offers it, logs only its expiry, and never
      // revives an expired one. The database is emptied again afterwards, so the random-token flow below starts clean.
      const scenarioStart = logs.length;
      const deployerToken = randomBytes(32).toString("hex");
      const deployerHash = crypto.createHash("sha256").update(deployerToken).digest("hex");
      const setupIP = "198.51.100.90";
      const beginWith = (panel, token) => connectRPC(panel, "AuthService/BeginSetup", {
        setupToken: token, displayName: "Deployer Owner", method: "SETUP_METHOD_PASSWORD", login: "deployer", password: "test-password-1234",
      }, "", setupIP);
      const hashPanel = await startIsolate(bytes, firstPanel);
      await assert.rejects(Promise.race([
        hashPanel.init({ ...initOptions, setupTokenSHA256: deployerHash.toUpperCase() }),
        wasmFailure,
      ]), /setupTokenSHA256/, "an uppercase or malformed hash is refused");
      assert.equal(Number(await globalThis.__d1.prepare("SELECT count(*) AS n FROM setting WHERE k NOT IN ('sub_rules_m3', 'sub_rules_singbox', 'link_device_limit')").first("n")), 0,
        "a refused hash leaves no settings behind");
      let mark = logs.length;
      await Promise.race([hashPanel.init({ ...initOptions, setupTokenSHA256: deployerHash }), wasmFailure]);
      const offerLogs = logs.slice(mark).map((args) => args.map(String).join(" "));
      assert.ok(offerLogs.some((line) => line.startsWith("No admin yet.") && line.includes("accepted until")), "the first start logs the expiry of the offered token");
      for (const line of offerLogs) {
        assert.ok(!line.includes(deployerToken) && !line.includes(deployerHash) && !line.includes("/setup#") && !line.includes("test-admin"),
          "the log shows neither the token, nor its hash, nor an address");
      }
      const offered = await globalThis.__d1.prepare("SELECT lower(hex(hash)) AS hash, expires_at FROM setup_token").all();
      assert.deepEqual(offered.results.map((row) => row.hash), [deployerHash], "the panel stored the offered hash and minted no token of its own");
      const stranger = await beginWith(hashPanel, randomBytes(32).toString("hex"));
      assert.notEqual(stranger.response.status, 200, "a token nobody offered is refused");
      const offeredBegin = await beginWith(hashPanel, deployerToken);
      assert.equal(offeredBegin.response.status, 200, "BeginSetup accepts the token the deployer chose");

      // An expired token is not revived by the next isolate's start.
      await globalThis.__d1.prepare("UPDATE setup_token SET expires_at = ?").bind(Math.floor(Date.now() / 1000) - 3600).run();
      const expiredPanel = await startIsolate(bytes, hashPanel);
      mark = logs.length;
      await Promise.race([expiredPanel.init({ ...initOptions, setupTokenSHA256: deployerHash }), wasmFailure]);
      assert.ok(logs.slice(mark).some((args) => String(args[0]).startsWith("No admin yet.") && String(args[0]).includes("expired")), "the next start says the token expired");
      const afterExpiry = await globalThis.__d1.prepare("SELECT count(*) AS n, min(expires_at) AS expires_at FROM setup_token").first();
      assert.equal(Number(afterExpiry.n), 1, "the expired token is still one row");
      assert.ok(Number(afterExpiry.expires_at) < Math.floor(Date.now() / 1000), "the expired token was not given a new expiry");
      const revived = await beginWith(expiredPanel, deployerToken);
      assert.notEqual(revived.response.status, 200, "an expired token is not revived");
      for (const line of logs.slice(scenarioStart).map((args) => args.map(String).join(" "))) {
        assert.ok(!line.includes(deployerToken) && !line.includes(deployerHash), "no log line shows the token or its hash");
      }
      logs.length = 0;
      for (const table of ["setup_token", "auth_ceremony"]) await globalThis.__d1.prepare(`DELETE FROM ${table}`).run();
      await globalThis.__d1.prepare("DELETE FROM setting WHERE k NOT IN ('sub_rules_m3', 'sub_rules_singbox', 'link_device_limit')").run();

      firstInstance = await Promise.race([firstPanel.init(initOptions), wasmFailure]);
      secondPanel = await startIsolate(bytes, expiredPanel);
      secondInstance = await Promise.race([secondPanel.init(initOptions), wasmFailure]);
    } finally {
      console.log = consoleLog;
    }
    assert.equal(firstInstance, firstPanel, "first isolate init returns the panel API");
    assert.equal(secondInstance, secondPanel, "fresh isolate init returns the panel API");
    // Two wasm instances in this Node process model separate Worker isolates with shared D1 and limiter callbacks.
    const setupMessages = logs.map((args) => String(args[0])).filter((message) => message.startsWith("No admin yet."));
    const newLinks = setupMessages.filter((message) => message.includes("Create one") && message.includes("/setup#"));
    const existingLinks = setupMessages.filter((message) => message.includes("already issued"));
    if (newLinks.length !== 1 || existingLinks.length !== 1 || !existingLinks[0].includes("expires at")) {
      throw new Error("a second isolate must log the existing setup link expiry without issuing another link");
    }
    const setupMatch = newLinks[0].match(/\/setup#([A-Za-z0-9_-]+)/);
    if (!setupMatch || existingLinks[0].includes(setupMatch[1])) throw new Error("setup logs did not keep the token private to its first link");

    const setupPage = await bridgeRequestFor(firstPanel, "https://example.com/test-admin/setup", {
      headers: { "CF-Connecting-IP": "127.0.0.1" },
    });
    assert.equal(setupPage.status, 200, "the fresh setup page is served");
    assert.ok(setupPage.headers.some(([name, value]) => name.toLowerCase() === "content-type" && value.includes("text/html")));
    // The SPA comes from the assets callback: a client route gets index.html with <base> rewritten to the admin path.
    assert.ok(Buffer.from(setupPage.body).toString("utf8").includes('<base href="/test-admin/">'), "SPA fallback serves the asset index.html");
    const jsAsset = await bridgeRequestFor(firstPanel, "https://example.com/test-admin/assets/app-1a2b3c.js", {
      headers: { "CF-Connecting-IP": "127.0.0.1" },
    });
    assert.equal(jsAsset.status, 200, "a hashed asset is read through the callback");
    assert.ok(jsAsset.headers.some(([name, value]) => name.toLowerCase() === "content-type" && value.includes("javascript")));
    assert.equal(Buffer.from(jsAsset.body).toString("utf8"), "console.log(1)");
    const missingAsset = await bridgeRequestFor(firstPanel, "https://example.com/test-admin/assets/gone.js", {
      headers: { "CF-Connecting-IP": "127.0.0.1" },
    });
    assert.equal(missingAsset.status, 404, "a missing asset is a 404, not the app shell");
    const callsBefore = assetCalls.length;
    await bridgeRequestFor(firstPanel, "https://example.com/test-admin/assets/app-1a2b3c.js", {
      headers: { "CF-Connecting-IP": "127.0.0.1" },
    });
    assert.equal(assetCalls.length, callsBefore, "an asset that was read once is served from the isolate cache");

    const rpcRequest = new Request("https://example.com/test-admin/api/mistgate.admin.v1.AuthService/GetLoginInfo", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Connect-Protocol-Version": "1",
        "CF-Connecting-IP": "127.0.0.1",
      },
      body: "{}",
    });
    const rpcBody = Buffer.from(await rpcRequest.clone().arrayBuffer());
    const rpcEdge = await bridgeRequest(rpcRequest.url, rpcRequest);
    const rpcVPS = await oracleRequest(port, {
      method: rpcRequest.method,
      url: rpcRequest.url,
      headers: Array.from(rpcRequest.headers.entries()),
      body: rpcBody,
    });
    assert.equal(rpcEdge.status, 200, "the unauthenticated setup-state RPC succeeds");
    // protojson adds a random space after separators, chosen from the binary's own hash: the two builds differ in bytes
    // and agree in content, which is what is compared.
    assert.deepEqual(
      JSON.parse(Buffer.from(rpcEdge.body).toString("utf8")),
      JSON.parse(rpcVPS.body.toString("utf8")),
      "edge setup-state RPC differs from the shared VPS handler",
    );
    assert.equal(JSON.parse(Buffer.from(rpcEdge.body).toString("utf8")).setupOpen, true);

    const unknownToken = `unknown-${randomBytes(16).toString("hex")}`;
    const subscriptionURL = `https://example.com/test-sub/${unknownToken}`;
    const subscriptionEdge = await bridgeRequest(subscriptionURL, {
      headers: { "CF-Connecting-IP": "127.0.0.1" },
    });
    const subscriptionVPS = await oracleRequest(port, {
      method: "GET", url: subscriptionURL,
      headers: [["cf-connecting-ip", "127.0.0.1"]],
    });
    if (subscriptionEdge.status !== subscriptionVPS.status || !Buffer.from(subscriptionEdge.body).equals(subscriptionVPS.body)) {
      throw new Error("unknown subscription response differs from the shared VPS handler");
    }

    const unknownPath = "https://example.com/not-a-panel-route";
    const unknownEdge = await bridgeRequest(unknownPath, { headers: { "CF-Connecting-IP": "127.0.0.1" } });
    const unknownVPS = await oracleRequest(port, {
      method: "GET", url: unknownPath,
      headers: [["cf-connecting-ip", "127.0.0.1"]],
    });
    if (unknownEdge.status !== unknownVPS.status || !Buffer.from(unknownEdge.body).equals(unknownVPS.body)) {
      throw new Error("unknown path response differs from the shared VPS handler");
    }

    const unknownAdminURL = "https://example.com/test-admin/not-a-panel-route";
    const unknownAdmin = await bridgeRequest(unknownAdminURL, {
      headers: { "CF-Connecting-IP": "127.0.0.1" },
    });
    const edgeMCP = await bridgeRequest("https://example.com/test-admin/mcp", {
      headers: { "CF-Connecting-IP": "127.0.0.1" },
    });
    if (edgeMCP.status !== unknownAdmin.status || !Buffer.from(edgeMCP.body).equals(Buffer.from(unknownAdmin.body))) {
      throw new Error("edge MCP path does not fall through like an unknown admin path");
    }

    const cookieResponse = await bridgeRequest("https://example.com/__edge_bridge_test__/cookies", {
      headers: { "CF-Connecting-IP": "127.0.0.1" },
    });
    const cookies = cookieResponse.headers
      .filter(([name]) => name.toLowerCase() === "set-cookie")
      .map(([, value]) => value);
    assert.equal(cookieResponse.status, 201);
    assert.deepEqual(cookies, ["first=one; Path=/", "second=two; Path=/"], "both Set-Cookie values survive the bridge");
    assert.equal(Buffer.from(cookieResponse.body).toString("utf8"), "cookie-test");

    const multiHeaderResponse = await Promise.race([mgPanel.fetch({
      method: "GET",
      url: "https://example.com/__edge_bridge_test__/headers",
      headers: [["X-Multi", "one"], ["X-Multi", "two"], ["CF-Connecting-IP", "127.0.0.1"]],
      body: null,
    }), wasmFailure]);
    assert.deepEqual(
      multiHeaderResponse.headers.filter(([name]) => name.toLowerCase() === "x-multi-response").map(([, value]) => value),
      ["one", "two"],
      "repeated request and response headers survive the bridge",
    );

    const streamResponse = await bridgeRequest("https://example.com/__edge_bridge_test__/stream", {
      headers: { "CF-Connecting-IP": "127.0.0.1" },
    });
    assert.equal(streamResponse.status, 501);
    assert.ok(Buffer.from(streamResponse.body).toString("utf8").includes("server-streaming responses are not supported"));

    const setupBegin = await connectRPC(firstPanel, "AuthService/BeginSetup", {
      setupToken: setupMatch[1],
      displayName: "Edge Test Owner",
      method: "SETUP_METHOD_PASSWORD",
      login: "owner",
      password: "test-password-1234",
    });
    assert.equal(setupBegin.response.status, 200, "the first setup link remains valid in the first isolate");
    const setupFinish = await connectRPC(secondPanel, "AuthService/FinishSetup", {
      setupToken: setupMatch[1],
      ceremonyId: setupBegin.message.ceremonyId,
      totpCode: totpCode(setupBegin.message.totpSecret),
    });
    assert.equal(setupFinish.response.status, 200, "setup creates the owner through the shared D1 database");
    const setCookie = setupFinish.response.headers.find(([name]) => name.toLowerCase() === "set-cookie")?.[1] || "";
    const adminCookie = setCookie.split(";", 1)[0];
    assert.ok(adminCookie.startsWith("__Host-sid="), "setup returns an admin session cookie");

    const backup = await connectRPC(secondPanel, "BackupService/CreateBackup", {}, adminCookie);
    assert.equal(backup.message.code, "failed_precondition", "filesystem backup RPC fails cleanly on the edge");
    assert.ok(backup.message.message.includes("unavailable_in_edition: feature=backups"), "the refusal is the coded edition reason");

    const rescan = await connectRPC(secondPanel, "UpdateService/RescanBundle", {}, adminCookie);
    assert.equal(rescan.response.status, 200, "RescanBundle refetches the trusted release on the edge");
    // The test build is 0.1.0-dev, which is no release tag: the edge has nothing to fetch and says so with the coded key.
    assert.equal(rescan.message.bundle.errorKey, "updates.bundle.err.no_release", "a dev build has no release to refetch");

    const me = await connectRPC(secondPanel, "AuthService/Me", {}, adminCookie);
    assert.equal(me.message.edition, "edge", "Me names the edge edition");
    assert.ok(me.message.unavailable.includes("backups") && me.message.unavailable.includes("mcp"), "Me lists the features the edge lacks");
    assert.ok(!me.message.unavailable.includes("agent_updates"), "agent updates are available on the edge");
    assert.ok(!me.message.unavailable.includes("provision"), "SSH installs run on the edge: Me does not list them as unavailable");

    const tokens = await connectRPC(secondPanel, "ApiTokenService/ListApiTokens", {}, adminCookie);
    assert.equal(tokens.response.status, 200, "the API-token admin RPC stays available on the edge");
    assert.ok(tokens.message.nowUnix, "the API-token RPC returns its current time");

    // The Reachability page's history on D1: every source off says nothing; with the block lists on, their checks of the week
    // come back as a lane, consecutive equal verdicts as one point.
    const quiet = await connectRPC(secondPanel, "CountryReachabilityService/GetReachabilityHistory", {}, adminCookie);
    assert.equal(quiet.response.status, 200, "the history RPC answers on the edge");
    assert.equal(quiet.message.country, "RU", "the history names the country");
    assert.ok(!quiet.message.sources && !quiet.message.nodes, "every source off: no sources, no nodes");
    const nowS = Math.floor(Date.now() / 1000);
    const hour = (n) => nowS - n * 3600;
    await globalThis.__d1.prepare("INSERT INTO setting (k, v) VALUES ('blocklist.enabled', '1')").run();
    for (const [at, listed] of [[hour(30), 0], [hour(24), 1], [hour(18), 1], [hour(24 * 9), 1]]) {
      await globalThis.__d1.prepare("INSERT INTO country_blocklist_check (node_id, at, in_rkn, rkn_subnet, listed_by, cdn_provider, cdn_region, lists_at) VALUES (?, ?, ?, '', ?, '', '', 0)")
        .bind("nod_hist", at, listed, listed ? "antifilter_ipsum" : "").run();
    }
    const week = await connectRPC(secondPanel, "CountryReachabilityService/GetReachabilityHistory", { days: 7 }, adminCookie);
    assert.deepEqual(week.message.sources, ["lists"], "the block lists are the one source on");
    assert.deepEqual(week.message.nodes?.[0]?.lanes?.[0]?.points?.map((p) => [Number(p.atUnix), p.verdict]),
      [[hour(30), "COUNTRY_VERDICT_OK"], [hour(24), "COUNTRY_VERDICT_LISTED"]], "the lists' lane, compacted, without the check older than the week");
    await globalThis.__d1.prepare("DELETE FROM setting WHERE k = 'blocklist.enabled'").run();
    await globalThis.__d1.prepare("DELETE FROM country_blocklist_check").run();

    // Seed the node fixture like the Go subscription fixtures, then create the profiles and access rows through the admin API.
    await globalThis.__d1.prepare("INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, ?, ?)")
      .bind("nod_bridge", "node de", "de.example.com", "active", 1).run();
    const hysteriaProfile = await connectRPC(secondPanel, "ProfileService/CreateProfile", {
      protocol: "hysteria2", name: "bridge-hysteria2",
    }, adminCookie);
    assert.equal(hysteriaProfile.response.status, 200, "the admin API creates a Hysteria2 profile");
    const hysteriaInbound = await connectRPC(secondPanel, "ProfileService/CreateInbound", {
      profileId: hysteriaProfile.message.profile.id, nodeId: "nod_bridge",
    }, adminCookie);
    assert.equal(hysteriaInbound.response.status, 200, "the admin API deploys the Hysteria2 profile");
    const awgProfile = await connectRPC(secondPanel, "ProfileService/CreateProfile", {
      protocol: "awg", name: "bridge-awg",
    }, adminCookie);
    assert.equal(awgProfile.response.status, 200, "the admin API creates an AmneziaWG profile");
    const awgInbound = await connectRPC(secondPanel, "ProfileService/CreateInbound", {
      profileId: awgProfile.message.profile.id, nodeId: "nod_bridge",
    }, adminCookie);
    assert.equal(awgInbound.response.status, 200, "the admin API deploys the AmneziaWG profile");
    const group = await connectRPC(secondPanel, "GroupService/CreateGroup", {
      name: "bridge-group", profileIds: [hysteriaProfile.message.profile.id, awgProfile.message.profile.id],
    }, adminCookie);
    assert.equal(group.response.status, 200, "the admin API creates a group with both profiles");
    const createdUser = await connectRPC(secondPanel, "UserService/CreateUser", {
      name: "bridge-user", groupId: group.message.group.id,
    }, adminCookie);
    assert.equal(createdUser.response.status, 200, "the admin API creates a subscription user");
    const userSubURL = createdUser.message.subscriptionUrl;
    const userToken = userSubURL.slice(userSubURL.lastIndexOf("/") + 1);
    const pagePassword = createdUser.message.pagePassword;
    assert.ok(userToken && pagePassword, "the admin API returns a link credential and page password");

    // Remove the implicit device's current credentials so this subscription fetch creates them lazily and notifies the
    // fleet. Wait for earlier admin-request background work before measuring this one request.
    const backgroundBarrier = await bridgeRequestFor(secondPanel, "https://example.com/test-sub/brand/logo.svg");
    await backgroundBarrier.waitUntil;
    await globalThis.__d1.prepare("DELETE FROM device_credential WHERE user_id = ? AND revoked_at IS NULL")
      .bind(createdUser.message.user.id).run();
    await globalThis.__d1.prepare("UPDATE device SET last_seen_at = ? WHERE user_id = ? AND hwid_hash IS NULL")
      .bind(1, createdUser.message.user.id).run();
    liveProjectionQueries = 0;
    nodePokeCalls = 0;
    nodePokeIDs.length = 0;
    const touchResponse = await bridgeRequestFor(secondPanel, userSubURL, {
      headers: { "CF-Connecting-IP": "127.0.0.1", "User-Agent": "Happ/4.10.2/ios" },
    });
    assert.equal(touchResponse.status, 200, "the edge subscription fetch succeeds");
    assert.ok(touchResponse.waitUntil && typeof touchResponse.waitUntil.then === "function", "the edge response exposes its background work promise");
    assert.equal(liveProjectionQueries, 0, "the fetch performs no Live read before the response");
    assert.equal(nodePokeCalls, 0, "the fetch performs no poke before the response");
    await touchResponse.waitUntil;
    assert.equal(liveProjectionQueries, 1, "the fetch waitUntil performs one fan-out Live query");
    assert.equal(nodePokeCalls, 1, "the fetch waitUntil makes one fan-out poke call");
    assert.deepEqual(nodePokeIDs, [[]], "the fan-out only includes connected nodes");
    const touchedAt = await globalThis.__d1.prepare("SELECT last_seen_at FROM device WHERE user_id = ? AND hwid_hash IS NULL")
      .bind(createdUser.message.user.id).first("last_seen_at");
    assert.ok(Number(touchedAt) > 1, "waiting on the response promise observes the device touch in D1");

    const awgDevice = await connectRPC(secondPanel, "DeviceService/CreateAwgDevice", {
      userId: createdUser.message.user.id, profileId: awgProfile.message.profile.id,
      platform: "linux", label: "bridge-device",
    }, adminCookie);
    assert.equal(awgDevice.response.status, 200, "the admin API creates an AWG device");
    const awgDeviceID = awgDevice.message.device.id;

    // The VPS oracle embeds the repo's web/dist when it is built; the parity panels read that same directory, so a
    // built sub.html (or none) is on both sides of every comparison.
    const repoDist = path.resolve(__dirname, "../../../web/dist");
    const distAssets = async (name) => {
      const file = path.resolve(repoDist, name);
      if (!file.startsWith(repoDist + path.sep)) return null;
      try {
        return new Uint8Array(fs.readFileSync(file));
      } catch {
        return null;
      }
    };
    let previousPanel = secondPanel;
    async function freshPanel() {
      const panel = await startIsolate(bytes, previousPanel);
      previousPanel = panel;
      await Promise.race([panel.init({ ...initOptions, assets: distAssets }), wasmFailure]);
      return panel;
    }

    // These requests use fresh isolates so isolate-local response, settings, and brand caches do not hide cold-fetch work.
    // No body masks are applied: both handlers read the same persisted rows and use the same master key.
    for (const [label, ua] of [
      ["Happ/4.10.2/ios", "Happ/4.10.2/ios"],
      ["Happ/2.1.0/Android", "Happ/2.1.0/Android"],
      ["mihomo/1.19.31", "mihomo/1.19.31"],
    ]) {
      const panel = await freshPanel();
      const { edge } = await compareHandlerBytes(label, panel, port, userSubURL, {
        headers: { "CF-Connecting-IP": "127.0.0.1", "User-Agent": ua },
      });
      assert.equal(edge.status, 200, `${label} subscription succeeds`);
    }

    const browserUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36";
    const browserIP = "198.51.100.80";
    const lockedPanel = await freshPanel();
    const lockedPage = await compareHandlerBytes("browser page HTML (locked)", lockedPanel, port, userSubURL, {
      headers: { "CF-Connecting-IP": browserIP, "User-Agent": browserUA, "Accept-Language": "en-US,en;q=0.9" },
    });
    assert.equal(lockedPage.edge.status, 200, "the browser receives the locked user page");

    const unlockURL = `${userSubURL}/unlock`;
    async function unlock(panel, password, ip) {
      return bridgeRequestFor(panel, unlockURL, {
        method: "POST",
        headers: {
          "CF-Connecting-IP": ip,
          "Content-Type": "application/json",
          Origin: "https://example.com",
          "User-Agent": browserUA,
        },
        body: JSON.stringify({ password }),
      });
    }
    for (let i = 0; i < 5; i++) {
      const wrong = await unlock(lockedPanel, "wrong-password", "127.0.0.1");
      assert.equal(wrong.status, 401, `wrong page password attempt ${i + 1} is refused`);
    }
    const refused = await unlock(lockedPanel, "wrong-password", "127.0.0.1");
    assert.equal(refused.status, 429, "the limiter callback refuses the next page-password attempt");
    await securityLimit({ operation: "reset", name: "page-password", key: `t:${userToken}` });

    const unlockedPanel = await freshPanel();
    const unlockPair = await compareHandlerBytes("page-password unlock", unlockedPanel, port, unlockURL, {
      method: "POST",
      headers: {
        "CF-Connecting-IP": browserIP,
        "Content-Type": "application/json",
        Origin: "https://example.com",
        "User-Agent": browserUA,
      },
      body: JSON.stringify({ password: pagePassword }),
    });
    assert.equal(unlockPair.edge.status, 200, "the correct page password unlocks the subscription");
    const edgeCookieHeader = unlockPair.edge.headers.find(([name]) => name.toLowerCase() === "set-cookie")?.[1] || "";
    const oracleCookies = unlockPair.vps.headers["set-cookie"] || [];
    const edgeCookie = edgeCookieHeader.split(";", 1)[0];
    assert.ok(edgeCookie, "the edge unlock response sets a page cookie");
    assert.deepEqual(oracleCookies, [edgeCookieHeader], "the VPS unlock cookie matches the edge cookie");

    const pagePair = await compareHandlerBytes("browser page HTML (unlocked)", unlockedPanel, port, userSubURL, {
      headers: {
        "CF-Connecting-IP": browserIP,
        "User-Agent": browserUA,
        "Accept-Language": "en-US,en;q=0.9",
        Cookie: edgeCookie,
      },
    });
    assert.equal(pagePair.edge.status, 200, "the unlocked browser page succeeds");

    const configURL = `${userSubURL}/devices/${awgDeviceID}/configs`;
    const configPair = await compareHandlerBytes("AWG .conf device configs", unlockedPanel, port, configURL, {
      method: "POST",
      headers: {
        "CF-Connecting-IP": browserIP,
        "Content-Type": "application/json",
        Origin: "https://example.com",
        "User-Agent": browserUA,
        Cookie: edgeCookie,
      },
      body: "{}",
    });
    assert.equal(configPair.edge.status, 200, "the AWG device config endpoint succeeds");

    // The subscription miss lockout lives in the shared limiter, not in an isolate: misses on one isolate lock the client
    // network for a fresh one. Edge only: the VPS oracle sees every request from its loopback peer, so it cannot tell
    // client networks apart. It runs after the cold fetches above so they still measure the first fetch of the user.
    async function edgeOnly(label, panel, url, init) {
      const { response, queries } = await countedRequest(label, () => bridgeRequestFor(panel, url, init));
      assertQueryBudget(queries);
      return response;
    }
    const missClientIP = "198.51.100.81";
    const missUA = "Happ/4.10.2/ios";
    const missPanel = await freshPanel();
    let missResponse;
    for (let i = 0; i < 20; i++) {
      const missURL = `https://example.com/test-sub/unknown-${randomBytes(16).toString("hex")}`;
      const miss = await edgeOnly(`subscription miss ${i + 1}/20`, missPanel, missURL, {
        headers: { "CF-Connecting-IP": missClientIP, "User-Agent": missUA },
      });
      assert.equal(miss.status, 404, `unknown subscription token ${i + 1} receives the decoy`);
      missResponse = miss.body;
    }
    const lockedValid = await edgeOnly("valid subscription from a locked miss network", await freshPanel(), userSubURL, {
      headers: { "CF-Connecting-IP": missClientIP, "User-Agent": missUA },
    });
    assert.equal(lockedValid.status, 404, "the locked network receives the decoy for a valid token");
    assert.deepEqual(Buffer.from(lockedValid.body), Buffer.from(missResponse), "the locked valid token matches the decoy response");

    const otherClient = await edgeOnly("valid subscription from an unaffected client network", await freshPanel(), userSubURL, {
      headers: { "CF-Connecting-IP": "198.51.100.82", "User-Agent": missUA },
    });
    assert.equal(otherClient.status, 200, "the unaffected client receives the valid subscription");
    assert.notDeepEqual(Buffer.from(otherClient.body), Buffer.from(missResponse), "the unaffected client does not receive the decoy");

    // This passkey ceremony carries a WebAuthn SessionData challenge across the two wasm instances.
    const crossIsolateIP = "198.51.100.72";
    const crossIsolateBegin = await connectRPC(firstPanel, "AuthService/BeginLogin", {}, "", crossIsolateIP);
    assert.equal(crossIsolateBegin.response.status, 200, "the first isolate starts a WebAuthn ceremony");
    const crossIsolateFinish = await connectRPC(secondPanel, "AuthService/FinishLogin", {
      ceremonyId: crossIsolateBegin.message.ceremonyId,
      credentialJson: "{}",
    }, "", crossIsolateIP);
    assert.equal(crossIsolateFinish.message.code, "unauthenticated", "the second isolate reads the stored challenge and rejects the invalid assertion");

    const loginIP = "198.51.100.71";
    for (let i = 0; i < 5; i++) {
      const begin = await connectRPC(secondPanel, "AuthService/BeginLogin", {}, "", loginIP);
      assert.equal(begin.response.status, 200, `login limiter burst request ${i + 1} is allowed`);
      const finish = await connectRPC(secondPanel, "AuthService/FinishLogin", {
        ceremonyId: begin.message.ceremonyId,
        credentialJson: "{}",
      }, "", loginIP);
      assert.equal(finish.message.code, "unauthenticated", "invalid assertion consumes its ceremony");
    }
    const limitedLogin = await connectRPC(secondPanel, "AuthService/BeginLogin", {}, "", loginIP);
    assert.equal(limitedLogin.message.code, "resource_exhausted", "the callback refuses login after its burst");

    const { linkPrefix, nodeID } = await assertLinkHandshake(secondPanel);

    const baseCronDate = new Date();
    // This node is deliberately disconnected for the fleet fan-out assertion but still inside the blip window, so the
    // evaluator pass has no alert transition to add a variable Telegram node-name lookup.
    await globalThis.__d1.prepare("UPDATE node SET last_seen_at = ?, last_connected_at = ? WHERE id = 'nod_bridge'")
      .bind(nowS, nowS).run();
    const normalCronDate = new Date(baseCronDate);
    normalCronDate.setUTCMinutes(3, 0, 0);
    const { queries: normalCronQueries } = await countedJob("cron normal tick", () =>
      Promise.race([secondPanel.cron({ at: normalCronDate.getTime() }), wasmFailure]),
    );
    console.log(`D1 query count ${JSON.stringify(normalCronQueries)}`);
    assert.equal(
      normalCronQueries.sequentialQueries,
      24,
      "a normal cron tick costs 24 D1 calls: 6 for the sweeps and the country settings, one read of the active rollout for the updates job, one Telegram pending-code check, one cheburcheck and one bsbord settings read while off, and 14 for the isolate's first evaluator pass: its lease, nodes, inbounds, live nodes, doctor results and acceptances, the signal batch, port checks, active alerts, the interval setting, the update rollout, the country settings, the node-summary write and one batched external-source settings read",
    );

    liveProjectionQueries = 0;
    nodePokeCalls = 0;
    nodePokeIDs.length = 0;
    await globalThis.__d1.prepare("UPDATE job_lease SET expires_at = 0 WHERE name = 'health-eval'").run();
    const cronDate = new Date(baseCronDate);
    cronDate.setUTCMinutes(10, 0, 0);
    const { output: cron, queries: cronQueries } = await countedJob("cron tick", () =>
      Promise.race([secondPanel.cron({ at: cronDate.getTime() }), wasmFailure]),
    );
    console.log(`D1 query count ${JSON.stringify(cronQueries)}`);
    assert.equal(
      cronQueries.sequentialQueries,
      23,
      "one minute 10 cron tick costs 23 D1 calls: the 7 of the sweeps, the country settings and the fan-out live read, one read of the active rollout for the updates job, one Telegram pending-code check, one Telegram bot check for the watcher, one cheburcheck and one bsbord settings read while off, plus 11 for a warm evaluator pass (no nodes or inbounds read again, no summary write, and one batched external-source settings read)",
    );
    assert.equal(liveProjectionQueries, 3, "the node-down sweep, the evaluator and the ten-minute safety net each read live nodes once");
    assert.equal(nodePokeCalls, 1, "the ten-minute safety net performs one remote fan-out");
    assert.deepEqual(nodePokeIDs, [[]], "the fan-out only includes connected nodes");

    const retentionDate = new Date(baseCronDate);
    retentionDate.setUTCMinutes(17, 0, 0);
    await globalThis.__d1.prepare("UPDATE job_lease SET expires_at = 0 WHERE name = 'health-eval'").run();
    const { queries: retentionQueries } = await countedJob("cron minute 17", () =>
      Promise.race([secondPanel.cron({ at: retentionDate.getTime() }), wasmFailure]),
    );
    console.log(`D1 query count ${JSON.stringify(retentionQueries)}`);
    assert.equal(
      retentionQueries.sequentialQueries,
      36,
      "a minute 17 cron tick costs 36 D1 calls: 6 for the sweeps and settings, one Telegram pending-code check, one cheburcheck and one bsbord settings read while off, 14 for retention (eight of them prune statements), 11 for a warm evaluator pass (including one batched external-source settings read), and 2 for the updates job (the active rollout read and its history prune)",
    );

    // The Overview is polled every 10 seconds. The health summary of every node is one settings row, read once: more connected
    // nodes add no D1 call (a read per connected node was the first version of this).
    const connectNode = async (id) => {
      await globalThis.__d1.prepare("INSERT INTO node (id, name, address, state, created_at, last_seen_at, last_connected_at) VALUES (?, ?, '198.51.100.20', 'active', 1, ?, ?)")
        .bind(id, id.replace("_", "-"), nowS, nowS).run();
      await globalThis.__d1.prepare("INSERT INTO node_live (node_id, session) VALUES (?, 1)").bind(id).run();
    };
    const overviewCost = async (label) => {
      const { response, queries } = await countedRequest(label, () => connectRPC(secondPanel, "FleetService/Overview", {}, adminCookie));
      console.log(`D1 query count ${JSON.stringify(queries)}`);
      assert.equal(response.response.status, 200, `${label} answers`);
      return { queries, nodes: response.message.nodes.length };
    };
    await connectNode("nod_more0");
    const overviewFew = await overviewCost("overview with one connected node");
    for (let i = 1; i < 6; i++) await connectNode(`nod_more${i}`);
    const overviewMany = await overviewCost("overview with six connected nodes");
    assert.equal(overviewMany.nodes, overviewFew.nodes + 5, "the added nodes are on the Overview");
    assert.equal(overviewMany.queries.sequentialQueries, overviewFew.queries.sequentialQueries, "an Overview costs the same D1 calls whatever the number of nodes");
    assert.equal(overviewFew.queries.sequentialQueries, 8, "an Overview costs 8 D1 calls: the session, nodes, live nodes, traffic hours, events, enabled inbounds, the node-health summary row and the alert badge");
    await globalThis.__d1.prepare("DELETE FROM node_live WHERE node_id LIKE 'nod_more%'").run();
    await globalThis.__d1.prepare("DELETE FROM node WHERE id LIKE 'nod_more%'").run();

    assert.equal(provisionKicks, 0, "cron ticks without an install wake no Provisioner");
    await assertProvisioner(bytes, secondPanel, adminCookie, initOptions, baseCronDate);

    thirdPanel = await startIsolate(bytes, secondPanel);
    const withoutLimiter = { ...initOptions };
    delete withoutLimiter.limit;
    delete withoutLimiter.nodeLink;
    await Promise.race([thirdPanel.init(withoutLimiter), wasmFailure]);
    const unavailableMarker = await thirdPanel.fetch({
      method: "GET",
      url: `https://example.com${linkPrefix}link/${nodeID}`,
      headers: [["Upgrade", "websocket"], ["Connection", "Upgrade"], ["CF-Connecting-IP", "127.0.0.1"]],
      body: null,
    });
    assert.equal(unavailableMarker.status, 404, "the link prefix is not mounted without nodeLink");
    const missingCallback = await connectRPC(thirdPanel, "AuthService/BeginLogin", {});
    assert.equal(missingCallback.message.code, "resource_exhausted", "login fails closed without the callback");

    // Memory budget: the largest linear memory of any isolate (wasm memory never shrinks, so this is the peak) must leave
    // about 32 MB of the 128 MB isolate to JavaScript.
    const memoryMiB = wasmMemories.map((memory) => memory.buffer.byteLength / (1024 * 1024));
    const peakMiB = Math.max(...memoryMiB);
    console.log(`wasm linear memory: maximum ${peakMiB.toFixed(1)} MiB over ${memoryMiB.length} isolates (budget ${WASM_MEMORY_BUDGET_MIB} MiB; each: ${memoryMiB.map((m) => m.toFixed(0)).join(", ")})`);
    assert.ok(peakMiB <= WASM_MEMORY_BUDGET_MIB, `wasm memory reached ${peakMiB.toFixed(1)} MiB, over the ${WASM_MEMORY_BUDGET_MIB} MiB budget`);
  } finally {
    oracle.child.kill();
  }
}

run().then(() => process.exit(0), (error) => {
  console.error(error);
  process.exit(1);
});
