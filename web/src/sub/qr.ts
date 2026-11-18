import qrcode from "qrcode-generator";
import { roundedPath } from "../lib/qr-path";

const ns = "http://www.w3.org/2000/svg";

// ---- the board's QR card (.u-qr): rounded modules, a hole in the middle and the logo over it ----

type Grid = { n: number; dark: (x: number, y: number) => boolean };
const grids = new Map<string, Grid | null>();

/** The modules of `value`; "Q" corrects a quarter of the code, which is what the logo in the middle (about 7 %) needs. */
function grid(value: string): Grid | null {
  if (grids.has(value)) return grids.get(value)!;
  let out: Grid | null = null;
  try {
    const qr = qrcode(0, "Q");
    qr.addData(value);
    qr.make();
    out = { n: qr.getModuleCount(), dark: (x, y) => x >= 0 && y >= 0 && x < qr.getModuleCount() && y < qr.getModuleCount() && qr.isDark(y, x) };
  } catch {
    // too long for any QR version
  }
  grids.set(value, out);
  return out;
}

/**
 * The QR card of the boards: a white square with the code in rounded modules and `logo` (an image URL) in a clear square in the
 * middle (26.9 % of a 29-module code, 22.3 % of a 53-module one: 0.18 n + 2.6 modules). `big` is the larger card (Telegram, a key).
 * Null when the value does not fit a QR code. The code is the quiet zone's white card's child; no quiet zone is drawn inside.
 */
export function qrCard(value: string, label: string, logo: string, o: { big?: boolean; cls?: string } = {}): HTMLElement | null {
  const g = value ? grid(value) : null;
  if (!g) return null;
  const { n } = g;
  const logoModules = 0.18 * n + 2.6;
  const lo = (n - logoModules) / 2 - 0.5;
  const hi = (n + logoModules) / 2 + 0.5;
  const path = roundedPath(n, (x, y) => g.dark(x, y) && !(x + 1 > lo && x < hi && y + 1 > lo && y < hi));
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", `0 0 ${n} ${n}`);
  svg.setAttribute("role", "img");
  svg.setAttribute("aria-label", label);
  const p = document.createElementNS(ns, "path");
  p.setAttribute("fill-rule", "evenodd");
  p.setAttribute("d", path);
  svg.append(p);
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
  const inner = document.createElement("span");
  inner.className = "u-qi";
  inner.append(svg, mark);
  const card = document.createElement("span");
  card.className = `u-qr${o.big ? " big" : ""}${o.cls ? ` ${o.cls}` : ""}`;
  card.append(inner);
  return card;
}
