"use strict";

const assert = require("node:assert/strict");
const { spawn } = require("node:child_process");
const { randomBytes } = require("node:crypto");
const fs = require("node:fs");
const http = require("node:http");
const net = require("node:net");
const path = require("node:path");
const { createHmac } = require("node:crypto");
const { pathToFileURL } = require("node:url");

let wasmFailure;
let rejectWasmFailure;
const securityLimits = new Map();
let limitMath;

const [wasmPath, wasmExecPath, oraclePath, dataDir] = process.argv.slice(2);
if (!wasmPath || !wasmExecPath || !oraclePath || !dataDir) {
  throw new Error("usage: bridge.cjs <panel.wasm> <wasm_exec.js> <vps-oracle> <temp-dir>");
}

const database = path.join(dataDir, "shared.sqlite");
process.env.MISTGATE_BRIDGE_D1_PATH = database;
require(path.resolve(__dirname, "../../../edge/d1driver/testdata/fake-d1.cjs"));
require(wasmExecPath);

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
    return { response, queries: globalThis.__d1.__endQueryCount() };
  } catch (error) {
    globalThis.__d1.__endQueryCount();
    throw error;
  }
}

function assertQueryBudget(queries) {
  console.log(`D1 query count ${JSON.stringify(queries)}`);
  // Target 6 (README §3.1). The AWG formats (Mihomo, .conf) measure 7: one extra round for the AWG scopes; phase 4.
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
    };
    try {
      await assert.rejects(Promise.race([
        firstPanel.init({ ...initOptions, publicURL: "https://192.0.2.10" }), // WebAuthn refuses an IP-address RP ID
        wasmFailure,
      ]), "invalid WebAuthn RP settings must fail before first-run settings are stored");
      // The VPS oracle shares this database; its own start writes the subscription-rules marker (subsettings.MigrateM3).
      const settingsAfterFailure = await globalThis.__d1.prepare("SELECT count(*) AS n FROM setting WHERE k <> 'sub_rules_m3'").first("n");
      assert.equal(Number(settingsAfterFailure), 0, "failed first init leaves no settings behind");
      firstInstance = await Promise.race([firstPanel.init(initOptions), wasmFailure]);
      secondPanel = await startIsolate(bytes, firstPanel);
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
    assert.ok(backup.message.message.includes("R2 binding"));

    const rescan = await connectRPC(secondPanel, "UpdateService/RescanBundle", {}, adminCookie);
    assert.equal(rescan.message.code, "failed_precondition", "filesystem update RPC fails cleanly on the edge");
    assert.ok(rescan.message.message.includes("edge edition"));

    const tokens = await connectRPC(secondPanel, "ApiTokenService/ListApiTokens", {}, adminCookie);
    assert.equal(tokens.response.status, 200, "the API-token admin RPC stays available on the edge");
    assert.ok(tokens.message.nowUnix, "the API-token RPC returns its current time");

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

    const awgDevice = await connectRPC(secondPanel, "DeviceService/CreateAwgDevice", {
      userId: createdUser.message.user.id, profileId: awgProfile.message.profile.id,
      platform: "linux", label: "bridge-device",
    }, adminCookie);
    assert.equal(awgDevice.response.status, 200, "the admin API creates an AWG device");
    const awgDeviceID = awgDevice.message.device.id;

    let previousPanel = secondPanel;
    async function freshPanel() {
      const panel = await startIsolate(bytes, previousPanel);
      previousPanel = panel;
      await Promise.race([panel.init(initOptions), wasmFailure]);
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

    thirdPanel = await startIsolate(bytes, secondPanel);
    const withoutLimiter = { ...initOptions };
    delete withoutLimiter.limit;
    await Promise.race([thirdPanel.init(withoutLimiter), wasmFailure]);
    const missingCallback = await connectRPC(thirdPanel, "AuthService/BeginLogin", {});
    assert.equal(missingCallback.message.code, "resource_exhausted", "login fails closed without the callback");
  } finally {
    oracle.child.kill();
  }
}

run().then(() => process.exit(0), (error) => {
  console.error(error);
  process.exit(1);
});
