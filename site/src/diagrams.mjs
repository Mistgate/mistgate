// Diagrams on the site. Hand-drawn SVG files (docs/<lang>/diagrams/*.svg) are inlined, so their colours follow the
// site's theme toggle through the --mgd-* tokens in style.css; GitHub and the wiki show the same file through <img>,
// where the file's own light and dark fallbacks apply. Mermaid fences are rendered here at build time (GitHub renders
// them itself). Every inlined diagram gets its own id prefix, so two diagrams on one page never share an id.
import { readFileSync } from "node:fs";
import { renderMermaidSVG } from "beautiful-mermaid";

const esc = (s) => String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
let seq = 0;

function prefixIds(svg, p) {
  return svg
    .replace(/\sid="([^"]+)"/g, ` id="${p}-$1"`)
    .replace(/url\(#([^)]+)\)/g, `url(#${p}-$1)`)
    .replace(/(xlink:)?href="#([^"]+)"/g, (_, x = "", id) => `${x}href="#${p}-${id}"`)
    .replace(/aria-(labelledby|describedby)="([^"]+)"/g, (_, a, ids) => `aria-${a}="${ids.split(/\s+/).map((i) => `${p}-${i}`).join(" ")}"`);
}

/** The root <svg ...>: drop the fixed size (CSS sizes it, up to `scale` times its natural width), name it with `label`. */
function root(svg, label, cls, scale = 1) {
  return svg.replace(/<svg\b([^>]*)>/, (_, attrs) => {
    const width = /\swidth="([\d.]+)"/.exec(attrs)?.[1] ?? /viewBox="[\d.\s-]+?\s([\d.]+)\s[\d.]+"/.exec(attrs)?.[1];
    const desc = /<desc\s+id="([^"]+)"/.exec(svg)?.[1];
    const max = width ? `max-width:${Math.round(width * scale)}px` : "";
    let rest = attrs.replace(/\s(width|height|role|aria-label|aria-labelledby|aria-describedby)="[^"]*"/g, "").replace(/\sclass="([^"]*)"/, (_, c) => ` class="${c} ${cls}"`);
    if (!/\sclass=/.test(rest)) rest += ` class="${cls}"`;
    if (max) rest = /\sstyle="/.test(rest) ? rest.replace(/\sstyle="([^"]*)"/, (_, s) => ` style="${s.replace(/;?$/, ";")}${max}"`) : `${rest} style="${max}"`;
    return `<svg${rest} role="img" aria-label="${esc(label)}"${desc ? ` aria-describedby="${desc}"` : ""}>`;
  });
}

/** A hand-drawn diagram file, ready to inline. `alt` is the Markdown image text and becomes its accessible name. */
export function inlineSvg(file, alt) {
  let svg = readFileSync(file, "utf8").replace(/^﻿/, "").replace(/<\?xml[^>]*>\s*/, "").replace(/<!--[\s\S]*?-->\s*/g, "").trim();
  if (!svg.startsWith("<svg")) throw new Error(`${file}: not an SVG file`);
  svg = svg.replace(/<title[^>]*>[\s\S]*?<\/title>\s*/, ""); // no hover tooltip over the whole picture; alt names it
  return root(prefixIds(svg, `d${++seq}`), alt, "mgd-inline");
}

// colours as the site's tokens (style.css --dg-*), so a rendered diagram follows the theme like everything else
const MERMAID = { bg: "var(--dg-bg)", fg: "var(--dg-fg)", line: "var(--dg-line)", accent: "var(--dg-accent)", muted: "var(--dg-muted)", surface: "var(--dg-surface)", border: "var(--dg-border)", transparent: true, font: "Onest", padding: 16 };

/** A ```mermaid fence -> inline SVG. accTitle names it (GitHub reads the same line); without one, `fallback` does. */
export function mermaidSvg(src, fallback, warn) {
  const acc = {};
  const body = src.replace(/^[ \t]*acc(Title|Descr)\s*:\s*(.*)$/gm, (_, k, v) => { acc[k] = v.trim(); return ""; });
  if (!acc.Title) warn("a mermaid diagram without accTitle: screen readers get only a generic name");
  let svg = renderMermaidSVG(body, MERMAID);
  // the renderer's style block imports a web font and styles bare `svg` and `text`: keep it to this diagram, no fetch
  svg = svg.replace(/<style>([\s\S]*?)<\/style>/, (_, css) => `<style>${css.replace(/@import\s+url\([^)]*\)[^;]*;\s*/g, "").replace(/(^|[{}])\s*([^{}@]+?)\s*\{/g, (m, b, sel) => `${b}\n${sel.split(",").map((s) => (s.trim() === "svg" ? ".mgm" : `.mgm ${s.trim()}`)).join(", ")} {`)}</style>`);
  if (acc.Descr) svg = svg.replace(/(<svg\b[^>]*>)/, `$1<desc id="desc">${esc(acc.Descr)}</desc>`);
  return root(prefixIds(svg, `m${++seq}`), acc.Title ?? fallback, "mgm", 1.25); // its 11 px labels read better a little larger
}
