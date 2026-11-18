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
    const d = roundedPath(n, (x, y) => dark(x, y) && !(s.window > 0 && x + 1 > lo && x < hi && y + 1 > lo && y < hi));
    expect(d.length).toBe(s.d_len);
    expect(createHash("sha256").update(d).digest("hex")).toBe(s.d_sha256);
    if (s.d) expect(d).toBe(s.d);
  });
});
