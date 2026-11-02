// A small CDP driver for filming the real admin: headless Chrome against the Vite dev server, every /api call answered
// from a mock table, every request that would leave the machine answered 404, and the page on Chrome's virtual clock so
// each frame is exactly 1/fps of page time apart however slow the capture is. No dependencies (Node 22: fetch + WebSocket).
import { spawn } from "node:child_process";
import { mkdirSync, rmSync } from "node:fs";

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Chrome from CHROME, else where it usually is
export const CHROME =
  process.env.CHROME ||
  { win32: "C:/Program Files/Google/Chrome/Application/chrome.exe", darwin: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" }[process.platform] ||
  "google-chrome";

export async function launch(profileDir, port = Number(process.env.CDP_PORT || 9381)) {
  rmSync(profileDir, { recursive: true, force: true });
  mkdirSync(profileDir, { recursive: true });
  const child = spawn(CHROME, [
    ...(/headless-shell/i.test(CHROME) ? [] : ["--headless=new"]), `--remote-debugging-port=${port}`, `--user-data-dir=${profileDir}`, "--no-first-run", "--no-default-browser-check",
    "--hide-scrollbars", "--force-color-profile=srgb", "--disable-background-networking", "--disable-component-update", "--disable-sync",
    "--disable-features=Translate,OptimizationHints,MediaRouter", "--font-render-hinting=none", "about:blank",
  ], { stdio: "ignore" });
  let list = [];
  for (let i = 0; i < 150; i++) {
    try {
      list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
      if (list.some((t) => t.type === "page")) break;
    } catch {}
    await sleep(200);
  }
  const ws = new WebSocket(list.find((t) => t.type === "page").webSocketDebuggerUrl);
  await new Promise((r, j) => ((ws.onopen = r), (ws.onerror = j)));
  ws.onclose = (e) => console.error(`cdp: the connection closed (${e.code})`);
  let seq = 0;
  const pending = new Map();
  const handlers = new Set();
  ws.onmessage = (m) => {
    const msg = JSON.parse(m.data);
    if (msg.id && pending.has(msg.id)) {
      const { res, rej } = pending.get(msg.id);
      pending.delete(msg.id);
      msg.error ? rej(new Error(JSON.stringify(msg.error))) : res(msg.result);
    } else if (msg.method) handlers.forEach((h) => h(msg));
  };
  const send = (method, params = {}) => new Promise((res, rej) => { const i = ++seq; pending.set(i, { res, rej }); ws.send(JSON.stringify({ id: i, method, params })); });
  const on = (h) => (handlers.add(h), () => handlers.delete(h));
  const once = (method) => new Promise((r) => { const off = on((m) => { if (m.method === method) { off(); r(m.params); } }); });
  return { child, send, on, once, close: () => { ws.close(); child.kill(); } };
}

/**
 * Answers the page's requests: /api/<pkg>.<Service>/<Method> from mocks["Service/Method"] (a value or a function of the
 * request body), the dev origin passes, anything else gets a 404 so nothing leaves the machine. Unknown API methods are
 * answered {} and reported once.
 */
export async function intercept(b, origins, mocks, log = () => {}, rewrite = null) {
  const missing = new Set();
  const pending = new Map(); // requestId → url, for the diagnosis of a stalled clock
  b.on(async (msg) => {
    if (msg.method === "Inspector.targetCrashed") log("the page crashed");
    if (msg.method === "Runtime.exceptionThrown") log("page exception " + JSON.stringify(msg.params.exceptionDetails).slice(0, 300));
    if (msg.method !== "Fetch.requestPaused") return;
    const p = msg.params;
    const url = new URL(p.request.url);
    pending.set(p.requestId, url.pathname);
    try {
      if (p.responseStatusCode !== undefined) {
        // a page whose HTML the take rewrites (the person's page: its data is inlined by the server)
        const r = await b.send("Fetch.getResponseBody", { requestId: p.requestId });
        const html = rewrite(url, r.base64Encoded ? Buffer.from(r.body, "base64").toString("utf8") : r.body);
        const headers = p.responseHeaders.filter((h) => !/content-length|content-encoding/i.test(h.name));
        await b.send("Fetch.fulfillRequest", { requestId: p.requestId, responseCode: p.responseStatusCode, responseHeaders: headers, body: Buffer.from(html).toString("base64") });
      } else if (url.pathname.includes("/api/") && origins.includes(url.origin)) {
        const method = url.pathname.split("/").slice(-2).join("/").replace(/^mistgate\.[a-z]+\.v1\./, "");
        let body = mocks[method];
        if (body === undefined && !missing.has(method)) (missing.add(method), log("unmocked " + method));
        if (typeof body === "function") body = await body(JSON.parse(p.request.postData || "{}"));
        const json = Buffer.from(JSON.stringify(body ?? {})).toString("base64");
        await b.send("Fetch.fulfillRequest", { requestId: p.requestId, responseCode: 200, responseHeaders: [{ name: "content-type", value: "application/json" }], body: json });
      } else if (origins.includes(url.origin) && !url.pathname.startsWith("/brand/")) {
        await b.send("Fetch.continueRequest", { requestId: p.requestId });
      } else if (url.protocol === "data:" || url.protocol === "blob:") {
        await b.send("Fetch.continueRequest", { requestId: p.requestId });
      } else {
        if (!url.pathname.startsWith("/brand/")) log("blocked " + url.href.slice(0, 120));
        await b.send("Fetch.fulfillRequest", { requestId: p.requestId, responseCode: 404, responseHeaders: [{ name: "content-type", value: "text/plain" }], body: "" });
      }
    } catch (e) {
      log(`intercept ${url.pathname}: ${e.message}`);
      await b.send("Fetch.fulfillRequest", { requestId: p.requestId, responseCode: 500, responseHeaders: [{ name: "content-type", value: "text/plain" }], body: "" }).catch(() => {});
    }
    pending.delete(p.requestId);
  });
  const patterns = [{ urlPattern: "*", requestStage: "Request" }];
  if (rewrite) patterns.push({ urlPattern: "*sub.html*", resourceType: "Document", requestStage: "Response" });
  await b.send("Fetch.enable", { patterns });
  return pending;
}
