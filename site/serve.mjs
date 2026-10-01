// Local static server for dist/ (like a static host: /x/ -> /x/index.html, 404.html for misses).
// Usage: node serve.mjs [port]   (binds 127.0.0.1 only)
import { createServer } from "node:http";
import { readFile, stat } from "node:fs/promises";
import { join, extname, normalize } from "node:path";
import { fileURLToPath } from "node:url";

const DIST = join(fileURLToPath(new URL(".", import.meta.url)), "dist");
const port = Number(process.argv[2] ?? 4173);
const TYPES = { ".html": "text/html; charset=utf-8", ".css": "text/css", ".js": "text/javascript", ".json": "application/json", ".svg": "image/svg+xml", ".png": "image/png", ".woff2": "font/woff2", ".xml": "application/xml", ".txt": "text/plain; charset=utf-8" };

createServer(async (req, res) => {
  let p = normalize(decodeURIComponent(new URL(req.url, "http://x").pathname)).replace(/^([/\\]\.\.)+/, "");
  let file = join(DIST, p);
  try { if ((await stat(file)).isDirectory()) { if (!p.endsWith("/") && !p.endsWith("\\")) { res.writeHead(301, { Location: p + "/" }).end(); return; } file = join(file, "index.html"); } } catch {}
  try {
    const data = await readFile(file);
    res.writeHead(200, { "Content-Type": TYPES[extname(file)] ?? "application/octet-stream" }).end(data);
  } catch {
    res.writeHead(404, { "Content-Type": "text/html; charset=utf-8" }).end(await readFile(join(DIST, "404.html")).catch(() => "not found"));
  }
}).listen(port, "127.0.0.1", () => console.log(`http://127.0.0.1:${port}/`));
