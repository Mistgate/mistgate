import badge from "../assets/mistgate-badge.svg?raw";
import type { MgData } from "./types";

// The name and the mark the page wears: the instance's own (the admin's settings), else the built-in badge.

export const brandName = (d: MgData) => d.title.trim() || (d.brand.parts.join("") || "VPN").replace(/^./, (c) => c.toUpperCase());

// The built-in badge (the admin's one). An <img> sees no page CSS variables, so the instance accent
// (a validated hex, see logic.ts) is written into the badge, which derives its other tones from it.
const builtin = (accent: string) => badge.replace("var(--mg-accent,#c3b2fd)", accent);

/** The instance uploaded its own logo (otherwise the page wears the built-in badge). */
export const hasCustomLogo = (d: MgData) => /^<svg[\s>]/i.test(d.brand.logo_svg.trim());

/** src for the logo <img> and the favicon: the instance's (server-sanitised) SVG, else the built-in badge. Never inlined as markup. */
export function logoSrc(d: MgData): string {
  let svg = d.brand.logo_svg.trim();
  if (!hasCustomLogo(d)) svg = builtin(d.brand.accent || "#b8acf2");
  else if (!/xmlns\s*=/.test(svg)) svg = svg.replace(/^<svg/i, '<svg xmlns="http://www.w3.org/2000/svg"');
  return "data:image/svg+xml," + encodeURIComponent(svg);
}
