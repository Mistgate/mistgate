import badge from "../assets/mistgate-badge.svg?raw";
import { h } from "./dom";
import { hasCustomLogo, logoSrc } from "./view";
import type { MgData } from "./types";

// The page's logo with an intro (motion rules: assets/mark-motion.css, plain CSS on the inlined badge). The built-in
// badge is inlined so its parts can move; an uploaded logo stays an <img> (it is never inlined as markup) and only its
// container fades and scales. prefers-reduced-motion: the CSS shows the static logo.

/** The built-in badge as markup: a constant of this bundle, never anything the server sent. Tones follow the page's --accent. */
const inline = badge.replace(' width="512" height="512"', ' width="100%" height="100%"').replace(' role="img" aria-label="Mistgate"', ' aria-hidden="true" focusable="false"');

/** A new element: the logo at `size` px, playing its intro when `intro` (a mark that is built again on every redraw should not replay). */
export function logoMark(d: MgData, size: number, intro = true): HTMLElement {
  const custom = hasCustomLogo(d);
  const box = h("span", {
    class: "mg-motion",
    "data-mode": intro ? "intro" : "rest",
    "data-kind": custom ? "custom" : "builtin",
    "aria-hidden": "true",
    style: { width: `${size}px`, height: `${size}px`, "--mg-accent": "var(--accent)" },
  });
  if (custom) box.append(h("img", { src: logoSrc(d), alt: "", width: size, height: size, draggable: "false" }));
  else box.innerHTML = inline;
  return box;
}
