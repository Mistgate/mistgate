import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { roundedPath } from "./qr-path";

// The Go renderer (internal/qr, svg.go) draws the same shape; both are held to this file, which the Go test reads too. Each
// sample has the modules of a code (its rows), the window the page cuts out of them and the path data that comes of it.
type Sample = { name: string; rows: string[]; window: number; d?: string; d_sha256: string; d_len: number };
const samples: Sample[] = JSON.parse(readFileSync(resolve(process.cwd(), "../internal/qr/testdata/path_golden.json"), "utf8"));

describe("roundedPath", () => {
  it.each(samples.map((s) => [s.name, s] as const))("makes the path the Go renderer makes: %s", (_, s) => {
    const n = s.rows.length;
    const lo = (n - s.window) / 2 - 0.5;
    const hi = (n + s.window) / 2 + 0.5;
    const dark = (x: number, y: number) => x >= 0 && y >= 0 && x < n && y < n && s.rows[y]![x] === "1";
    const d = roundedPath(n, (x, y) => dark(x, y) && !(s.window > 0 && x + 1 > lo && x < hi && y + 1 > lo && y < hi), { eyes: true, fillet: true });
    expect(d.length).toBe(s.d_len);
    expect(createHash("sha256").update(d).digest("hex")).toBe(s.d_sha256);
    if (s.d) expect(d).toBe(s.d);
  });
});

// Small grids with their paths, the same file the Go renderer is held to (shapes_golden.json): one module, a block, a corner that
// goes in with and without the fillet, two modules that meet at a corner, a ring with its hole.
type Shape = { name: string; rows: string[]; fillet: boolean; d: string };
const shapes: Shape[] = JSON.parse(readFileSync(resolve(process.cwd(), "../internal/qr/testdata/shapes_golden.json"), "utf8"));

describe("roundedPath: the shape of the figures", () => {
  it.each(shapes.map((s) => [s.name, s] as const))("%s", (_, s) => {
    expect(roundedPath(s.rows.length, (x, y) => s.rows[y]?.[x] === "1", { fillet: s.fillet })).toBe(s.d);
  });

  it("draws a connected figure as one shape, however many modules it has (no seams between modules)", () => {
    const n = 12;
    const full = (x: number, y: number) => x >= 1 && y >= 1 && x < 11 && y < 11; // a 10x10 block
    const d = roundedPath(n, full, { fillet: true });
    expect(d.match(/M/g)).toHaveLength(1);
    // a comb: a bar with teeth is still one figure, and each inner corner has its fillet
    const comb = (x: number, y: number) => (y === 1 && x >= 1 && x <= 9) || (y > 1 && y < 5 && x % 2 === 1 && x <= 9);
    const c = roundedPath(n, comb, { fillet: true });
    expect(c.match(/M/g)).toHaveLength(1);
    expect(c.match(/a0\.3 0\.3 0 0 0/g)).toHaveLength(8); // two at the foot of each of the four gaps between the five teeth
  });
});
