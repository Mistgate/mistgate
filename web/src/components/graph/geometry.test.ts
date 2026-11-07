import { describe, expect, it } from "vitest";
import { boundsOf, cubicPoint, distanceToRoute, distToSegment, dragWire, fitView, flattenRoute, hitEdge, nearestPoint, rectsIntersect, routeWire, snapTo, zoomAround, MAX_ZOOM, MIN_ZOOM } from "./geometry";

describe("routeWire", () => {
  it("leaves and arrives horizontally on a forward wire", () => {
    const a = { x: 0, y: 0 };
    const b = { x: 300, y: 120 };
    const r = routeWire(a, b);
    expect(r.backward).toBe(false);
    expect(r.segments).toHaveLength(1);
    const c = r.segments[0]!;
    expect(c.p0).toEqual(a);
    expect(c.p3).toEqual(b);
    expect(c.p1.y).toBe(a.y);
    expect(c.p1.x).toBeGreaterThan(a.x);
    expect(c.p2.y).toBe(b.y);
    expect(c.p2.x).toBeLessThan(b.x);
    expect(r.d.startsWith("M0 0 C")).toBe(true);
    expect(r.mid.x).toBeCloseTo(150, 5);
    expect(r.mid.y).toBeCloseTo(60, 5);
  });

  it("keeps a minimum handle on a short forward wire", () => {
    const c = routeWire({ x: 0, y: 0 }, { x: 20, y: 0 }).segments[0]!;
    expect(c.p1.x).toBeGreaterThanOrEqual(40);
  });

  it("loops a backward wire out to the right and in from the left, through the clear line", () => {
    const a = { x: 400, y: 100 };
    const b = { x: 100, y: 110 };
    const r = routeWire(a, b, { clearY: 260 });
    expect(r.backward).toBe(true);
    expect(r.segments).toHaveLength(2);
    expect(r.segments[0]!.p1.x).toBeGreaterThan(a.x);
    expect(r.segments[1]!.p2.x).toBeLessThan(b.x);
    expect(r.segments[0]!.p3.y).toBe(260);
    expect(r.mid.y).toBe(260);
    expect(Math.max(...flattenRoute(r).map((p) => p.y))).toBeCloseTo(260, 5);
    const pts = flattenRoute(r);
    expect(pts[0]).toEqual(a);
    expect(pts[pts.length - 1]).toEqual(b);
  });

  it("goes between the two heights when they are far apart", () => {
    const r = routeWire({ x: 400, y: 0 }, { x: 100, y: 400 });
    expect(r.mid.y).toBe(200);
  });
});

describe("hit testing", () => {
  const r = routeWire({ x: 0, y: 0 }, { x: 200, y: 0 });
  it("measures the distance to a wire", () => {
    expect(distanceToRoute({ x: 100, y: 0 }, r)).toBeLessThan(0.01);
    expect(distanceToRoute({ x: 100, y: 12 }, r)).toBeCloseTo(12, 0);
  });
  it("hits the closest wire within the tolerance, or none", () => {
    const routes = new Map([
      ["a", r],
      ["b", routeWire({ x: 0, y: 30 }, { x: 200, y: 30 })],
    ]);
    expect(hitEdge({ x: 100, y: 4 }, routes, 10)).toBe("a");
    expect(hitEdge({ x: 100, y: 27 }, routes, 10)).toBe("b");
    expect(hitEdge({ x: 100, y: 15 }, routes, 5)).toBeNull();
  });
  it("finds distances to segments and nearest points", () => {
    expect(distToSegment({ x: 5, y: 5 }, { x: 0, y: 0 }, { x: 10, y: 0 })).toBe(5);
    expect(distToSegment({ x: -3, y: 4 }, { x: 0, y: 0 }, { x: 10, y: 0 })).toBe(5);
    const c = [{ x: 0, y: 0, id: 1 }, { x: 18, y: 0, id: 2 }, { x: 50, y: 0, id: 3 }];
    expect(nearestPoint({ x: 14, y: 0 }, c, 20)?.id).toBe(2);
    expect(nearestPoint({ x: 100, y: 0 }, c, 20)).toBeNull();
  });
  it("evaluates a cubic at its ends", () => {
    const c = r.segments[0]!;
    expect(cubicPoint(c, 0)).toEqual(c.p0);
    expect(cubicPoint(c, 1)).toEqual(c.p3);
  });
});

describe("view maths", () => {
  it("zooms around a point keeping it still, within the limits", () => {
    const v = { x: 30, y: 10, z: 1 };
    const p = { x: 200, y: 150 };
    const n = zoomAround(v, p, 1.5);
    expect((p.x - n.x) / n.z).toBeCloseTo((p.x - v.x) / v.z, 6);
    expect((p.y - n.y) / n.z).toBeCloseTo((p.y - v.y) / v.z, 6);
    expect(zoomAround(v, p, 99).z).toBe(MAX_ZOOM);
    expect(zoomAround(v, p, 0.01).z).toBe(MIN_ZOOM);
  });
  it("fits bounds in the canvas without zooming in past 100%", () => {
    const small = fitView({ x: 0, y: 0, w: 100, h: 50 }, { w: 1000, h: 600 });
    expect(small.z).toBe(1);
    expect(small.x).toBeCloseTo(450, 5);
    const big = fitView({ x: 0, y: 0, w: 4000, h: 1000 }, { w: 1000, h: 600 }, 40);
    expect(big.z).toBeCloseTo(0.3, 5);
    const mid = fitView({ x: 0, y: 0, w: 2000, h: 500 }, { w: 1000, h: 600 }, 40);
    expect(mid.z).toBeCloseTo(920 / 2000, 5);
    expect(fitView(null, { w: 10, h: 10 })).toEqual({ x: 0, y: 0, z: 1 });
  });
  it("snaps, intersects and bounds", () => {
    expect(snapTo(13)).toBe(16);
    expect(snapTo(11)).toBe(8);
    expect(rectsIntersect({ x: 0, y: 0, w: 10, h: 10 }, { x: 9, y: 9, w: 5, h: 5 })).toBe(true);
    expect(rectsIntersect({ x: 0, y: 0, w: 10, h: 10 }, { x: 10, y: 0, w: 5, h: 5 })).toBe(false);
    expect(boundsOf([{ x: 0, y: 0, w: 10, h: 10 }, { x: 20, y: 5, w: 10, h: 10 }])).toEqual({ x: 0, y: 0, w: 30, h: 15 });
    expect(boundsOf([])).toBeNull();
  });
  it("draws the dragged wire forward even when the cursor is behind the port", () => {
    expect(dragWire({ x: 100, y: 0 }, { x: 0, y: 0 }).startsWith("M100 0 C")).toBe(true);
  });
});
