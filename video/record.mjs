// Films one take of the real admin (or the person's page) frame by frame: the page runs on its own clock (vclock.js) and
// every frame is 1/FPS of page time after the last, so the footage is smooth however slow the capture is. Real input events
// (eased mouse moves, clicks, typing) drive the app; the cursor itself is drawn later by the composition from track.json.
//
//   node record.mjs <take> [--lang en] [--theme dark]      (takes in takes.mjs; frames to assets/takes/<take>-<lang>/)
//
// Needs the admin's Vite on ADMIN_ORIGIN (default http://127.0.0.1:5293) and, for the person's page, the sub Vite on
// SUB_ORIGIN (default http://127.0.0.1:5294). Chrome from CHROME (default: where it usually is on the platform).
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { intercept, launch } from "./cdp.mjs";
import { NOW } from "./mocks.mjs";
import { takes } from "./takes.mjs";

const here = dirname(fileURLToPath(import.meta.url));
export const FPS = 30;
const ADMIN = process.env.ADMIN_ORIGIN || "http://127.0.0.1:5293";
const SUB = process.env.SUB_ORIGIN || "http://127.0.0.1:5294";

const args = process.argv.slice(2);
const opt = (name, dflt) => (args.includes(`--${name}`) ? args[args.indexOf(`--${name}`) + 1] : dflt);
const takeName = args[0];
const lang = opt("lang", "en");
const theme = opt("theme", "dark");
if (!takes[takeName]) throw new Error(`unknown take "${takeName}"; known: ${Object.keys(takes).join(", ")}`);

const ease = (t) => (t < 0.5 ? 4 * t * t * t : 1 - (-2 * t + 2) ** 3 / 2); // easeInOutCubic

async function film(take) {
  const out = join(here, "assets", "takes", `${takeName}-${lang}`);
  rmSync(out, { recursive: true, force: true });
  mkdirSync(out, { recursive: true });
  const b = await launch(join(tmpdir(), `mg-video-${takeName}-${lang}`));
  const log = (s) => console.log(`[${takeName}-${lang}] ${s}`);
  const vp = take.viewport ?? { width: 1600, height: 1000, dpr: 2, mobile: false };
  const mocks = {};
  const holds = []; // API answers kept back for a number of frames (the page shows its busy state meanwhile)
  const track = { fps: FPS, viewport: vp, frames: [], marks: {}, clicks: [], rects: {} };
  let frame = 0;
  let cursor = take.cursorFrom ?? { x: vp.width + 40, y: vp.height * 0.7 };
  let pressed = false;

  try {
    await b.send("Page.enable");
    await b.send("Runtime.enable");
    // a held method: the request waits in the interceptor until its frames are filmed
    const held = {};
    const wrapped = new Proxy(mocks, {
      get(t, k) {
        const v = t[k];
        if (!held[k] || v === undefined) return v;
        const frames = held[k];
        delete held[k];
        return (body) => new Promise((resolve) => holds.push({ left: frames, resolve: async () => resolve(typeof v === "function" ? await v(body) : v) }));
      },
    });
    // the person's page: the server inlines its data as <script id="mg-data">; the take's page(data) changes it
    const rewrite = (url, html) => html.replace(/(<script type="application\/json" id="mg-data">)([\s\S]*?)(<\/script>)/, (m, a, json, c) => a + JSON.stringify(personPage(JSON.parse(json))).replace(/</g, "\\u003c") + c);
    let personPage = (d) => d;
    const inFlight = await intercept(b, [ADMIN, SUB], wrapped, log, rewrite);
    await b.send("Emulation.setDeviceMetricsOverride", { width: vp.width, height: vp.height, deviceScaleFactor: vp.dpr, mobile: !!vp.mobile });
    if (vp.mobile) await b.send("Emulation.setTouchEmulationEnabled", { enabled: false });
    await b.send("Emulation.setTimezoneOverride", { timezoneId: "UTC" });
    await b.send("Emulation.setLocaleOverride", { locale: lang === "zh" ? "zh-CN" : lang }).catch(() => {});
    await b.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: theme }, { name: "prefers-reduced-motion", value: "no-preference" }] });
    await b.send("Page.addScriptToEvaluateOnNewDocument", { source: `try{localStorage.setItem("lang",${JSON.stringify(lang)});localStorage.setItem("theme",${JSON.stringify(theme)});localStorage.setItem("firstRunHidden","1")}catch(e){}` });
    await b.send("Page.addScriptToEvaluateOnNewDocument", { source: readFileSync(join(here, "vclock.js"), "utf8").replace("__START__", String(NOW * 1000)) });
    await b.send("Animation.enable");

    const evaluate = async (expression) => {
      const r = await b.send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
      if (r.exceptionDetails) throw new Error(`${expression.slice(0, 120)}: ${JSON.stringify(r.exceptionDetails).slice(0, 400)}`);
      return r.result.value;
    };
    const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
    // one frame of page time: held answers count down, the page clock moves 1/FPS s, the answers in flight land, and two
    // real frames pass so the paint is committed before the screenshot
    const tick = async () => {
      for (const h of holds) h.left--;
      for (const h of holds.filter((x) => x.left <= 0)) holds.splice(holds.indexOf(h), 1), await h.resolve();
      await evaluate(`__vclock.advance(${1000 / FPS})`);
      for (let i = 0; inFlight.size > holds.length && i < 2000; i++) await sleep(4);
      await evaluate(`new Promise((r) => __vclock.realRaf(() => __vclock.realRaf(r)))`);
    };
    const shoot = async () => {
      const late = setTimeout(() => log(`screenshot stalled at frame ${frame}`), 8000);
      const { data } = await b.send("Page.captureScreenshot", { format: "png", optimizeForSpeed: true, captureBeyondViewport: false });
      clearTimeout(late);
      writeFileSync(join(out, `f${String(frame).padStart(5, "0")}.png`), Buffer.from(data, "base64"));
      track.frames.push([Math.round(cursor.x * 10) / 10, Math.round(cursor.y * 10) / 10, pressed ? 1 : 0]);
      frame++;
    };
    const step = async () => (await tick(), await shoot());

    /** The centre (or a point by fractions fx, fy) of the first element the JS expression returns, in CSS px. */
    const rect = async (expr) => {
      const r = await evaluate(`(() => { const el = (${expr}); if (!el) return null; const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, w: r.width, h: r.height }; })()`);
      if (!r) throw new Error(`no element: ${expr}`);
      return r;
    };
    const s = {
      lang, theme, vp, evaluate, rect,
      get frame() { return frame; },
      mock: (table) => Object.assign(mocks, table),
      /** The person's page data: a function of the dev server's sample data. */
      page: (fn) => (personPage = fn),
      hold: (method, frames) => (held[method] = frames),
      log,
      async open(url, ready, origin = ADMIN) {
        await b.send("Page.navigate", { url: origin + url });
        await b.send("Animation.setPlaybackRate", { playbackRate: 0 }); // the animations move only with the page clock
        for (let i = 0; i < 200 && !(await evaluate("!!window.__vclock").catch(() => false)); i++) await sleep(20);
        // pre-roll, not filmed: modules load, the first answers land, the entry animations finish
        for (let i = 0; i < 40 * FPS; i++) {
          await tick();
          if (i > 10 && (await evaluate(`document.readyState === "complete" && !!(${ready})`).catch(() => false))) break;
          if (i === 40 * FPS - 1) throw new Error(`never ready: ${ready}`);
        }
        for (let i = 0; i < (take.settle ?? 1.2) * FPS; i++) await tick();
      },
      async wait(sec) { for (let i = 0, n = Math.round(sec * FPS); i < n; i++) await step(); },
      mark(name) { track.marks[name] = frame; },
      async rectMark(name, expr) { track.rects[name] = { frame, ...(await rect(expr)) }; },
      /** Eased move to an element (or {x,y}); fx/fy pick the point inside it. */
      async move(target, sec = 0.7, fx = 0.5, fy = 0.5) {
        const to = typeof target === "string" ? await rect(target).then((r) => ({ x: r.x + r.w * fx, y: r.y + r.h * fy })) : target;
        const from = { ...cursor };
        const n = Math.max(1, Math.round(sec * FPS));
        // a slight arc, as a hand moves
        const bend = Math.min(60, Math.hypot(to.x - from.x, to.y - from.y) * 0.08);
        for (let i = 1; i <= n; i++) {
          const k = ease(i / n);
          const arc = Math.sin(Math.PI * k) * bend;
          cursor = { x: from.x + (to.x - from.x) * k, y: from.y + (to.y - from.y) * k - arc };
          await b.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: cursor.x, y: cursor.y });
          await step();
        }
      },
      async click(target, sec = 0.7, fx, fy) {
        if (target) await s.move(target, sec, fx, fy);
        await s.wait(0.12);
        pressed = true;
        track.clicks.push(frame);
        await b.send("Input.dispatchMouseEvent", { type: "mousePressed", x: cursor.x, y: cursor.y, button: "left", clickCount: 1 });
        await step();
        await step();
        pressed = false;
        await b.send("Input.dispatchMouseEvent", { type: "mouseReleased", x: cursor.x, y: cursor.y, button: "left", clickCount: 1 });
        await step();
      },
      /** Types like a person: `cps` characters a second with a little rhythm. */
      async type(text, cps = 14) {
        let i = 0;
        for (const ch of text) {
          await b.send("Input.insertText", { text: ch });
          const gap = (1 / cps) * (1 + 0.35 * Math.sin(i++ * 2.3));
          await s.wait(gap);
        }
      },
      async key(key, opts = {}) {
        const map = { Enter: [13, "Enter"], Escape: [27, "Escape"], Tab: [9, "Tab"], Backspace: [8, "Backspace"], ArrowDown: [40, "ArrowDown"], ArrowUp: [38, "ArrowUp"] };
        const [code, name] = map[key] ?? [key.toUpperCase().charCodeAt(0), `Key${key.toUpperCase()}`];
        const base = { key, code: name, windowsVirtualKeyCode: code, nativeVirtualKeyCode: code, modifiers: opts.modifiers ?? 0, ...(opts.commands && { commands: opts.commands }) };
        await b.send("Input.dispatchKeyEvent", { type: "rawKeyDown", ...base, ...(key === "Enter" && { text: "\r" }) });
        if (key === "Enter") await b.send("Input.dispatchKeyEvent", { type: "char", ...base, text: "\r" });
        await b.send("Input.dispatchKeyEvent", { type: "keyUp", ...base });
        await step();
      },
      /** Real key presses (what typeahead in lists listens to), one every `gap` seconds. */
      async press(text, gap = 0.08) {
        for (const ch of text) {
          const code = `Key${ch.toUpperCase()}`;
          await b.send("Input.dispatchKeyEvent", { type: "keyDown", key: ch, code, text: ch, unmodifiedText: ch, windowsVirtualKeyCode: ch.toUpperCase().charCodeAt(0) });
          await b.send("Input.dispatchKeyEvent", { type: "keyUp", key: ch, code, windowsVirtualKeyCode: ch.toUpperCase().charCodeAt(0) });
          await s.wait(gap);
        }
      },
      async selectAll() { await s.key("a", { modifiers: 2, commands: ["selectAll"] }); },
      async scroll(dy, sec = 0.6) {
        const n = Math.round(sec * FPS);
        let done = 0;
        for (let i = 1; i <= n; i++) {
          const to = Math.round(dy * ease(i / n));
          await b.send("Input.dispatchMouseEvent", { type: "mouseWheel", x: cursor.x, y: cursor.y, deltaX: 0, deltaY: to - done });
          done = to;
          await step();
        }
      },
      /** Scrolls the page to y with an ease, one step per frame (a phone's flick, without the compositor's own timing). */
      async scrollTo(y, sec = 1.2) {
        const from = await evaluate("window.scrollY");
        const n = Math.max(1, Math.round(sec * FPS));
        for (let i = 1; i <= n; i++) {
          await evaluate(`window.scrollTo(0, ${from + (y - from) * ease(i / n)})`);
          await step();
        }
      },
      /** Moves the cursor without filming (between cuts). */
      place(p) { cursor = p; },
    };
    await take.run(s);
    writeFileSync(join(out, "track.json"), JSON.stringify(track));
    // the same for the composition, which loads it as a script (a render fetches nothing)
    writeFileSync(join(out, "track.js"), `(window.__tracks ||= {})[${JSON.stringify(`${takeName}-${lang}`)}] = ${JSON.stringify(track)};
`);
    log(`${frame} frames (${(frame / FPS).toFixed(1)} s) → ${out}`);
  } finally {
    b.close();
  }
}

await film(takes[takeName]);
