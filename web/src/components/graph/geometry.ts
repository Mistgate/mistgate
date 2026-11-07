import type { Pos, View } from "./types";

export interface Cubic {
  p0: Pos;
  p1: Pos;
  p2: Pos;
  p3: Pos;
}
export interface Route {
  segments: Cubic[];
  d: string;
  /** The point the label and the menu hang on. */
  mid: Pos;
  backward: boolean;
}

export const MIN_ZOOM = 0.3;
export const MAX_ZOOM = 2;
export const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));
const r2 = (n: number) => Math.round(n * 100) / 100;
const pt = (p: Pos) => `${r2(p.x)} ${r2(p.y)}`;

export function cubicPoint(c: Cubic, t: number): Pos {
  const u = 1 - t;
  const a = u * u * u;
  const b = 3 * u * u * t;
  const d = 3 * u * t * t;
  const e = t * t * t;
  return { x: a * c.p0.x + b * c.p1.x + d * c.p2.x + e * c.p3.x, y: a * c.p0.y + b * c.p1.y + d * c.p2.y + e * c.p3.y };
}

/** A target further left than this (relative to the source) makes a backward wire. */
export const BACKWARD_GAP = 16;

/**
 * The wire from an output (a) to an input (b): a cubic Bezier with horizontal tangents at both ends. When the target is
 * left of the source the wire leaves to the right, swings through a horizontal run at `clearY` (below both cards by
 * default) and comes into the target from the left, so it loops around the cards instead of crossing them.
 */
export function routeWire(a: Pos, b: Pos, opts: { clearY?: number } = {}): Route {
  const dx = b.x - a.x;
  if (dx >= BACKWARD_GAP) {
    const k = Math.max(40, dx * 0.5);
    const c: Cubic = { p0: a, p1: { x: a.x + k, y: a.y }, p2: { x: b.x - k, y: b.y }, p3: b };
    return { segments: [c], d: `M${pt(a)} C${pt(c.p1)} ${pt(c.p2)} ${pt(b)}`, mid: cubicPoint(c, 0.5), backward: false };
  }
  const k = 56;
  const dy = Math.abs(b.y - a.y);
  const ym = dy >= 120 ? (a.y + b.y) / 2 : (opts.clearY ?? Math.max(a.y, b.y) + 96);
  const px = (a.x + b.x) / 2;
  const h = Math.max(28, Math.abs(a.x - b.x) * 0.3);
  const s1: Cubic = { p0: a, p1: { x: a.x + k, y: a.y }, p2: { x: px + h, y: ym }, p3: { x: px, y: ym } };
  const s2: Cubic = { p0: { x: px, y: ym }, p1: { x: px - h, y: ym }, p2: { x: b.x - k, y: b.y }, p3: b };
  return {
    segments: [s1, s2],
    d: `M${pt(a)} C${pt(s1.p1)} ${pt(s1.p2)} ${pt(s1.p3)} C${pt(s2.p1)} ${pt(s2.p2)} ${pt(b)}`,
    mid: { x: px, y: ym },
    backward: true,
  };
}

export function flattenRoute(r: Route, perSegment = 24): Pos[] {
  const out: Pos[] = [];
  r.segments.forEach((s, i) => {
    for (let n = i === 0 ? 0 : 1; n <= perSegment; n++) out.push(cubicPoint(s, n / perSegment));
  });
  return out;
}

export function distToSegment(p: Pos, a: Pos, b: Pos): number {
  const vx = b.x - a.x;
  const vy = b.y - a.y;
  const len2 = vx * vx + vy * vy;
  const t = len2 === 0 ? 0 : clamp(((p.x - a.x) * vx + (p.y - a.y) * vy) / len2, 0, 1);
  return Math.hypot(p.x - (a.x + t * vx), p.y - (a.y + t * vy));
}

export function distanceToRoute(p: Pos, r: Route): number {
  const pts = flattenRoute(r);
  let best = Infinity;
  for (let i = 1; i < pts.length; i++) best = Math.min(best, distToSegment(p, pts[i - 1]!, pts[i]!));
  return best;
}

/** The id of the wire closest to p within `tol` world units, or null. */
export function hitEdge(p: Pos, routes: Map<string, Route>, tol: number): string | null {
  let best: string | null = null;
  let bestD = tol;
  for (const [id, r] of routes) {
    const d = distanceToRoute(p, r);
    if (d <= bestD) {
      bestD = d;
      best = id;
    }
  }
  return best;
}

/** The nearest candidate within `radius` of p (ties: the first). */
export function nearestPoint<T extends Pos>(p: Pos, candidates: T[], radius: number): T | null {
  let best: T | null = null;
  let bestD = radius;
  for (const c of candidates) {
    const d = Math.hypot(c.x - p.x, c.y - p.y);
    if (d <= bestD) {
      bestD = d;
      best = c;
    }
  }
  return best;
}

export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}
export const rectsIntersect = (a: Rect, b: Rect) => a.x < b.x + b.w && a.x + a.w > b.x && a.y < b.y + b.h && a.y + a.h > b.y;
export function boundsOf(rects: Rect[]): Rect | null {
  if (!rects.length) return null;
  let x0 = Infinity;
  let y0 = Infinity;
  let x1 = -Infinity;
  let y1 = -Infinity;
  for (const r of rects) {
    x0 = Math.min(x0, r.x);
    y0 = Math.min(y0, r.y);
    x1 = Math.max(x1, r.x + r.w);
    y1 = Math.max(y1, r.y + r.h);
  }
  return { x: x0, y: y0, w: x1 - x0, h: y1 - y0 };
}

/** Zoom to `z` keeping the world point under the screen point `p` (relative to the canvas) still. */
export function zoomAround(v: View, p: Pos, z: number): View {
  const nz = clamp(z, MIN_ZOOM, MAX_ZOOM);
  const wx = (p.x - v.x) / v.z;
  const wy = (p.y - v.y) / v.z;
  return { x: p.x - wx * nz, y: p.y - wy * nz, z: nz };
}

/** The view that shows `b` centred with a margin; never zoomed in past 100%. */
export function fitView(b: Rect | null, size: { w: number; h: number }, pad = 32): View {
  if (!b || size.w <= 0 || size.h <= 0) return { x: 0, y: 0, z: 1 };
  const z = clamp(Math.min((size.w - pad * 2) / Math.max(b.w, 1), (size.h - pad * 2) / Math.max(b.h, 1), 1), MIN_ZOOM, MAX_ZOOM);
  return { z, x: size.w / 2 - (b.x + b.w / 2) * z, y: size.h / 2 - (b.y + b.h / 2) * z };
}

/** The wire while it is dragged: always one forward cubic from the output end (a) to the input end (b), even when b is left of a. */
export function dragWire(a: Pos, b: Pos): string {
  const k = Math.max(40, Math.abs(b.x - a.x) * 0.5);
  return `M${pt(a)} C${pt({ x: a.x + k, y: a.y })} ${pt({ x: b.x - k, y: b.y })} ${pt(b)}`;
}

export const snapTo =(v: number, grid = 8) => Math.round(v / grid) * grid;
