// helpers that write plain, hand-editable SVG for the docs diagrams (output goes to docs/<lang>/diagrams/*.svg)
import { readFileSync } from "node:fs";
import { parseIcons } from "../src/icons.mjs";

const { paths, nav } = parseIcons(readFileSync(new URL("../../web/src/components/ui/icons.tsx", import.meta.url), "utf8"));
export const ICON = { ...paths, ...Object.fromEntries(Object.entries(nav).map(([k, v]) => [`nav-${k}`, v.d])) };
export const esc = (s) => String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
const n = (v) => +(+v).toFixed(1);

// light and dark fallbacks (GitHub, the wiki, a viewer); the site overrides them with --mgd-* from its own tokens
const L = { bg: "#ffffff", box: "#f8f8f6", line: "#e3e3df", text: "#16161a", muted: "#6b6b73", lav: "#473a73", "lav-bg": "#eae8fc", sky: "#174e6a", "sky-bg": "#e0f0fa", mint: "#00582f", "mint-bg": "#dcf3e4", sage: "#3b511d", "sage-bg": "#e8f1e0", sand: "#5c4a12", "sand-bg": "#f5efdf", rose: "#67343e", "rose-bg": "#f9e6e8" };
const D = { bg: "#141417", box: "#0c0c0e", line: "#25252b", text: "#f3f3f5", muted: "#8b8b94", lav: "#b8acf2", "lav-bg": "#2d2b38", sky: "#8ec8ea", "sky-bg": "#263037", mint: "#7dd3a0", "mint-bg": "#243229", sage: "#b0cc90", "sage-bg": "#2c3126", sand: "#dcc68c", "sand-bg": "#332f25", rose: "#e6a6b0", "rose-bg": "#362a2c" };
const vars = (m) => Object.entries(m).map(([k, v]) => `--d-${k}:var(--mgd-${k},${v})`).join(";");

export const STYLE = `<style>
.mgd{${vars(L)}}
@media (prefers-color-scheme:dark){.mgd{${vars(D)}}}
.mgd text{font-family:Onest,ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;fill:var(--d-text)}
.mgd .bg{fill:var(--d-bg);stroke:var(--d-line)}
.mgd .card{fill:var(--d-box);stroke:var(--d-line)}
.mgd .pill{fill:var(--d-bg);stroke:var(--d-line)}
.mgd .h{font-size:15px;font-weight:800;letter-spacing:-.02em}
.mgd .t{font-size:13px;font-weight:700}
.mgd .s{font-size:13px;fill:var(--d-muted)}
.mgd .m{font-family:"JetBrains Mono",ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:11px;fill:var(--d-muted)}
.mgd .lav{--t:var(--d-lav);--tb:var(--d-lav-bg)}
.mgd .sky{--t:var(--d-sky);--tb:var(--d-sky-bg)}
.mgd .mint{--t:var(--d-mint);--tb:var(--d-mint-bg)}
.mgd .sage{--t:var(--d-sage);--tb:var(--d-sage-bg)}
.mgd .sand{--t:var(--d-sand);--tb:var(--d-sand-bg)}
.mgd .rose{--t:var(--d-rose);--tb:var(--d-rose-bg)}
.mgd .grey{--t:var(--d-muted);--tb:var(--d-line)}
.mgd .chip{fill:var(--tb)}
.mgd .ico{fill:none;stroke:var(--t);stroke-width:2.2;stroke-linecap:round;stroke-linejoin:round}
.mgd .edge{fill:var(--tb);stroke:var(--t);stroke-opacity:.45}
.mgd .flow{fill:none;stroke:var(--t);stroke-width:2;stroke-linecap:round;stroke-linejoin:round}
.mgd .wide{stroke-width:3}
.mgd .dash{stroke-dasharray:5 5}
.mgd .head{fill:var(--t)}
.mgd .badge{fill:var(--t)}
.mgd .bn{font-size:11px;font-weight:800;fill:var(--d-bg);text-anchor:middle}
.mgd .tt{fill:var(--t)}
.mgd .rule{stroke:var(--d-line)}
</style>`;

export function svg({ w, h, title, desc, body }) {
  return `<svg xmlns="http://www.w3.org/2000/svg" class="mgd" viewBox="0 0 ${w} ${h}" width="${w}" height="${h}" role="img" aria-labelledby="title desc">
<title id="title">${esc(title)}</title>
<desc id="desc">${esc(desc)}</desc>
${STYLE}
<rect class="bg" x="0.5" y="0.5" width="${w - 1}" height="${h - 1}" rx="16"/>
${body.flat(Infinity).filter(Boolean).join("\n")}
</svg>
`;
}

export const rect = (cls, x, y, w, h, rx = 12) => `<rect class="${cls}" x="${n(x)}" y="${n(y)}" width="${n(w)}" height="${n(h)}" rx="${rx}"/>`;
export const text = (cls, x, y, s, anchor) => `<text class="${cls}" x="${n(x)}" y="${n(y)}"${anchor ? ` text-anchor="${anchor}"` : ""}>${esc(s)}</text>`;
/** A tinted square with a panel icon (the panel's IconChip). */
export function chip(tone, icon, x, y, size = 24) {
  const i = Math.round(size * 0.6), o = (size - i) / 2;
  return `<g class="${tone}"><rect class="chip" x="${n(x)}" y="${n(y)}" width="${size}" height="${size}" rx="7"/><svg x="${n(x + o)}" y="${n(y + o)}" width="${i}" height="${i}" viewBox="0 0 24 24"><path class="ico" d="${ICON[icon]}"/></svg></g>`;
}
/** A box with a chip, a title and a mono line, like a row in the panel. */
export function item(tone, icon, x, y, w, h, title, sub, cls = "pill") {
  const cy = y + h / 2;
  return [rect(cls, x, y, w, h, 11), chip(tone, icon, x + 12, cy - 11, 22), text("t", x + 44, sub ? cy - 2 : cy + 4.5, title), sub ? text("m", x + 44, cy + 13, sub) : ""];
}
const HEAD = { r: (x, y) => `M${n(x)} ${n(y)}l-8 -4.5v9z`, l: (x, y) => `M${n(x)} ${n(y)}l8 -4.5v9z`, u: (x, y) => `M${n(x)} ${n(y)}l-4.5 8h9z`, d: (x, y) => `M${n(x)} ${n(y)}l-4.5 -8h9z` };
const dir = (a, b) => (a[0] === b[0] ? (b[1] > a[1] ? "d" : "u") : b[0] > a[0] ? "r" : "l");
/** An orthogonal line through points, with arrowheads (end, and start when `both`); the line stops under each head. */
export function flow(tone, pts, { both = false, cls = "" } = {}) {
  const p = pts.map((q) => [...q]);
  const trim = (a, b) => { const d = dir(a, b); if (d === "r") b[0] -= 7; if (d === "l") b[0] += 7; if (d === "d") b[1] -= 7; if (d === "u") b[1] += 7; };
  const end = [...p[p.length - 1]], endDir = dir(p[p.length - 2], p[p.length - 1]);
  const start = [...p[0]], startDir = dir(p[1], p[0]);
  trim(p[p.length - 2], p[p.length - 1]);
  if (both) trim(p[1], p[0]);
  const d = p.map((q, i) => `${i ? "L" : "M"}${n(q[0])} ${n(q[1])}`).join("");
  return `<g class="${tone}"><path class="flow ${cls}" d="${d}"/><path class="head" d="${HEAD[endDir](...end)}"/>${both ? `<path class="head" d="${HEAD[startDir](...start)}"/>` : ""}</g>`;
}
export const badge = (tone, x, y, num) => `<g class="${tone}"><circle class="badge" cx="${n(x)}" cy="${n(y)}" r="10"/><text class="bn" x="${n(x)}" y="${n(y + 4)}">${num}</text></g>`;
/** Estimated width of a short label: `latin` px per character, `cjk` px per Han or full-width character (about 1 em). */
const CJK = /[\u2e80-\u9fff\uf900-\ufaff\uff00-\uffef]/;
export const textWidth = (s, latin, cjk) => [...s].reduce((a, c) => a + (CJK.test(c) ? cjk : latin), 0);
/** The panel's status pill: a tinted capsule, a filled dot with a check or a dash, the word. `right` anchors it at its right edge. */
export function yn(yes, x, y, word, right = false) {
  const w = Math.round(30 + textWidth(word, 6.4, 11.2)), x0 = right ? x - w : x;
  const g = yes ? `<path d="M${n(x0 + 9.5)} ${n(y + 11)}l2 2 3.5 -4" fill="none" stroke="var(--d-bg)" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>` : `<path d="M${n(x0 + 9.5)} ${n(y + 11)}h5" stroke="var(--d-bg)" stroke-width="1.8" stroke-linecap="round"/>`;
  return `<g class="${yes ? "mint" : "grey"}"><rect class="chip" x="${n(x0)}" y="${n(y)}" width="${w}" height="22" rx="11"/><circle class="badge" cx="${n(x0 + 12)}" cy="${n(y + 11)}" r="7"/>${g}<text class="tt" x="${n(x0 + 24)}" y="${n(y + 15)}" font-size="11" font-weight="700">${esc(word)}</text></g>`;
}
export const lines =(cls, x, y, arr, lh = 17, anchor) => arr.map((s, i) => text(cls, x, y + i * lh, s, anchor));
