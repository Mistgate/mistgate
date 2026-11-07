import { describe, expect, it } from "vitest";
import { MAX_ZOOM, MIN_ZOOM, fitView, zoomAround } from "./geometry";
import { accumulate, glideStopped, inertiaStep, releaseVelocity, smoothStep, wheelFactor } from "./motion";

describe("wheel accumulation", () => {
  it("steps 10 % per notch of a mouse wheel, in either direction, several notches at once", () => {
    expect(wheelFactor(-100)).toBeCloseTo(1.1, 6);
    expect(wheelFactor(100)).toBeCloseTo(1 / 1.1, 6);
    expect(wheelFactor(-120)).toBeCloseTo(1.1, 6);
    expect(wheelFactor(-300)).toBeCloseTo(1.1 ** 3, 6);
    expect(wheelFactor(-4, 1)).toBeCloseTo(1.1, 6); // 4 lines = 64 px: one notch
  });
  it("is continuous for a trackpad pinch (small deltas)", () => {
    expect(wheelFactor(-2.5)).toBeCloseTo(Math.exp(0.025), 6);
    expect(wheelFactor(0)).toBe(1);
    expect(wheelFactor(-2.5) * wheelFactor(-2.5)).toBeCloseTo(wheelFactor(-5), 6);
  });
  it("accumulates events into one target within the limits", () => {
    let t = 1;
    for (let i = 0; i < 4; i++) t = accumulate(t, wheelFactor(-100), MIN_ZOOM, MAX_ZOOM);
    expect(t).toBeCloseTo(1.1 ** 4, 6);
    expect(accumulate(1.9, 1.5, MIN_ZOOM, MAX_ZOOM)).toBe(MAX_ZOOM);
    expect(accumulate(0.35, 0.5, MIN_ZOOM, MAX_ZOOM)).toBe(MIN_ZOOM);
  });
});

describe("smooth zoom", () => {
  it("eases toward the target: most of the way in about 140 ms, never past it, the same at any frame rate", () => {
    let a = 1;
    for (let i = 0; i < 9; i++) a = smoothStep(a, 2, 16);
    let b = 1;
    for (let i = 0; i < 4; i++) b = smoothStep(b, 2, 36);
    expect(a).toBeGreaterThan(1.9);
    expect(a).toBeLessThan(2);
    expect(a).toBeCloseTo(b, 6);
    expect(smoothStep(1.9996, 2, 16)).toBe(2);
  });
  it("keeps the world point under the pointer still at every frame of the zoom", () => {
    const v0 = { x: 40, y: -20, z: 1 };
    const p = { x: 300, y: 200 };
    const world = { x: (p.x - v0.x) / v0.z, y: (p.y - v0.y) / v0.z };
    let v = v0;
    for (let i = 0; i < 12; i++) {
      v = zoomAround(v, p, smoothStep(v.z, 1.8, 16));
      expect((p.x - v.x) / v.z).toBeCloseTo(world.x, 6);
      expect((p.y - v.y) / v.z).toBeCloseTo(world.y, 6);
    }
    expect(v.z).toBeGreaterThan(1.5);
  });
});

describe("inertia", () => {
  it("decays: slower every frame, less distance, and it stops", () => {
    let v = { x: 2, y: -1 };
    let total = 0;
    let last = Infinity;
    let frames = 0;
    while (!glideStopped(v) && frames < 600) {
      const s = inertiaStep(v, 16);
      expect(Math.abs(s.dx)).toBeLessThan(last);
      last = Math.abs(s.dx);
      total += s.dx;
      v = s.v;
      frames++;
    }
    expect(frames).toBeGreaterThan(10);
    expect(frames).toBeLessThan(600);
    // the sum of the glide is v0 x tau, whatever the frame rate
    expect(total).toBeGreaterThan(2 * 280 * 0.97);
    expect(total).toBeLessThan(2 * 280 * 1.0001);
  });
  it("covers the same distance at 30 and 60 frames per second", () => {
    const run = (dt: number) => {
      let v = { x: 1, y: 0 };
      let d = 0;
      for (let t = 0; t < 3000; t += dt) {
        const s = inertiaStep(v, dt);
        d += s.dx;
        v = s.v;
      }
      return d;
    };
    expect(run(16)).toBeCloseTo(run(33), 3);
  });
  it("takes the release velocity from the last 100 ms of the drag", () => {
    const samples = [
      { x: 0, y: 0, t: 0 },
      { x: 50, y: 0, t: 400 },
      { x: 60, y: 6, t: 460 },
      { x: 90, y: 12, t: 500 },
    ];
    const v = releaseVelocity(samples, 505);
    expect(v.x).toBeCloseTo(30 / 40, 6);
    expect(v.y).toBeCloseTo(6 / 40, 6);
    expect(releaseVelocity(samples, 900)).toEqual({ x: 0, y: 0 });
  });
});

describe("fit", () => {
  it("never shrinks below the given floor, and a graph that fits stays at 100 %", () => {
    expect(fitView({ x: 0, y: 0, w: 4000, h: 1000 }, { w: 1000, h: 600 }, 32, 0.8).z).toBe(0.8);
    expect(fitView({ x: 0, y: 0, w: 600, h: 300 }, { w: 1000, h: 600 }, 32, 0.8).z).toBe(1);
  });
});
