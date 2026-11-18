/**
 * The board's path, one shape for every connected figure of dark modules, drawn along its outline (never module by module,
 * which leaves hairline seams where two shapes meet): a corner that sticks out is round with a radius of half a module, and with
 * `fillet` a corner that goes in (a light module with a dark one on each of two sides, the third dark too) is filled with a
 * quarter of a circle of 0.3 of a module, so the figures look cast. Two modules that touch only at a corner are two figures, each
 * with its own round corner; with `fillet` the light module on each side of that point gets a small fillet of its own. The
 * modules under the logo are left out. With `eyes` the three finder patterns are rounded squares at the end of the path
 * (the board's), and the modules in them are left out. Every number is a multiple of 0.1, written as the Go renderer writes it.
 */
export function roundedPath(n: number, on: (x: number, y: number) => boolean, o: { eyes?: boolean; fillet?: boolean } = {}): string {
  const dark = (x: number, y: number) => x >= 0 && y >= 0 && x < n && y < n && on(x, y) && !(o.eyes && inEye(n, x, y));
  const f = (v: number) => String(v / 10); // tenths of a module
  const w = n + 1;
  // the edges between a dark and a light module, directed so that the dark one is on the right (a figure runs clockwise on screen):
  // out[d][y * w + x] is an edge that leaves the lattice point (x, y) heading E, S, W or N
  const dx = [1, 0, -1, 0], dy = [0, 1, 0, -1]; // prettier-ignore
  const out = [0, 1, 2, 3].map(() => new Uint8Array(w * w));
  for (let y = 0; y < n; y++) {
    for (let x = 0; x < n; x++) {
      if (!dark(x, y)) continue;
      if (!dark(x, y - 1)) out[0]![y * w + x] = 1;
      if (!dark(x + 1, y)) out[1]![y * w + x + 1] = 1;
      if (!dark(x, y + 1)) out[2]![(y + 1) * w + x + 1] = 1;
      if (!dark(x - 1, y)) out[3]![(y + 1) * w + x] = 1;
    }
  }
  const seen = [0, 1, 2, 3].map(() => new Uint8Array(w * w)); // the edges already in a loop
  const radius = (right: boolean) => (right ? 5 : o.fillet ? 3 : 0); // tenths: the corner that sticks out, the one that goes in
  let d = "";
  for (let y = 0; y < w; y++) {
    for (let x = 0; x < w; x++) {
      for (let d0 = 0; d0 < 4; d0++) {
        if (!out[d0]![y * w + x] || seen[d0]![y * w + x]) continue;
        // one loop: follow the edges, turning right where two leave a point (the figures stay apart), until the first is met again
        const dirs: number[] = [], xs: number[] = [], ys: number[] = [];
        let [cx, cy, cd] = [x, y, d0];
        do {
          seen[cd]![cy * w + cx] = 1;
          dirs.push(cd);
          xs.push(cx);
          ys.push(cy);
          cx += dx[cd]!;
          cy += dy[cd]!;
          cd = [(cd + 1) % 4, cd, (cd + 3) % 4].find((c) => out[c]![cy * w + cx])!;
        } while (cx !== x || cy !== y || cd !== d0);
        // the corners of the loop: where the heading changes. A point on a straight run adds nothing.
        const m = dirs.length;
        const corners: { x: number; y: number; din: number; dout: number; r: number }[] = [];
        for (let i = 0; i < m; i++) {
          const din = dirs[(i + m - 1) % m]!, dout = dirs[i]!;
          if (din !== dout) corners.push({ x: xs[i]! * 10, y: ys[i]! * 10, din, dout, r: radius(dout === (din + 1) % 4) });
        }
        const pt = (c: (typeof corners)[number], dir: number, sign: number) => [c.x + sign * c.r * dx[dir]!, c.y + sign * c.r * dy[dir]!] as const;
        const first = corners[0]!;
        const [sx, sy] = pt(first, first.dout, 1);
        d += `M${f(sx)} ${f(sy)}`;
        let [px, py] = [sx, sy];
        for (let i = 1; i <= corners.length; i++) {
          const c = corners[i % corners.length]!;
          const [ex, ey] = pt(c, c.din, -1); // where the corner starts
          const [qx, qy] = pt(c, c.dout, 1); // where it ends
          if (ex !== px || ey !== py) d += ex === px ? `V${f(ey)}` : `H${f(ex)}`;
          if (c.r > 0) d += `a${f(c.r)} ${f(c.r)} 0 0 ${c.dout === (c.din + 1) % 4 ? 1 : 0} ${f(qx - ex)} ${f(qy - ey)}`;
          [px, py] = [qx, qy];
        }
        d += "z";
      }
    }
  }
  if (o.fillet) {
    // two modules that touch only at a corner: the light module on each side of the point gets a fillet of its own
    for (let y = 1; y < n; y++) {
      for (let x = 1; x < n; x++) {
        const [nw, ne, sw, se] = [dark(x - 1, y - 1), dark(x, y - 1), dark(x - 1, y), dark(x, y)];
        if (!((ne && sw && !nw && !se) || (nw && se && !ne && !sw))) continue;
        for (const [lx, ly] of nw ? ([[1, -1], [-1, 1]] as const) : ([[-1, -1], [1, 1]] as const)) {
          // the light module lies lx, ly away from the point; a quarter-circle fillet between the two dark ones
          const [vx, vy] = [x * 10, y * 10];
          d += `M${f(vx)} ${f(vy)}H${f(vx + 3 * lx)}a0.3 0.3 0 0 ${lx * ly === 1 ? 0 : 1} ${f(-3 * lx)} ${f(3 * ly)}z`;
        }
      }
    }
  }
  if (o.eyes) for (const [x, y] of eyeCorners(n)) d += eye(x, y);
  return d;
}

// ---- the three finder patterns: the board draws them as rounded squares, not as modules ----

const eyeCorners = (n: number): [number, number][] => [[0, 0], [n - 7, 0], [0, n - 7]]; // prettier-ignore

/** Is (x, y) in one of the three 7x7 finder patterns, which `roundedPath` draws as shapes when asked? */
export const inEye = (n: number, x: number, y: number) => (x < 7 && y < 7) || (x >= n - 7 && y < 7) || (x < 7 && y >= n - 7);

// A finder pattern at (x, y): the 7x7 ring with corner radius 2, its 5x5 cut-out with radius 1.2 and the 3x3 centre with
// radius 0.9, in one path with the data (fill-rule evenodd). Lengths are in tenths of a module and divided at the end, so the
// numbers are written the way the Go renderer writes them.
function eye(x: number, y: number): string {
  const f = (v: number) => String(v / 10);
  // a square of side s with corner radius a at (ox, oy), the sizes in tenths
  const sq = (ox: number, oy: number, s: number, a: number) => {
    const [x0, y0, w] = [ox * 10, oy * 10, s - 2 * a];
    const arc = (dx: number, dy: number) => `a${f(a)} ${f(a)} 0 0 1 ${f(dx)} ${f(dy)}`;
    return `M${f(x0 + a)} ${f(y0)}h${f(w)}${arc(a, a)}v${f(w)}${arc(-a, a)}h${f(-w)}${arc(-a, -a)}v${f(-w)}${arc(a, -a)}z`;
  };
  return sq(x, y, 70, 20) + sq(x + 1, y + 1, 50, 12) + sq(x + 2, y + 2, 30, 9);
}

// ---- a code as the server sends it: which modules are dark and how big the hole for the logo is ----

/** A code the server encoded (internal/qr): `n` modules a side, `bits` row by row eight to a byte, first in the high bit. */
export type QrBits = { n: number; bits: Uint8Array; window: number };

/**
 * The drawing of a code: its path (rounded modules, the three eyes, no module under the logo) and the side of the hole, in
 * modules, for the logo (0: none). Null when `n`, `bits` or `window` are no code the server could have sent: a side that is not
 * 4 v + 17 from 21 to 177, the wrong number of bytes. A window that is nonsense is dropped and the code is kept.
 */
export function qrDrawing(code: { n: unknown; bits: unknown; window?: unknown }): { n: number; d: string; window: number } | null {
  const { n, bits } = code;
  if (typeof n !== "number" || !Number.isInteger(n) || n < 21 || n > 177 || (n - 17) % 4 !== 0) return null;
  if (!(bits instanceof Uint8Array) || bits.length !== Math.ceil((n * n) / 8)) return null;
  const w = code.window;
  const window = typeof w === "number" && Number.isFinite(w) && w > 0 && w <= n / 2 ? w : 0;
  const dark = (x: number, y: number) => x >= 0 && y >= 0 && x < n && y < n && (((bits[(y * n + x) >> 3] ?? 0) >> (7 - ((y * n + x) & 7))) & 1) === 1;
  const lo = (n - window) / 2 - 0.5;
  const hi = (n + window) / 2 + 0.5;
  return { n, window, d: roundedPath(n, (x, y) => dark(x, y) && !(window > 0 && x + 1 > lo && x < hi && y + 1 > lo && y < hi), { eyes: true, fillet: true }) };
}
