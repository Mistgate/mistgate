import { useSyncExternalStore } from "react";
import { writePref } from "./storage";

// Brand is instance data, not product code: the panel ships a neutral "mist" + "gate" wordmark and the
// Mistgate badge (assets/mistgate-badge*.svg, drawn by the owner, inlined and tinted by the accent, see
// components/brand.tsx), and the
// instance owner can replace both (wordmark parts and a logo SVG).
// lib/instance.ts reads them from the panel on boot (GetLoginInfo is public, the sign-in page shows the
// brand) and from Settings -> Interface, and passes them to setBrand().

/**
 * The contract for an instance-uploaded logo (the built-in mark has gradients and is not re-tinted):
 * an SVG with up to three fills. White stays white; #cba5fa is the "light" tone and
 * #693fc2 the "dark" tone, both re-tinted from the accent (--logo-l / --logo-d in index.css). Any other
 * fill is kept as it is.
 */
const tones: Record<string, string> = {
  "#ffffff": "var(--logo-w)",
  "#fff": "var(--logo-w)",
  "#cba5fa": "var(--logo-l)",
  "#693fc2": "var(--logo-d)",
};

// Plain shapes only: no script, style, image, use, foreignObject, links or event handlers.
const allowedTags = new Set([
  "svg", "g", "path", "rect", "circle", "ellipse", "line", "polygon", "polyline",
  "defs", "clippath", "mask", "lineargradient", "radialgradient", "stop", "title",
]); // prettier-ignore
const allowedAttrs = new Set([
  "xmlns", "viewbox", "d", "fill", "fill-rule", "clip-rule", "opacity", "fill-opacity", "transform",
  "x", "y", "width", "height", "rx", "ry", "cx", "cy", "r", "points", "x1", "y1", "x2", "y2",
  "id", "clip-path", "mask", "offset", "stop-color", "stop-opacity", "gradientunits", "gradienttransform",
  "fx", "fy", "spreadmethod",
]); // prettier-ignore
const safeValue = /^[#\w\s.,%()+-]*$/;
const maxLogoBytes = 256 * 1024;

/**
 * Turns an instance-provided SVG into a safe inline string with the tone fills bound to CSS variables,
 * or null when it is not a usable SVG. Everything outside the whitelist above is dropped.
 */
export function sanitizeLogo(source: string): string | null {
  if (source.length > maxLogoBytes) return null;
  const doc = new DOMParser().parseFromString(source, "image/svg+xml");
  const root = doc.documentElement;
  if (root.localName !== "svg" || doc.querySelector("parsererror")) return null;

  const walk = (el: Element) => {
    for (const child of Array.from(el.children)) {
      if (!allowedTags.has(child.localName.toLowerCase())) child.remove();
      else walk(child);
    }
    for (const attr of Array.from(el.attributes)) {
      const name = attr.name.toLowerCase();
      const value = attr.value.trim();
      // url() may only point at the same document (gradients, clip paths).
      const badUrl = /url\(/i.test(value) && !/^url\(#[\w-]+\)$/.test(value);
      if (!allowedAttrs.has(name) || !safeValue.test(value) || badUrl) el.removeAttribute(attr.name);
      else if (name === "fill" && tones[value.toLowerCase()]) {
        el.removeAttribute(attr.name);
        el.setAttribute("style", `fill:${tones[value.toLowerCase()]}`);
      }
    }
  };
  walk(root);
  root.setAttribute("aria-hidden", "true");
  root.setAttribute("focusable", "false");
  root.setAttribute("width", "100%");
  root.setAttribute("height", "100%");
  return new XMLSerializer().serializeToString(root);
}

export type Brand = {
  /** Two parts of the wordmark; the second is drawn in the accent. */
  wordmark: readonly [string, string];
  /** Display name for running text: the wordmark parts joined, first letter capitalised. */
  name: string;
  /** Sanitized inline SVG of an instance-uploaded logo (see sanitizeLogo); null = the built-in Mistgate mark. */
  logo: string | null;
};

/** localStorage key of the last applied wordmark and (sanitized) logo, for the splash: {"w": [head, tail], "l": svg | null}. */
export const brandCacheKey = "brand";

const displayName = (w: readonly [string, string]) => (w[0] + w[1]).replace(/^./, (c) => c.toUpperCase());
const listeners = new Set<() => void>();
let brand: Brand = { wordmark: ["mist", "gate"], name: "Mistgate", logo: null };

/**
 * Applies instance branding. Invalid parts are ignored, so a bad logo never blanks the UI;
 * `logoSvg: null` goes back to the built-in mark (the owner removed the custom one).
 */
export function setBrand(next: { wordmark?: readonly [string, string]; logoSvg?: string | null }) {
  const [a, b] = next.wordmark ?? brand.wordmark;
  // undefined or an unusable SVG keeps the current logo; null goes back to the built-in mark
  const logo = next.logoSvg === null ? null : next.logoSvg ? (sanitizeLogo(next.logoSvg) ?? undefined) : undefined;
  const wordmark = [a.slice(0, 24), b.slice(0, 24)] as const;
  brand = { wordmark, name: displayName(wordmark), logo: logo === undefined ? brand.logo : logo };
  document.title = brand.name;
  // The splash (index.html, public/splash.js) paints before any of this code runs; it reads the last brand from here.
  writePref(brandCacheKey, JSON.stringify({ w: wordmark, l: brand.logo }));
  listeners.forEach((l) => l());
}

function subscribe(cb: () => void) {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

export function useBrand(): Brand {
  return useSyncExternalStore(subscribe, () => brand);
}
