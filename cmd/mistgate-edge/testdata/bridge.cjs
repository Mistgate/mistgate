"use strict";

const assert = require("node:assert/strict");
const { spawn } = require("node:child_process");
const { randomBytes } = require("node:crypto");
const fs = require("node:fs");
const http = require("node:http");
const net = require("node:net");
const path = require("node:path");
const { createHmac } = require("node:crypto");

let wasmFailure;
let rejectWasmFailure;

const [wasmPath, wasmExecPath, oraclePath, dataDir] = process.argv.slice(2);
if (!wasmPath || !wasmExecPath || !oraclePath || !dataDir) {
  throw new Error("usage: bridge.cjs <panel.wasm> <wasm_exec.js> <vps-oracle> <temp-dir>");
}

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

function startOracle(binary, database, port) {
  const child = spawn(binary, [database, `127.0.0.1:${port}`], {
    stdio: ["ignore", "pipe", "pipe"],
  });
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
      response.on("end", () => resolve({ status: response.statusCode, body: Buffer.concat(chunks) }));
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

async function connectRPC(panel, method, body, cookie = "") {
  const headers = {
    "Content-Type": "application/json",
    "Connect-Protocol-Version": "1",
    "CF-Connecting-IP": "127.0.0.1",
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
  const port = await reservePort();
  const database = path.join(dataDir, "vps-oracle.sqlite");
  const oracle = startOracle(oraclePath, database, port);
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
    const masterKey = new Uint8Array(randomBytes(32));
    const initOptions = {
      d1: globalThis.__d1,
      masterKey,
      publicURL: "https://example.com",
      adminPrefix: "/test-admin/",
      subPrefix: "/test-sub/",
    };
    try {
      firstInstance = await Promise.race([firstPanel.init(initOptions), wasmFailure]);
      secondPanel = await startIsolate(bytes, firstPanel);
      secondInstance = await Promise.race([secondPanel.init(initOptions), wasmFailure]);
    } finally {
      console.log = consoleLog;
    }
    assert.equal(firstInstance, firstPanel, "first isolate init returns the panel API");
    assert.equal(secondInstance, secondPanel, "fresh isolate init returns the panel API");
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
    if (!Buffer.from(rpcEdge.body).equals(rpcVPS.body)) {
      throw new Error("edge setup-state RPC bytes differ from the shared VPS handler");
    }
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
    const setupFinish = await connectRPC(firstPanel, "AuthService/FinishSetup", {
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
  } finally {
    oracle.child.kill();
  }
}

run().then(() => process.exit(0), (error) => {
  console.error(error);
  process.exit(1);
});
