import { describe, expect, it } from "vitest";
import { assignRanks, clearOf, computeLayout, tabOrder, type LayoutNode } from "./layout";

const n = (id: string, h = 80, rank?: number): LayoutNode => ({ id, w: 200, h, rank });

describe("assignRanks", () => {
  it("uses the explicit rank when given", () => {
    const r = assignRanks([n("a", 80, 0), n("b", 80, 3)], []);
    expect(r.get("a")).toBe(0);
    expect(r.get("b")).toBe(3);
  });
  it("takes the longest path from the edges otherwise", () => {
    const r = assignRanks([n("a"), n("b"), n("c"), n("d")], [
      { from: "a", to: "b" },
      { from: "b", to: "c" },
      { from: "a", to: "c" },
      { from: "a", to: "d" },
    ]);
    expect([r.get("a"), r.get("b"), r.get("c"), r.get("d")]).toEqual([0, 1, 2, 1]);
  });
  it("survives cycles and self-loops", () => {
    const r = assignRanks([n("a"), n("b"), n("c")], [
      { from: "a", to: "b" },
      { from: "b", to: "c" },
      { from: "c", to: "a" },
      { from: "b", to: "b" },
    ]);
    expect(r.get("a")).toBe(0);
    expect(r.get("c")).toBe(2);
  });
});

describe("computeLayout", () => {
  const overlap = (a: { x: number; y: number }, wa: number, ha: number, b: { x: number; y: number }, wb: number, hb: number) =>
    a.x < b.x + wb && a.x + wa > b.x && a.y < b.y + hb && a.y + ha > b.y;

  it("puts ranks in columns left to right and never overlaps nodes", () => {
    const nodes = [n("p", 60, 0), n("e1", 120, 1), n("e2", 90, 1), n("s", 300, 2), n("x1", 70, 3), n("x2", 70, 3)];
    const edges = [
      { from: "p", to: "e1" },
      { from: "p", to: "e2" },
      { from: "e1", to: "s" },
      { from: "e2", to: "s" },
      { from: "s", to: "x1" },
      { from: "s", to: "x2" },
    ];
    const pos = computeLayout(nodes, edges);
    expect(pos.p!.x).toBeLessThan(pos.e1!.x);
    expect(pos.e1!.x).toBe(pos.e2!.x);
    expect(pos.e2!.x).toBeLessThan(pos.s!.x);
    expect(pos.s!.x).toBeLessThan(pos.x1!.x);
    for (const a of nodes) for (const b of nodes) if (a.id < b.id) expect(overlap(pos[a.id]!, a.w, a.h, pos[b.id]!, b.w, b.h)).toBe(false);
  });

  it("orders a column by the barycenter of its neighbours (no crossing for a simple swap)", () => {
    const nodes = [n("a1", 80, 0), n("a2", 80, 0), n("b1", 80, 1), n("b2", 80, 1)];
    // given in the order that would cross: a1->b2, a2->b1
    const pos = computeLayout(nodes, [
      { from: "a1", to: "b2" },
      { from: "a2", to: "b1" },
    ]);
    const a1Above = pos.a1!.y < pos.a2!.y;
    const b2Above = pos.b2!.y < pos.b1!.y;
    expect(a1Above).toBe(b2Above);
  });

  it("is stable for nodes without edges and handles an empty graph", () => {
    expect(computeLayout([], [])).toEqual({});
    const pos = computeLayout([n("a"), n("b")], []);
    expect(pos.a!.y).toBeLessThan(pos.b!.y);
    expect(pos.a!.x).toBe(pos.b!.x);
  });

  it("spaces rows by the measured heights", () => {
    const pos = computeLayout([n("a", 100, 0), n("b", 50, 0)], [], { rowGap: 20 });
    expect(pos.b!.y - pos.a!.y).toBe(120);
  });
});

describe("tabOrder and clearOf", () => {
  it("orders columns first, then top to bottom", () => {
    const order = tabOrder([
      { id: "c", x: 600, y: 0, w: 200 },
      { id: "b2", x: 300, y: 200, w: 200 },
      { id: "b1", x: 300, y: 10, w: 200 },
      { id: "a", x: 0, y: 300, w: 200 },
    ]);
    expect(order).toEqual(["a", "b1", "b2", "c"]);
  });
  it("moves a box down until it clears the taken ones", () => {
    const taken = [{ x: 0, y: 0, w: 200, h: 100 }, { x: 0, y: 124, w: 200, h: 100 }];
    const p = clearOf({ x: 10, y: 10, w: 150, h: 60 }, taken);
    expect(p.y).toBeGreaterThanOrEqual(224 + 24);
  });
});
