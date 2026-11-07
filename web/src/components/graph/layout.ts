import type { Positions } from "./types";

export interface LayoutNode {
  id: string;
  rank?: number;
  w: number;
  h: number;
}
export interface LayoutEdge {
  from: string;
  to: string;
}
export interface LayoutOptions {
  colGap?: number;
  rowGap?: number;
  originX?: number;
  originY?: number;
}

/** Columns: the explicit rank when given, otherwise the longest path from the edges (back edges of cycles are ignored). */
export function assignRanks(nodes: LayoutNode[], edges: LayoutEdge[]): Map<string, number> {
  const ids = new Set(nodes.map((n) => n.id));
  const out = new Map<string, string[]>();
  for (const e of edges) {
    if (!ids.has(e.from) || !ids.has(e.to) || e.from === e.to) continue;
    (out.get(e.from) ?? out.set(e.from, []).get(e.from)!).push(e.to);
  }
  // drop back edges (DFS), leaving a DAG
  const state = new Map<string, 1 | 2>();
  const preds = new Map<string, string[]>();
  const visit = (u: string) => {
    state.set(u, 1);
    for (const v of out.get(u) ?? []) {
      const s = state.get(v);
      if (s === 1) continue;
      (preds.get(v) ?? preds.set(v, []).get(v)!).push(u);
      if (s === undefined) visit(v);
    }
    state.set(u, 2);
  };
  for (const n of nodes) if (!state.has(n.id)) visit(n.id);
  const own = new Map(nodes.map((n) => [n.id, n.rank]));
  const rank = new Map<string, number>();
  const calc = (id: string): number => {
    const known = rank.get(id);
    if (known !== undefined) return known;
    let r = 0;
    for (const p of preds.get(id) ?? []) r = Math.max(r, calc(p) + 1);
    const fixed = own.get(id);
    if (fixed !== undefined) r = fixed;
    rank.set(id, r);
    return r;
  };
  for (const n of nodes) calc(n.id);
  return rank;
}

/** Columns by rank, rows ordered by the barycenter of the neighbours (a few sweeps), spaced by measured height, centred. */
export function computeLayout(nodes: LayoutNode[], edges: LayoutEdge[], opts: LayoutOptions = {}): Positions {
  const colGap = opts.colGap ?? 96;
  const rowGap = opts.rowGap ?? 24;
  const ox = opts.originX ?? 0;
  const oy = opts.originY ?? 0;
  if (!nodes.length) return {};
  const rank = assignRanks(nodes, edges);
  const ranks = [...new Set(nodes.map((n) => rank.get(n.id)!))].sort((a, b) => a - b);
  const cols: LayoutNode[][] = ranks.map((r) => nodes.filter((n) => rank.get(n.id) === r));
  const colOf = new Map<string, number>();
  cols.forEach((c, i) => c.forEach((n) => colOf.set(n.id, i)));

  const adj = new Map<string, string[]>();
  for (const e of edges) {
    if (!colOf.has(e.from) || !colOf.has(e.to) || e.from === e.to) continue;
    (adj.get(e.from) ?? adj.set(e.from, []).get(e.from)!).push(e.to);
    (adj.get(e.to) ?? adj.set(e.to, []).get(e.to)!).push(e.from);
  }
  const index = new Map<string, number>();
  const reindex = () => cols.forEach((c) => c.forEach((n, i) => index.set(n.id, i)));
  reindex();
  const sweep = (ci: number, toward: number) => {
    const col = cols[ci]!;
    const bary = new Map<string, number>();
    col.forEach((n, i) => {
      const ns = (adj.get(n.id) ?? []).filter((m) => colOf.get(m) === toward);
      bary.set(n.id, ns.length ? ns.reduce((s, m) => s + index.get(m)!, 0) / ns.length : i);
    });
    col.sort((a, b) => bary.get(a.id)! - bary.get(b.id)! || index.get(a.id)! - index.get(b.id)!);
    reindex();
  };
  for (let pass = 0; pass < 3; pass++) {
    for (let c = 1; c < cols.length; c++) sweep(c, c - 1);
    for (let c = cols.length - 2; c >= 0; c--) sweep(c, c + 1);
  }

  const heights = cols.map((c) => c.reduce((s, n) => s + n.h, 0) + rowGap * (c.length - 1));
  const tallest = Math.max(...heights);
  const out: Positions = {};
  let x = ox;
  cols.forEach((col, ci) => {
    let y = oy + (tallest - heights[ci]!) / 2;
    for (const n of col) {
      out[n.id] = { x, y };
      y += n.h + rowGap;
    }
    x += Math.max(...col.map((n) => n.w)) + colGap;
  });
  return out;
}

/** Tab order: columns left to right (by centre x, bucketed), then top to bottom. */
export function tabOrder(nodes: { id: string; x: number; y: number; w: number }[]): string[] {
  const bucket = (n: { x: number; w: number }) => Math.round((n.x + n.w / 2) / 160);
  return [...nodes].sort((a, b) => bucket(a) - bucket(b) || a.y - b.y || a.x - b.x).map((n) => n.id);
}

type Box = { x: number; y: number; w: number; h: number };
/** Moves `p` down until its box clears every box in `taken`; for nodes added to an existing layout. */
export function clearOf(p: Box, taken: Box[], gap = 24): { x: number; y: number } {
  let y = p.y;
  for (let guard = 0; guard < 200; guard++) {
    const hit = taken.find((t) => p.x < t.x + t.w + gap && p.x + p.w + gap > t.x && y < t.y + t.h + gap && y + p.h + gap > t.y);
    if (!hit) break;
    y = hit.y + hit.h + gap;
  }
  return { x: p.x, y };
}
