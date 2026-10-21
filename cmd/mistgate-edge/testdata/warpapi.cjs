"use strict";

// A fake WARP client API for the js tests of internal/panel/warp, and the switch that lets Go's net/http use fetch under
// Node. Loaded with --require (scripts/test-edge-store.ps1), only for the tests that need it.
//
// Go's net/http sends requests through the runtime's fetch on GOOS=js, as in a Worker, except under Node: it reads
// process.argv0 once at start and, when it begins with "node", uses its own in-process network instead (go.dev/issue/57613).
// A Worker has no such process, so the tests present one whose argv0 is "workerd"; everything else is the real process.
// This is a stand-in for Cloudflare, never the real API: it listens on 127.0.0.1 only.
//
//   __warpApi.start()           -> Promise<base URL>
//   __warpApi.reply(status, body?, headers?)  the answer to every following request (no body: a registration)
//   __warpApi.seen()            -> the requests so far: [{method, url, headers, body}]

const http = require("node:http");

const workerProcess = Object.create(process, { argv0: { value: "workerd", enumerable: true } });
Object.defineProperty(globalThis, "process", { value: workerProcess, writable: true, configurable: true });

const registration = {
  id: "dev-0001",
  token: "tok-0001",
  account: { account_type: "free", license: "lic-0001" },
  config: {
    client_id: "AQID",
    interface: { addresses: { v4: "172.16.0.2", v6: "2606:4700:110:8000::2" } },
    peers: [{ public_key: Buffer.alloc(32, 7).toString("base64"), endpoint: { v4: "162.159.192.1:0", v6: "[2606:4700:d0::a29f:c001]:0", ports: [2408, 500] } }],
  },
};

let server = null;
let answer = { status: 201, body: JSON.stringify(registration), headers: { "Content-Type": "application/json" } };
const requests = [];

globalThis.__warpApi = {
  start() {
    if (server) return Promise.resolve(`http://127.0.0.1:${server.address().port}`);
    server = http.createServer((req, res) => {
      const chunks = [];
      req.on("data", (c) => chunks.push(c));
      req.on("end", () => {
        requests.push({ method: req.method, url: req.url, headers: { ...req.headers }, body: Buffer.concat(chunks).toString("utf8") });
        res.writeHead(answer.status, answer.headers);
        res.end(answer.body);
      });
    });
    server.unref();
    return new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(`http://127.0.0.1:${server.address().port}`)));
  },
  reply(status, body, headers) {
    answer = { status, body: body ?? JSON.stringify(registration), headers: headers ?? { "Content-Type": "application/json" } };
  },
  seen() {
    return requests.map((r) => ({ ...r, headers: { ...r.headers } }));
  },
};
