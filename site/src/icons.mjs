// The admin panel's icons, read from its source so the docs never drift from it: stroke icons on a 24 grid
// (web/src/components/ui/icons.tsx `paths`) and the section icons with their pastel (`navPaths`).
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const SRC = fileURLToPath(new URL("../../web/src/components/ui/icons.tsx", import.meta.url));

export function parseIcons(tsx) {
  const block = (name) => {
    const at = tsx.indexOf(`const ${name} = {`);
    if (at < 0) throw new Error(`icons.tsx: no "const ${name} = {"`);
    return tsx.slice(at, tsx.indexOf("} as const", at));
  };
  const paths = Object.fromEntries([...block("paths").matchAll(/^\s+(\w+): "([^"]+)",/gm)].map((m) => [m[1], m[2]]));
  const nav = Object.fromEntries([...block("navPaths").matchAll(/(\w+): \[\s*"([^"]+)",\s*"(\w+)",?\s*\]/g)].map((m) => [m[1], { d: m[2], tone: m[3] }]));
  if (Object.keys(paths).length < 40 || !nav.overview || !nav.settings) throw new Error("icons.tsx: could not read the icon paths");
  return { paths, nav };
}

const panel = parseIcons(readFileSync(SRC, "utf8"));

/** Section icons and tones of the panel's sidebar: overview, nodes, users, profiles, subscriptions, health, updates, integrations, settings. */
export const NAV = panel.nav;

// the few the panel does not draw (theme toggle, drawer button)
const EXTRA = {
  menu: "M4 7h16M4 12h16M4 17h16",
  sun: "M12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM12 2.5v2M12 19.5v2M2.5 12h2M19.5 12h2M5.3 5.3l1.4 1.4M17.3 17.3l1.4 1.4M5.3 18.7l1.4-1.4M17.3 6.7l1.4-1.4",
  moon: "M20 14.5A8.5 8.5 0 0 1 9.5 4a8.5 8.5 0 1 0 10.5 10.5z",
};
const P = { ...panel.paths, ...EXTRA, ...Object.fromEntries(Object.entries(NAV).map(([k, v]) => [`nav-${k}`, v.d])) };

/** An inline stroke icon (decorative: aria-hidden). Names are the panel's (`externalLink`, `chevronRight` ...), `nav-<section>` or EXTRA. */
export function icon(name, size = 16, cls = "") {
  if (!P[name]) throw new Error(`unknown icon "${name}"`);
  return `<svg class="ico${cls ? " " + cls : ""}" viewBox="0 0 24 24" width="${size}" height="${size}" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="${P[name]}"/></svg>`;
}
