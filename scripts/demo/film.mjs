// Films /dev-demo frame by frame: headless Chrome over CDP, the page on the Stageshow virtual clock (?film), one PNG per
// clock.step(). /api and /brand are answered here with 404, so nothing leaves the machine.
// node film.mjs <theme> <outDir> [--fps 25] [--poster] [--extra 12]
// The README, the home page and the wiki show demo-{dark,light}.webp from docs/images/readme. To film them again: run the
// admin's dev server on 5291 (pnpm --dir web exec vite --port 5291), then per theme
//   node scripts/demo/film.mjs dark out/frames-dark && node scripts/demo/film.mjs dark out/poster-dark --poster
//   python scripts/demo/encode.py out/frames-dark docs/images/readme/demo-dark.webp out/poster-dark/poster@2x.png out/demo-poster-dark.webp 90
// and the same for light. sheet.py lays frames on a contact sheet to review them.
import { spawn } from "node:child_process";
import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";

const CHROME = process.env.CHROME_PATH || (process.platform === "win32" ? "C:/Program Files/Google/Chrome/Application/chrome.exe" : "/usr/bin/google-chrome");
const ORIGIN = process.env.DEMO_ORIGIN || "http://127.0.0.1:5291";
const PORT = Number(process.env.CDP_PORT || 9371);
const W = 1100;
const H = 620;

const args = process.argv.slice(2);
const theme = args[0] || "dark";
const outDir = args[1];
const opt = (name, dflt) => {
  const i = args.indexOf(`--${name}`);
  return i < 0 ? dflt : args[i + 1];
};
const fps = Number(opt("fps", 25));
const posterOnly = args.includes("--poster");
const extra = Number(opt("extra", 12));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const profile = resolve(outDir, "..", `.chrome-${theme}-${posterOnly ? "poster" : "loop"}`);
rmSync(profile, { recursive: true, force: true });
mkdirSync(profile, { recursive: true });
mkdirSync(outDir, { recursive: true });

const child = spawn(CHROME, ["--headless=new", `--remote-debugging-port=${PORT}`, `--user-data-dir=${profile}`, "--no-first-run", "--no-default-browser-check", "--hide-scrollbars", "--force-color-profile=srgb", "--lang=en-US", "about:blank"], { stdio: "ignore" });
let list = [];
for (let i = 0; i < 150; i++) {
  try {
    list = await (await fetch(`http://127.0.0.1:${PORT}/json/list`)).json();
    if (list.some((t) => t.type === "page")) break;
  } catch {}
  await sleep(200);
}
const ws = new WebSocket(list.find((t) => t.type === "page").webSocketDebuggerUrl);
await new Promise((r, j) => ((ws.onopen = r), (ws.onerror = j)));
let seq = 0;
const pending = new Map();
ws.onmessage = async (m) => {
  const msg = JSON.parse(m.data);
  if (msg.id && pending.has(msg.id)) {
    const { res, rej } = pending.get(msg.id);
    pending.delete(msg.id);
    msg.error ? rej(new Error(JSON.stringify(msg.error))) : res(msg.result);
  } else if (msg.method === "Fetch.requestPaused") {
    send("Fetch.fulfillRequest", { requestId: msg.params.requestId, responseCode: 404, responseHeaders: [{ name: "content-type", value: "application/json" }], body: Buffer.from("{}").toString("base64") }).catch(() => {});
  } else if (msg.method === "Runtime.exceptionThrown") {
    console.error("page exception:", JSON.stringify(msg.params.exceptionDetails).slice(0, 500));
  } else if (msg.method === "Runtime.consoleAPICalled" && ["error", "warning"].includes(msg.params.type)) {
    console.error(`console.${msg.params.type}:`, msg.params.args.map((a) => a.value ?? a.description ?? "").join(" ").slice(0, 400));
  }
};
const send = (method, params = {}) => new Promise((res, rej) => { const i = ++seq; pending.set(i, { res, rej }); ws.send(JSON.stringify({ id: i, method, params })); });
const evaluate = async (expression) => {
  const r = await send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 500));
  return r.result.value;
};

try {
  await send("Page.enable");
  await send("Runtime.enable");
  await send("Fetch.enable", { patterns: [{ urlPattern: `${ORIGIN}/api/*` }, { urlPattern: `${ORIGIN}/brand/*` }, { urlPattern: `${ORIGIN}/preview/*` }] });
  await send("Emulation.setDeviceMetricsOverride", { width: W, height: H, deviceScaleFactor: posterOnly ? 2 : 1, mobile: false });
  if (posterOnly) await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-reduced-motion", value: "reduce" }] });
  await send("Page.navigate", { url: `${ORIGIN}/dev-demo?film&theme=${theme}&fps=${fps}` });

  // Ready: the clock is installed, the fonts are in, the stage is measured at 1:1 and its script has started
  let ready = null;
  for (let i = 0; i < 100 && !ready; i++) {
    await sleep(200);
    ready = await evaluate(`(async () => {
      const c = window.__stageshow?.clock, st = document.querySelector('.ss-stage');
      if (!c || !st || document.fonts.status !== 'loaded') return null;
      const r = st.getBoundingClientRect();
      return st.dataset.mark === 'start' || st.dataset.still !== undefined ? { x: r.x, y: r.y, w: r.width, h: r.height, fps: c.fps } : null;
    })()`).catch(() => null);
  }
  if (!ready) throw new Error("the stage never started");
  if (Math.abs(ready.w - W) > 0.5 || Math.abs(ready.h - H) > 0.5 || ready.x !== 0 || ready.y !== 0) throw new Error(`stage not at 1:1 in the corner: ${JSON.stringify(ready)}`);
  console.log("ready", JSON.stringify(ready));

  const shot = async (file) => {
    const { data } = await send("Page.captureScreenshot", { format: "png", clip: { x: 0, y: 0, width: W, height: H, scale: 1 } });
    writeFileSync(file, Buffer.from(data, "base64"));
  };
  const mark = () => evaluate("document.querySelector('.ss-stage').dataset.mark || ''");
  const step = () => evaluate("window.__stageshow.clock.step(1)");

  if (posterOnly) {
    // The Stage's poster: the payoff as a still, every value landed (what reduced motion shows); stepped a second so
    // the page's own mount transitions are done
    for (let i = 0; i < fps; i++) await step();
    await shot(join(outDir, "poster@2x.png"));
  } else {
    // The first loop plays unfilmed: mount fade-ins finish and the cursor learns its first move. Then the second loop
    // is filmed whole, frame 0 = its start, until the third starts; then a few frames past the seam
    let skipped = 0;
    for (let seen = false; skipped < 40 * fps; skipped++) {
      await step();
      const m = await mark();
      if (m !== "start") seen = true;
      else if (seen) break;
    }
    console.log("skipped", skipped + 1, "frames");
    const marks = [];
    let frame = 0;
    let last = await mark();
    let loopFrames = 0;
    for (; frame < 40 * fps; frame++) {
      await shot(join(outDir, `f${String(frame).padStart(4, "0")}.png`));
      await step();
      const m = await mark();
      if (m !== last) {
        marks.push({ frame: frame + 1, mark: m });
        if (m === "start") {
          loopFrames = frame + 1;
          break;
        }
        last = m;
      }
    }
    for (let k = 0; k < extra; k++) {
      await shot(join(outDir, `seam${String(k).padStart(2, "0")}.png`));
      await step();
    }
    writeFileSync(join(outDir, "film.json"), JSON.stringify({ theme, fps, loopFrames, ms: (loopFrames * 1000) / fps, marks }, null, 1));
    console.log("loop", loopFrames, "frames", ((loopFrames * 1000) / fps / 1000).toFixed(2), "s", JSON.stringify(marks));
  }
} finally {
  ws.close();
  child.kill();
}
