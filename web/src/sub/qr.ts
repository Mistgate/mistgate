import qrcode from "qrcode-generator";

const ns = "http://www.w3.org/2000/svg";
const cache = new Map<string, { n: number; d: string } | null>();

function cells(value: string) {
  if (cache.has(value)) return cache.get(value)!;
  let out: { n: number; d: string } | null = null;
  try {
    const qr = qrcode(0, "M");
    qr.addData(value);
    qr.make();
    const n = qr.getModuleCount();
    let d = "";
    for (let y = 0; y < n; y++) for (let x = 0; x < n; x++) if (qr.isDark(y, x)) d += `M${x} ${y}h1v1h-1z`;
    out = { n, d };
  } catch {
    // too long for any QR version: the caller falls back to the copy button
  }
  cache.set(value, out);
  return out;
}

/** The QR code as an SVG (cells in currentColor; the caller puts it on a white card). Null if it does not fit. */
export function qrSvg(value: string, label: string): SVGSVGElement | null {
  const c = value ? cells(value) : null;
  if (!c) return null;
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", `0 0 ${c.n} ${c.n}`);
  svg.setAttribute("shape-rendering", "crispEdges");
  svg.setAttribute("role", "img");
  svg.setAttribute("aria-label", label);
  const path = document.createElementNS(ns, "path");
  path.setAttribute("d", c.d);
  path.setAttribute("fill", "currentColor");
  svg.append(path);
  return svg;
}
