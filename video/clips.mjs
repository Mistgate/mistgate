// The short loops beside the features in the README: one clip of a scene of the admin's dev-kit (invented data, no panel
// needed), with a cursor that moves, hovers and clicks like a person. Real time, not the film pipeline: Chrome's screencast
// hands over a frame whenever the page repaints; the frames become an animated WebP with ffmpeg.
//
//   node clips.mjs <take>... [--theme dark|light|both] [--out docs/images/readme] [--keep]   (takes below; default: both themes, out is ../docs/images/readme; files <take>-<theme>-en.webp)
//   node clips.mjs --probe "<dev-kit url>" [width height]            (lists what can be clicked on that page)
//
// Needs the admin's Vite on ADMIN_ORIGIN (default http://127.0.0.1:5293, started from web/) and ffmpeg on PATH.
// Chrome from CHROME, always headless=old (the new headless mode opens windows on some systems).
import { spawn, execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const ORIGIN = process.env.ADMIN_ORIGIN || "http://127.0.0.1:5293";
const CHROME =
  process.env.CHROME ||
  { win32: "C:/Program Files/Google/Chrome/Application/chrome.exe", darwin: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" }[process.platform] ||
  "google-chrome";
const PORT = Number(process.env.CDP_PORT || 9391);

// ---- the takes: url (a dev-kit page), vp (the browser window), crop [x, y, w, h] in page pixels (the clip is this part of
// the window), steps. Steps: ["wait", ms] ["move", target, ms?] ["click", target] ["type", text] ["eval", js] ["hold", ms]
// A target is a CSS selector, "text:Exact words" (a visible element whose own text is these words) or [x, y].
const takes = {
  decoys: {
    url: "/dev-kit?only=decoys-page&state=dlg-tpl&fx=1",
    vp: [1280, 900],
    crop: [190, 30, 900, 820],
    steps: [
      ["wait", 300],
      ["click", "text:Germany"],
      ["wait", 300],
      ["click", "text:United Kingdom"],
      ["wait", 300],
      ["click", "text:Portfolio"],
      ["wait", 1100],
      ["click", "text:Another variant"],
      ["wait", 1000],
    ],
  },
  addnode: {
    url: "/dev-kit?only=windows-page&scene=node-srv-filled&fx=1",
    vp: [1440, 900],
    crop: [250, 80, 920, 640],
    steps: [
      ["wait", 700],
      ["click", "text:Check server", 700],
      ["goto", "/dev-kit?only=windows-page&scene=node-auth-ok&fx=1", 1100],
      ["goto", "/dev-kit?only=windows-page&scene=node-chk&fx=1", 1500],
      ["goto", "/dev-kit?only=windows-page&scene=node-run&fx=1", 1500],
      ["goto", "/dev-kit?only=windows-page&scene=node-done&fx=1", 1800],
    ],
  },
  health: {
    url: "/dev-kit?only=health-page&state=checks&fx=1",
    vp: [1440, 900],
    crop: [280, 150, 1130, 700],
    steps: [
      ["wait", 500],
      ["move", "text:de1", 600],
      ["click", "text:fi1"],
      ["wait", 900],
      ["click", "text:fi1"],
      ["wait", 1800],
    ],
  },
  shared: {
    url: "/dev-kit?only=share",
    vp: [1100, 440],
    crop: [38, 0, 1024, 440],
    prep: "document.querySelectorAll('[data-shot]>:first-child').forEach((e)=>{if(!e.querySelector('input,button'))e.style.display='none'});document.querySelector('[data-shot=share-offer]').scrollIntoView({block:'start'})",
    steps: [
      ["wait", 900],
      ["click", "[data-shot=share-offer] input"],
      ["wait", 500],
      ["click", "text:Put beside on 443"],
      ["wait", 1100],
      ["eval", "document.querySelector('[data-shot=share-card]').scrollIntoView({block:'start',behavior:'smooth'})"],
      ["wait", 2200],
    ],
  },
  warp: {
    url: "/dev-kit?only=health",
    vp: [1100, 520],
    crop: [30, 14, 1040, 490],
    prep: "document.querySelectorAll('[data-shot]>:first-child').forEach((e)=>{if(!e.querySelector('input,button'))e.style.display='none'});document.querySelector('[data-shot=warp-nod_w_ok]').scrollIntoView({block:'start'})",
    steps: [
      ["wait", 700],
      ["move", "text:Pause", 800],
      ["wait", 700],
      ["eval", "document.querySelector('[data-shot=warp-nod_w_down]').scrollIntoView({block:'start',behavior:'smooth'})"],
      ["wait", 900],
      ["move", "text:Restart WARP", 800],
      ["wait", 1200],
    ],
  },
  dns: {
    url: "/dev-kit?only=subs-page&state=dns&fx=1",
    vp: [1440, 900],
    crop: [280, 90, 1130, 720],
    steps: [
      ["wait", 800],
      ["move", "text:AdGuard: no ads", 800],
      ["wait", 500],
      ["goto", "/dev-kit?only=subs-page&state=dns-cf&fx=1", 1800],
      ["goto", "/dev-kit?only=subs-page&state=dns-split&fx=1", 2200],
    ],
  },
  link: {
    url: "/dev-kit?only=user-page&state=ok&fx=1",
    vp: [1440, 900],
    crop: [280, 90, 1130, 720],
    steps: [
      ["wait", 900],
      ["move", "text:Copy", 800],
      ["wait", 500],
      ["goto", "/dev-kit?only=user-page&state=ok&ui=link-qr&fx=1", 2600],
    ],
  },
  updates: {
    url: "/dev-kit?only=pn-updates-page&state=ag.new&fx=1",
    vp: [1440, 900],
    crop: [280, 90, 1130, 760],
    steps: [
      ["wait", 1300],
      ["goto", "/dev-kit?only=pn-updates-page&state=ag.roll0&fx=1", 1500],
      ["goto", "/dev-kit?only=pn-updates-page&state=ag.roll1&fx=1", 1500],
      ["goto", "/dev-kit?only=pn-updates-page&state=ag.roll2&fx=1", 1800],
    ],
  },
  os: {
    url: "/dev-kit?only=pn-updates-page&state=os.new.closed&fx=1",
    vp: [1440, 900],
    crop: [280, 90, 1130, 760],
    steps: [
      ["wait", 1200],
      ["goto", "/dev-kit?only=pn-updates-page&state=os.new.closed.tick&fx=1", 1500],
      ["goto", "/dev-kit?only=pn-updates-page&state=os.busy&fx=1", 1500],
      ["goto", "/dev-kit?only=pn-updates-page&state=os.just&fx=1", 1800],
    ],
  },
  assistant: {
    url: "/dev-kit?only=pn-assistant-page&state=empty.big&fx=1",
    vp: [1440, 900],
    crop: [230, 30, 965, 840],
    steps: [
      ["wait", 1200],
      ["goto", "/dev-kit?only=pn-assistant-page&state=think.big&fx=1", 1400],
      ["goto", "/dev-kit?only=pn-assistant-page&state=answer.big.plans&fx=1", 2000],
      ["goto", "/dev-kit?only=pn-assistant-page&state=done.big&fx=1", 1800],
    ],
  },
  telegram: {
    url: "/dev-kit?only=pn-integrations-page&state=unlinked&fx=1",
    vp: [1440, 900],
    crop: [280, 90, 1130, 700],
    steps: [
      ["wait", 1400],
      ["goto", "/dev-kit?only=pn-integrations-page&state=wait&fx=1", 1500],
      ["goto", "/dev-kit?only=pn-integrations-page&state=fresh-chat&fx=1", 1500],
      ["goto", "/dev-kit?only=pn-integrations-page&state=tdone&fx=1", 1800],
    ],
  },
  backups: {
    url: "/dev-kit?only=restore&state=pick-file",
    vp: [1100, 640],
    crop: [250, 10, 600, 620],
    steps: [
      ["wait", 1300],
      ["click", "text:Check backup", 700],
      ["goto", "/dev-kit?only=restore&state=confirm", 1800],
      ["goto", "/dev-kit?only=restore&state=restarting", 1300],
      ["goto", "/dev-kit?only=restore&state=back", 1800],
    ],
  },
};

const ease = (t) => (t < 0.5 ? 4 * t * t * t : 1 - (-2 * t + 2) ** 3 / 2);

async function launch() {
  const prof = mkdtempSync(join(tmpdir(), "mg-clips-"));
  const child = spawn(CHROME, ["--headless=old", `--remote-debugging-port=${PORT}`, `--user-data-dir=${prof}`, "--no-first-run", "--no-default-browser-check", "--hide-scrollbars", "--force-color-profile=srgb", "--disable-gpu", "--disable-background-networking", "--disable-sync", "--font-render-hinting=none", "about:blank"], { stdio: "ignore" });
  let list = [];
  for (let i = 0; i < 100; i++) {
    try {
      list = await (await fetch(`http://127.0.0.1:${PORT}/json/list`)).json();
      if (list.some((t) => t.type === "page")) break;
    } catch {}
    await sleep(200);
  }
  const ws = new WebSocket(list.find((t) => t.type === "page").webSocketDebuggerUrl);
  await new Promise((r, j) => ((ws.onopen = r), (ws.onerror = j)));
  let seq = 0;
  const pend = new Map();
  const handlers = new Set();
  ws.onmessage = (m) => {
    const x = JSON.parse(m.data);
    if (x.id && pend.has(x.id)) {
      const { res, rej } = pend.get(x.id);
      pend.delete(x.id);
      x.error ? rej(new Error(JSON.stringify(x.error))) : res(x.result);
    } else if (x.method) handlers.forEach((h) => h(x));
  };
  const send = (method, params = {}) => new Promise((res, rej) => { const i = ++seq; pend.set(i, { res, rej }); ws.send(JSON.stringify({ id: i, method, params })); });
  const on = (h) => (handlers.add(h), () => handlers.delete(h));
  return { send, on, close: async () => { ws.close(); child.kill(); await sleep(800); try { rmSync(prof, { recursive: true, force: true }); } catch {} } };
}

// the drawn pointer: Chrome's headless screencast has none
const CURSOR = `(()=>{const mk=()=>{if(document.getElementById("__cur"))return;const c=document.createElement("div");c.id="__cur";c.style.cssText="position:fixed;left:0;top:0;width:22px;height:22px;pointer-events:none;z-index:2147483647;display:none;transition:none;will-change:transform";c.innerHTML='<svg width="22" height="22" viewBox="0 0 22 22"><path d="M3 2l14 8.5-6.2 1.6L8 18.5z" fill="#fff" stroke="#111" stroke-width="1.4" stroke-linejoin="round"/></svg>';document.documentElement.appendChild(c);
const r=document.createElement("div");r.id="__ring";r.style.cssText="position:fixed;left:0;top:0;width:34px;height:34px;margin:-17px 0 0 -17px;border-radius:50%;border:2px solid rgba(160,235,190,.9);pointer-events:none;z-index:2147483646;opacity:0";document.documentElement.appendChild(r);
addEventListener("mousemove",e=>{c.style.display="block";c.style.transform="translate("+e.clientX+"px,"+e.clientY+"px)";r.style.left=e.clientX+"px";r.style.top=e.clientY+"px"},true);
addEventListener("mousedown",()=>{r.animate([{opacity:.95,transform:"scale(.4)"},{opacity:0,transform:"scale(1.25)"}],{duration:420,easing:"ease-out"})},true)};
if(document.documentElement)mk();else addEventListener("DOMContentLoaded",mk)})()`;

// where a target is on the page: its centre
const FIND = (q) => `(()=>{const q=${JSON.stringify(q)};let e=null;const vis=(n)=>{const r=n.getBoundingClientRect();return r.width>0&&r.height>0&&getComputedStyle(n).visibility!=="hidden"};
if(q.startsWith("text:")){const t=q.slice(5).trim().toLowerCase();const all=[...document.querySelectorAll("*")].filter(n=>vis(n)&&(n.innerText||"").trim().toLowerCase()===t);e=all.find(n=>n.closest("button,a,[role=button],[role=tab],[role=option],[role=radio],[role=menuitem]"))||all[0];if(e)e=e.closest("button,a,[role=button],[role=tab],[role=option],[role=radio],[role=menuitem]")||e}else e=document.querySelector(q);
if(!e||!vis(e))return null;e.scrollIntoView({block:"nearest"});const r=e.getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2}})()`;

async function drive(b, take, log, cast, theme) {
  const url = (u) => ORIGIN + u + (u.includes("?") ? "&" : "?") + "theme=" + theme;
  const [vw, vh] = take.vp;
  let cur = take.from ?? [vw * 0.9, vh * 1.05];
  const where = async (t) => {
    if (Array.isArray(t)) return { x: t[0], y: t[1] };
    for (let i = 0; i < 20; i++) {
      const r = await b.send("Runtime.evaluate", { expression: FIND(t), returnByValue: true });
      if (r.result.value) return r.result.value;
      await sleep(150);
    }
    const shot = await b.send("Page.captureScreenshot", { format: "png" });
    writeFileSync(join(tmpdir(), "mg-clip-error.png"), Buffer.from(shot.data, "base64"));
    throw new Error(`${take.name}: nothing to point at: ${t} (the page is in ${join(tmpdir(), "mg-clip-error.png")})`);
  };
  const mouse = (type, x, y, extra = {}) => b.send("Input.dispatchMouseEvent", { type, x, y, button: "none", ...extra });
  const move = async (t, ms = 650) => {
    const to = await where(t);
    const n = Math.max(4, Math.round(ms / 24));
    const [x0, y0] = cur;
    for (let i = 1; i <= n; i++) {
      const k = ease(i / n);
      await mouse("mouseMoved", x0 + (to.x - x0) * k, y0 + (to.y - y0) * k);
      await sleep(ms / n);
    }
    cur = [to.x, to.y];
    return to;
  };
  for (const [op, a, c] of take.steps) {
    if (op === "wait" || op === "hold") await sleep(a);
    else if (op === "move") await move(a, c);
    else if (op === "goto") {
      // another state of the scene, as a cut: the page loads again and the pointer stays where it was
      await cast.stop();
      await b.send("Page.navigate", { url: url(a) });
      await sleep(500);
      for (let i = 0; i < 80; i++) {
        const r = await b.send("Runtime.evaluate", { expression: "!document.getElementById('mg-splash') && document.body.innerText.length>60", returnByValue: true });
        if (r.result.value) break;
        await sleep(250);
      }
      await sleep(350);
      await mouse("mouseMoved", cur[0], cur[1]);
      await cast.start(); // the loading is not filmed: a cut, the next frame comes at once
      await sleep(c ?? 1200);
    }
    else if (op === "click") {
      const to = await move(a, c ?? 480);
      await sleep(90);
      await mouse("mousePressed", to.x, to.y, { button: "left", buttons: 1, clickCount: 1 });
      await sleep(70);
      await mouse("mouseReleased", to.x, to.y, { button: "left", buttons: 0, clickCount: 1 });
      await sleep(200);
    } else if (op === "type") {
      for (const ch of a) { await b.send("Input.insertText", { text: ch }); await sleep(c ?? 70); }
    } else if (op === "key") await b.send("Input.dispatchKeyEvent", { type: "keyDown", key: a }), await b.send("Input.dispatchKeyEvent", { type: "keyUp", key: a });
    else if (op === "eval") await b.send("Runtime.evaluate", { expression: a, awaitPromise: true });
    else throw new Error("unknown step " + op);
  }
  log(`${take.name}: played`);
}

async function film(name, theme, outDir, keep) {
  const take = { ...takes[name], name };
  const [vw, vh] = take.vp;
  const [cx, cy, cw, ch] = take.crop ?? [0, 0, vw, vh];
  const dpr = 1; // the screencast gives the window at 1x; the crops are at least as wide as the clip (840 px)
  const log = (s) => console.log(s);
  const b = await launch();
  const tmp = mkdtempSync(join(tmpdir(), `mg-clip-${name}-`));
  try {
    await b.send("Page.enable");
    await b.send("Runtime.enable");
    await b.send("Emulation.setDeviceMetricsOverride", { width: vw, height: vh, deviceScaleFactor: dpr, mobile: false });
    await b.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: theme }] });
    await b.send("Page.addScriptToEvaluateOnNewDocument", { source: `try{localStorage.setItem("lang","en");localStorage.setItem("theme","${theme}");localStorage.setItem("firstRunHidden","1")}catch(e){}` });
    await b.send("Page.addScriptToEvaluateOnNewDocument", { source: CURSOR });
    await b.send("Page.navigate", { url: ORIGIN + take.url + (take.url.includes("?") ? "&" : "?") + "theme=" + theme });
    for (let i = 0; i < 80; i++) {
      const r = await b.send("Runtime.evaluate", { expression: "!document.getElementById('mg-splash') && document.body.innerText.length>60", returnByValue: true });
      if (r.result.value) break;
      await sleep(500);
    }
    await sleep(take.settle ?? 1500);
    const frames = [];
    let cut = false;
    let offset = 0;
    b.on(async (m) => {
      if (m.method !== "Page.screencastFrame") return;
      let t = m.params.metadata.timestamp - offset;
      if (cut && frames.length) (offset += t - frames.at(-1).t - 0.04), (t = frames.at(-1).t + 0.04);
      cut = false;
      frames.push({ t, data: m.params.data });
      b.send("Page.screencastFrameAck", { sessionId: m.params.sessionId }).catch(() => {});
    });
    const cast = {
      start: async () => ((cut = true), b.send("Page.startScreencast", { format: "jpeg", quality: 92, everyNthFrame: 1, maxWidth: vw * dpr, maxHeight: vh * dpr })),
      stop: () => b.send("Page.stopScreencast"),
    };
    if (take.prep) await b.send("Runtime.evaluate", { expression: take.prep, awaitPromise: true }); // set the page up before the first frame
    await sleep(300);
    await cast.start();
    await sleep(take.lead ?? 700);
    await drive(b, take, log, cast, theme);
    await sleep(take.tail ?? 900);
    await cast.stop();
    if (frames.length < 3) throw new Error(`${name}: only ${frames.length} frames`);
    // the concat list: every frame stays until the next one arrives; the last stays 0.6 s (the loop's breath)
    const lines = [];
    frames.forEach((f, i) => {
      const file = join(tmp, `f${String(i).padStart(5, "0")}.jpg`);
      writeFileSync(file, Buffer.from(f.data, "base64"));
      const d = i + 1 < frames.length ? frames[i + 1].t - f.t : 0.6;
      lines.push(`file '${file.replace(/\\/g, "/")}'`, `duration ${Math.max(d, 0.001).toFixed(4)}`);
    });
    lines.push(lines[lines.length - 2]);
    writeFileSync(join(tmp, "list.txt"), lines.join("\n"));
    mkdirSync(outDir, { recursive: true });
    const out = join(outDir, `${name}-${theme}-en.webp`);
    const width = take.width ?? 840;
    const fps = take.fps ?? 12;
    const q = take.q ?? 55;
    const crop = `crop=${Math.round(cw * dpr)}:${Math.round(ch * dpr)}:${Math.round(cx * dpr)}:${Math.round(cy * dpr)}`;
    execFileSync("ffmpeg", ["-v", "error", "-y", "-f", "concat", "-safe", "0", "-i", join(tmp, "list.txt"), "-vf", `fps=${fps},${crop},scale=${width}:-2:flags=lanczos`, "-c:v", "libwebp_anim", "-lossless", "0", "-q:v", String(q), "-compression_level", "6", "-loop", "0", "-an", out], { stdio: "inherit" });
    const kb = statSync(out).size / 1024;
    log(`${out}: ${kb.toFixed(0)} kB, ${frames.length} frames, ${(frames.at(-1).t - frames[0].t).toFixed(1)} s`);
    if (keep) log(`frames kept in ${tmp}`);
  } finally {
    await b.close();
    if (!keep) rmSync(tmp, { recursive: true, force: true });
  }
}

async function probe(url, w = 1440, h = 900) {
  const b = await launch();
  try {
    await b.send("Page.enable");
    await b.send("Emulation.setDeviceMetricsOverride", { width: Number(w), height: Number(h), deviceScaleFactor: 1, mobile: false });
    await b.send("Page.addScriptToEvaluateOnNewDocument", { source: `try{localStorage.setItem("lang","en");localStorage.setItem("theme","dark");localStorage.setItem("firstRunHidden","1")}catch(e){}` });
    await b.send("Page.navigate", { url: ORIGIN + url });
    for (let i = 0; i < 80; i++) {
      const r = await b.send("Runtime.evaluate", { expression: "!document.getElementById('mg-splash') && document.body.innerText.length>60", returnByValue: true });
      if (r.result.value) break;
      await sleep(500);
    }
    await sleep(1500);
    const r = await b.send("Runtime.evaluate", { returnByValue: true, expression: `JSON.stringify([...document.querySelectorAll("button,a,[role=button],[role=tab],[role=option],[role=radio],input,select")].filter(n=>{const r=n.getBoundingClientRect();return r.width>0&&r.height>0}).map(n=>{const r=n.getBoundingClientRect();return Math.round(r.x)+","+Math.round(r.y)+" "+Math.round(r.width)+"x"+Math.round(r.height)+" "+(n.tagName+(n.getAttribute("role")?"["+n.getAttribute("role")+"]":""))+" | "+(n.innerText||n.value||n.placeholder||n.getAttribute("aria-label")||"").trim().replace(/\\s+/g," ").slice(0,50)}))` });
    console.log(JSON.parse(r.result.value).join("\n"));
  } finally {
    await b.close();
  }
}

{
  const args = process.argv.slice(2);
  if (args[0] === "--probe") await probe(args[1], args[2], args[3]);
  else {
    const o = args.indexOf("--out");
    const outDir = o >= 0 ? args[o + 1] : join(here, "..", "docs", "images", "readme");
    const names = args.filter((a, i) => !a.startsWith("--") && (o < 0 || i !== o + 1) && args[i - 1] !== "--theme");
    const t = args.indexOf("--theme");
    const themes = t >= 0 && args[t + 1] !== "both" ? [args[t + 1]] : ["dark", "light"];
    for (const n of names) {
      if (!takes[n]) throw new Error(`unknown take "${n}"; known: ${Object.keys(takes).join(", ")}`);
      for (const theme of themes) await film(n, theme, outDir, args.includes("--keep"));
    }
  }
}
