import { describe, expect, it } from "vitest";
import {
  accentPalette,
  bandCenter,
  bandHalfWidth,
  cosineColor,
  easings,
  envelope,
  palettes,
  paletteNames,
  paletteStops,
  resolveDirection,
  resolveEasing,
  resolvePalette,
  ringColor,
  ringColors,
  revealEdges,
  revealMask,
} from "./palette";

describe("cosine palettes", () => {
  it("evaluates a + b*cos(2*pi*(c*t + d)) per channel", () => {
    const p = { a: [0.5, 0.5, 0.5], b: [0.5, 0.5, 0.5], c: [1, 1, 1], d: [0, 0.33, 0.67], t0: 0, t1: 1 } as const;
    const [r, g, b] = cosineColor(p, 0);
    expect(r).toBeCloseTo(1, 5);
    expect(g).toBeCloseTo(0.5 + 0.5 * Math.cos(2 * Math.PI * 0.33), 5);
    expect(b).toBeCloseTo(0.5 + 0.5 * Math.cos(2 * Math.PI * 0.67), 5);
    // periodic in t with c = 1
    expect(cosineColor(p, 0.25)[1]).toBeCloseTo(cosineColor(p, 1.25)[1], 9);
  });

  it("clamps every channel to 0..1, whatever the vectors", () => {
    const wild = { a: [1, 0, 0.5], b: [2, 2, 2], c: [3, 5, 7], d: [0.1, 0.2, 0.3], t0: 0, t1: 1 } as const;
    for (let t = -1; t <= 2; t += 0.05) for (const ch of cosineColor(wild, t)) expect(ch >= 0 && ch <= 1).toBe(true);
  });

  it("runs the default spectrum pink, orange/yellow, green, cyan, blue", () => {
    const at = (f: number) => cosineColor(palettes.spectrum, palettes.spectrum.t0 + (palettes.spectrum.t1 - palettes.spectrum.t0) * f);
    const [r0, g0, b0] = at(0);
    expect(r0).toBeGreaterThan(0.8);
    expect(b0).toBeGreaterThan(g0); // pink: more blue than green
    const [r1, g1] = at(0.3);
    expect(r1).toBeGreaterThan(0.6);
    expect(g1).toBeGreaterThan(0.6); // yellow
    const [r2, g2, b2] = at(0.5);
    expect(g2).toBeGreaterThan(r2);
    expect(g2).toBeGreaterThan(b2); // green
    const [r3, , b3] = at(1);
    expect(b3).toBeGreaterThan(0.8);
    expect(r3).toBeLessThan(0.5); // blue
  });

  it("has every named preset, and resolves names, objects and the accent", () => {
    for (const n of paletteNames) {
      const p = resolvePalette(n, "#7dd3a0");
      expect(p.t1).toBeGreaterThan(p.t0);
    }
    // the default is the halo's ring of the accent: the band and the halo of one moment are the same colours
    const ring = resolvePalette(undefined, "#b8acf2");
    expect(ring.ring).toEqual(ringColors("#b8acf2"));
    expect(cosineColor(ring, 0)).toEqual(ringColor(ring.ring!, -1.5));
    expect(cosineColor(ring, 0)[2]).toBeGreaterThan(0.9); // lavender's ring starts blue
    expect(resolvePalette(palettes.ember, "#000000")).toBe(palettes.ember);
  });

  it("makes the accent palette hit the accent at the middle of the band", () => {
    for (const hex of ["#b8acf2", "#7dd3a0", "#dcc68c", "#e6a6b0"]) {
      const want = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255);
      const got = cosineColor(accentPalette(hex), 0.5);
      got.forEach((c, i) => expect(c).toBeCloseTo(want[i]!, 2));
    }
    // and does not stay one flat tone
    const p = accentPalette("#b8acf2");
    expect(cosineColor(p, p.t0)).not.toEqual(cosineColor(p, p.t1));
    // junk falls back to the default accent instead of NaN
    expect(cosineColor(accentPalette("nonsense"), 0.5).every(Number.isFinite)).toBe(true);
  });

  it("makes CSS stops, optionally lifted towards white", () => {
    const plain = paletteStops(palettes.spectrum, 5);
    expect(plain).toHaveLength(5);
    expect(plain[0]).toMatch(/^rgb\(\d+ \d+ \d+\)$/);
    const lifted = paletteStops(palettes.spectrum, 5, 1);
    expect(new Set(lifted)).toEqual(new Set(["rgb(255 255 255)"]));
  });
});

describe("easing", () => {
  it("starts at 0 and ends at 1", () => {
    for (const f of Object.values(easings)) {
      expect(f(0)).toBeCloseTo(0, 9);
      expect(f(1)).toBeCloseTo(1, 9);
    }
  });

  it("is monotonic, and the default is quick at first and slow at the end", () => {
    for (const f of Object.values(easings)) {
      let prev = -1;
      for (let t = 0; t <= 1.0001; t += 0.02) {
        const v = f(Math.min(1, t));
        expect(v).toBeGreaterThanOrEqual(prev - 1e-9);
        prev = v;
      }
    }
    expect(easings.easeOut(0.5)).toBeGreaterThan(0.5);
  });

  it("resolves a name, a function or nothing, and clamps its input", () => {
    expect(resolveEasing(undefined)(0.5)).toBe(easings.easeOut(0.5));
    expect(resolveEasing("linear")(0.25)).toBe(0.25);
    expect(resolveEasing((t) => t * t)(0.5)).toBe(0.25);
    expect(resolveEasing("linear")(7)).toBe(1);
    expect(resolveEasing("linear")(-3)).toBe(0);
  });
});

describe("direction", () => {
  it("follows the reading direction by default, so a right-to-left page sweeps right to left", () => {
    expect(resolveDirection(undefined, "ltr")).toBe("ltr");
    expect(resolveDirection(undefined, "rtl")).toBe("rtl");
    expect(resolveDirection("inline", "rtl")).toBe("rtl");
    expect(resolveDirection("inline-reverse", "rtl")).toBe("ltr");
    expect(resolveDirection("inline-reverse", "ltr")).toBe("rtl");
  });

  it("takes the physical names as they are", () => {
    for (const d of ["ltr", "rtl", "ttb", "btt"] as const) expect(resolveDirection(d, "rtl")).toBe(d);
  });
});

describe("the band", () => {
  it("starts off the start edge and ends off the far edge", () => {
    const half = bandHalfWidth(0.42, 800);
    expect(bandCenter(0, 800, half)).toBeLessThan(0);
    expect(bandCenter(1, 800, half)).toBeGreaterThan(800);
    expect(bandCenter(0.5, 800, half)).toBeCloseTo(400, 5);
  });

  it("keeps a band that reads on a narrow card and does not swallow a wide table", () => {
    expect(bandHalfWidth(0.42, 100)).toBe(64);
    expect(bandHalfWidth(0.42, 10_000)).toBe(420);
    expect(bandHalfWidth(0.2, 1000)).toBeLessThan(bandHalfWidth(0.6, 1000)); // tighter is narrower
  });

  it("reveals nothing at the start and everything at the end, hugging the band", () => {
    const len = 600;
    const half = bandHalfWidth(0.42, len);
    expect(revealEdges(bandCenter(0, len, half), half).hiddenStart).toBeLessThan(0);
    expect(revealEdges(bandCenter(1, len, half), half).opaqueEnd).toBeGreaterThan(len);
    const e = revealEdges(300, half);
    expect(e.opaqueEnd).toBeLessThan(300);
    expect(e.hiddenStart).toBeGreaterThan(300);
  });

  it("writes the reveal as a mask pointing the way the band goes", () => {
    const m = revealMask("ltr", 200, { opaqueEnd: 50, hiddenStart: 100 });
    expect(m).toBe("linear-gradient(to right, #000 25.0%, transparent 50.0%)");
    expect(revealMask("rtl", 200, { opaqueEnd: 50, hiddenStart: 100 })).toContain("to left");
    expect(revealMask("ttb", 200, { opaqueEnd: 50, hiddenStart: 100 })).toContain("to bottom");
    expect(revealMask("btt", 200, { opaqueEnd: 50, hiddenStart: 100 })).toContain("to top");
  });

  it("fades the band in quickly, holds it, and fades it out after the travel", () => {
    expect(envelope(0, 1000, 200)).toBe(0);
    expect(envelope(70, 1000, 200)).toBeGreaterThan(0);
    expect(envelope(70, 1000, 200)).toBeLessThan(1);
    expect(envelope(500, 1000, 200)).toBe(1);
    expect(envelope(1100, 1000, 200)).toBeCloseTo(0.5, 5);
    expect(envelope(1200, 1000, 200)).toBe(0);
    expect(envelope(5000, 1000, 200)).toBe(0);
  });
});
