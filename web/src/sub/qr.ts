import qrcode from "qrcode-generator";

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
 * The board's path: a run of dark modules in a row is one shape, its corners are round (radius half a module) where the module
 * above (below) the end of the run is light. The modules under the logo are left out.
 */
function roundedPath(g: Grid, hole: (x: number, y: number) => boolean): string {
  const { n } = g;
  const on = (x: number, y: number) => g.dark(x, y) && !hole(x, y);
  const r = 0.5;
  let d = "";
  for (let y = 0; y < n; y++) {
    let x = 0;
    while (x < n) {
      if (!on(x, y)) {
        x++;
        continue;
      }
      let e = x;
      while (e + 1 < n && on(e + 1, y)) e++;
      const x1 = e + 1;
      const tl = !on(x, y - 1), tr = !on(e, y - 1), bl = !on(x, y + 1), br = !on(e, y + 1);
      d += `M${x + (tl ? r : 0)} ${y}H${x1 - (tr ? r : 0)}${tr ? `a${r} ${r} 0 0 1 ${r} ${r}` : ""}V${y + 1 - (br ? r : 0)}${br ? `a${r} ${r} 0 0 1 ${-r} ${r}` : ""}H${x + (bl ? r : 0)}${bl ? `a${r} ${r} 0 0 1 ${-r} ${-r}` : ""}V${y + (tl ? r : 0)}${tl ? `a${r} ${r} 0 0 1 ${r} ${-r}` : ""}z`;
      x = x1;
    }
  }
  return d;
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
  const path = roundedPath(g, (x, y) => x + 1 > lo && x < hi && y + 1 > lo && y < hi);
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
