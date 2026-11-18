import { roundedPath } from "../lib/qr-path";
import type { Qr } from "./types";

const ns = "http://www.w3.org/2000/svg";

// ---- the board's QR card (.u-qr): rounded modules, a hole in the middle and the logo over it ----
// The server encodes (internal/qr) and says which modules are dark and how big the hole is; the page only draws them, with DOM
// calls: nothing of the data is ever parsed as markup.

/**
 * The QR card of the boards: a white square with the code in rounded modules and `logo` (an image URL) in a clear square in the
 * middle, as big as the server made the hole (26.9 % of a 29-module code, 22.3 % of a 53-module one: 0.18 n + 2.6 modules, less
 * where the code could not spare the error correction). `big` is the larger card (Telegram, a key). Null when there is no code
 * (the value did not fit a QR code, or the server drew none). No quiet zone is drawn inside; the white card is it.
 */
export function qrCard(code: Qr | null, label: string, logo: string, o: { big?: boolean; cls?: string } = {}): HTMLElement | null {
  if (!code) return null;
  const { n, window: logoModules } = code;
  let bytes: Uint8Array;
  try {
    bytes = Uint8Array.from(atob(code.bits), (c) => c.charCodeAt(0));
  } catch {
    return null;
  }
  const dark = (x: number, y: number) => x >= 0 && y >= 0 && x < n && y < n && (((bytes[(y * n + x) >> 3] ?? 0) >> (7 - ((y * n + x) & 7))) & 1) === 1;
  const lo = (n - logoModules) / 2 - 0.5;
  const hi = (n + logoModules) / 2 + 0.5;
  const path = roundedPath(n, (x, y) => dark(x, y) && !(logoModules > 0 && x + 1 > lo && x < hi && y + 1 > lo && y < hi));
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", `0 0 ${n} ${n}`);
  svg.setAttribute("role", "img");
  svg.setAttribute("aria-label", label);
  const p = document.createElementNS(ns, "path");
  p.setAttribute("fill-rule", "evenodd");
  p.setAttribute("d", path);
  svg.append(p);
  const inner = document.createElement("span");
  inner.className = "u-qi";
  inner.append(svg);
  if (logoModules > 0) {
    const pct = `${((logoModules / n) * 100).toFixed(2)}%`;
    const img = document.createElement("img");
    img.src = logo;
    img.alt = "";
    img.setAttribute("draggable", "false");
    const mark = document.createElement("span");
    mark.className = "u-qt";
    mark.style.width = pct;
    mark.style.height = pct;
    mark.append(img);
    inner.append(mark);
  }
  const card = document.createElement("span");
  card.className = `u-qr${o.big ? " big" : ""}${o.cls ? ` ${o.cls}` : ""}`;
  card.append(inner);
  return card;
}
