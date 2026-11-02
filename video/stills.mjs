// Stills of a composition at given times, without a render: a static server on this folder, Chrome at 1920×1080, the
// engine's own render(t). For reviewing a cut quickly; the delivery files come from `hyperframes render`.
//
//   node stills.mjs <composition.html> <lang> <outDir> <t1,t2,...>
import { createReadStream, existsSync, mkdirSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { dirname, extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { launch } from "./cdp.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const [comp, lang, outDir, times] = process.argv.slice(2);
const types = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".png": "image/png", ".svg": "image/svg+xml", ".woff2": "font/woff2" };
const server = createServer((req, res) => {
  const path = normalize(join(here, decodeURIComponent(new URL(req.url, "http://x").pathname)));
  if (!path.startsWith(here) || !existsSync(path)) return res.writeHead(404).end();
  res.writeHead(200, { "content-type": types[extname(path)] ?? "application/octet-stream" });
  createReadStream(path).pipe(res);
}).listen(0, "127.0.0.1");
await new Promise((r) => server.once("listening", r));
const b = await launch(join(tmpdir(), "mg-video-stills"), 9391);
try {
  mkdirSync(outDir, { recursive: true });
  await b.send("Page.enable");
  await b.send("Emulation.setDeviceMetricsOverride", { width: 1920, height: 1080, deviceScaleFactor: 1, mobile: false });
  await b.send("Page.navigate", { url: `http://127.0.0.1:${server.address().port}/${comp}?lang=${lang}` });
  const ev = async (expression) => {
    const r = await b.send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
    if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 500));
    return r.result.value;
  };
  for (let i = 0; i < 100 && !(await ev("!!window.__film").catch(() => false)); i++) await new Promise((r) => setTimeout(r, 100));
  await ev("window.__film.ready");
  console.log(await ev(`JSON.stringify({ captions: window.__film.spec().captions.map((c) => [c.at.toFixed(2), c.end.toFixed(2), c.text]), ends: window.__film.spec().windows.map((w) => w.segments.at(-1)).map((x) => (x.at + (x.to - x.from) / 30 / x.speed).toFixed(2)) })`));
  for (const t of times.split(",").map(Number)) {
    await ev(`window.__film.render(${t}).then(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))))`);
    const { data } = await b.send("Page.captureScreenshot", { format: "png" });
    writeFileSync(join(outDir, `t${t.toFixed(2).padStart(6, "0")}.png`), Buffer.from(data, "base64"));
  }
} finally {
  b.close();
  server.close();
}
