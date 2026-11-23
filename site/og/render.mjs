// og.png: the picture the link previews show (1200x630), drawn from the brand badge and the site's font.
//   CHROME=<chrome-headless-shell> node og/render.mjs        writes src/assets/og.png
// The text is set here; change it and run again, then commit the png. (A headless shell, not --headless=new.)
import { readFileSync, writeFileSync, mkdtempSync, rmSync } from "node:fs";
import { join, dirname } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath, pathToFileURL } from "node:url";
import { spawnSync } from "node:child_process";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");
const BRAND = join(ROOT, "..", "web", "src");
const badge = readFileSync(join(BRAND, "assets", "mistgate-badge.svg"), "utf8");
const font = (f) => pathToFileURL(join(BRAND, "fonts", f)).href;
const ACC = "#7dd3a0";
const html = `<!doctype html><meta charset="utf-8"><style>
@font-face{font-family:Onest;src:url(${font("onest-latin.woff2")}) format("woff2");font-weight:100 900}
html,body{margin:0;background:#0d0f0e;overflow:hidden}
body{width:1200px;height:630px;position:relative;font-family:Onest,system-ui,sans-serif;color:#eef1ee}
svg.mg-badge{--mg-accent:${ACC};display:block;width:150px;height:150px}
.row{position:absolute;left:84px;top:150px;display:flex;align-items:center;gap:32px}
.w{font-size:124px;font-weight:700;letter-spacing:-.03em;line-height:1}.w b{color:${ACC};font-weight:700}
.s{position:absolute;left:84px;top:362px;font-size:42px;color:#b6bdb8;line-height:1.3}
.u{position:absolute;left:84px;bottom:64px;font-size:30px;font-weight:500;color:${ACC}}
.arcs{position:absolute;right:40px;bottom:0;width:470px;height:470px}
</style><div class="row">${badge}<div class="w">mist<b>gate</b></div></div>
<div class="s">Self-hosted panel for a small VPN</div><div class="u">mistgate.app</div>
<svg class="arcs" viewBox="0 0 500 500" fill="none"><path d="M40 500V250a210 210 0 0 1 420 0v250" stroke="#fff" stroke-opacity=".07" stroke-width="2"/><path d="M110 500V260a140 140 0 0 1 280 0v240" stroke="#fff" stroke-opacity=".10" stroke-width="2"/><path d="M180 500V270a70 70 0 0 1 140 0v230" stroke="${ACC}" stroke-opacity=".7" stroke-width="2"/></svg>`;

const dir = mkdtempSync(join(tmpdir(), "mg-og-"));
const page = join(dir, "og.html"), shot = join(dir, "og.png");
writeFileSync(page, html);
const r = spawnSync(process.env.CHROME, ["--headless", "--disable-gpu", "--hide-scrollbars", "--force-device-scale-factor=1", "--allow-file-access-from-files", "--window-size=1200,630", "--virtual-time-budget=3000", `--screenshot=${shot}`, pathToFileURL(page).href], { stdio: "inherit" });
if (r.status !== 0) { console.error("chrome failed"); process.exit(1); }
writeFileSync(join(ROOT, "src", "assets", "og.png"), readFileSync(shot));
rmSync(dir, { recursive: true, force: true });
console.log("wrote src/assets/og.png");
