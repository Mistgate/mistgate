// The pure parts of the sweep: cosine palettes (Inigo Quilez: c(t) = a + b*cos(2*pi*(c*t + d))), easings, the direction of a
// sweep and the geometry of the band. No DOM here, so all of it is tested directly (palette.test.ts).

export type Vec3 = readonly [number, number, number];

/** One palette: the four vectors of the formula and the stretch of `t` the band spans, trailing edge (t0) to leading edge (t1). */
export type CosinePalette = { a: Vec3; b: Vec3; c: Vec3; d: Vec3; t0: number; t1: number };

const clamp01 = (v: number) => Math.min(1, Math.max(0, v));

/** The colour of the palette at `t`, each channel 0..1. */
export function cosineColor(p: CosinePalette, t: number): Vec3 {
  const ch = (i: 0 | 1 | 2) => clamp01(p.a[i] + p.b[i] * Math.cos(2 * Math.PI * (p.c[i] * t + p.d[i])));
  return [ch(0), ch(1), ch(2)];
}

export const paletteNames = ["spectrum", "prism", "lagoon", "berry", "citrus", "azure", "ember", "accent"] as const;
export type PaletteName = (typeof paletteNames)[number];
type Fixed = Exclude<PaletteName, "accent">;

const v = (x: number, y: number, z: number): Vec3 => [x, y, z];

/**
 * The presets. "spectrum" is the rainbow in the order of the reference (pink, orange, yellow, green, cyan, blue); the others
 * are narrower families. Tuned by eye in the dev kit (?only=fx), in both themes.
 */
export const palettes: Record<Fixed, CosinePalette> = {
  spectrum: { a: v(0.5, 0.5, 0.5), b: v(0.5, 0.5, 0.5), c: v(1, 1, 1), d: v(0, 0.67, 0.33), t0: -0.07, t1: 0.68 },
  prism: { a: v(0.5, 0.5, 0.5), b: v(0.5, 0.5, 0.5), c: v(1, 1, 0.5), d: v(0.8, 0.9, 0.3), t0: 0, t1: 1 },
  lagoon: { a: v(0.2, 0.55, 0.62), b: v(0.25, 0.4, 0.35), c: v(1, 1, 1), d: v(0.55, 0.6, 0.75), t0: 0, t1: 0.7 },
  berry: { a: v(0.62, 0.3, 0.55), b: v(0.4, 0.3, 0.4), c: v(1, 1, 1), d: v(0.95, 0.1, 0.6), t0: 0, t1: 0.65 },
  citrus: { a: v(0.78, 0.62, 0.22), b: v(0.22, 0.38, 0.22), c: v(1, 1, 1), d: v(0, 0.1, 0.25), t0: 0, t1: 0.7 },
  azure: { a: v(0.3, 0.55, 0.78), b: v(0.28, 0.35, 0.25), c: v(1, 1, 1), d: v(0.62, 0.55, 0.62), t0: 0, t1: 0.7 },
  ember: { a: v(0.78, 0.36, 0.18), b: v(0.3, 0.3, 0.2), c: v(1, 1, 1), d: v(0, 0.12, 0.25), t0: 0, t1: 0.6 },
};

function hexToRgb(hex: string): Vec3 {
  const m = /^#?([0-9a-f]{6})$/i.exec(hex.trim());
  const n = m ? parseInt(m[1]!, 16) : 0xb8acf2;
  return [(n >> 16) / 255, ((n >> 8) & 255) / 255, (n & 255) / 255];
}

/**
 * A palette of the panel's accent: at the middle of the band (t = 0.5) it is exactly that colour (as far as the channel's
 * range allows), and the channels drift at different speeds, so the band runs through its neighbours (pinker and bluer
 * edges for a lavender, greener and yellower for a mint) instead of staying one flat tone.
 */
export function accentPalette(hex: string): CosinePalette {
  const rgb = hexToRgb(hex);
  const c = v(0.55, 0.85, 1.15);
  const a = 0.55;
  const b = 0.45;
  const d = rgb.map((x, i) => Math.acos(Math.min(1, Math.max(-1, (x - a) / b))) / (2 * Math.PI) - c[i]! * 0.5) as unknown as Vec3;
  return { a: v(a, a, a), b: v(b, b, b), c, d, t0: 0.18, t1: 0.82 };
}

export function resolvePalette(p: PaletteName | CosinePalette | undefined, accent: string): CosinePalette {
  if (typeof p === "object") return p;
  if (p === "accent") return accentPalette(accent);
  return palettes[p ?? "spectrum"];
}

/** A palette as `n` CSS colour stops, `lift` of the way to white (the CSS fallback's gradient, and the dev kit's swatches). */
export function paletteStops(p: CosinePalette, n: number, lift = 0): string[] {
  return Array.from({ length: n }, (_, i) => {
    const [r, g, b] = cosineColor(p, p.t0 + ((p.t1 - p.t0) * i) / Math.max(1, n - 1)).map((c) => c + (1 - c) * lift) as [number, number, number];
    return `rgb(${Math.round(r * 255)} ${Math.round(g * 255)} ${Math.round(b * 255)})`;
  });
}

// --- easing ---

export type EasingFn = (t: number) => number;
export const easings = {
  linear: (t) => t,
  // the default: quick off the line, long soft landing
  easeOut: (t) => 1 - Math.pow(1 - t, 2.6),
  easeInOut: (t) => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2),
  expoOut: (t) => (t >= 1 ? 1 : 1 - Math.pow(2, -9 * t)),
  sine: (t) => Math.sin((t * Math.PI) / 2),
} satisfies Record<string, EasingFn>;
export type EasingName = keyof typeof easings;

export function resolveEasing(e: EasingName | EasingFn | undefined): EasingFn {
  const f = typeof e === "function" ? e : easings[e ?? "easeOut"];
  return (t) => f(clamp01(t));
}

// --- direction ---

export type Direction = "ltr" | "rtl" | "ttb" | "btt" | "inline" | "inline-reverse";
export type Axis = "ltr" | "rtl" | "ttb" | "btt";

/**
 * "inline" is the reading direction of the element (left to right, right to left in Persian), "inline-reverse" its opposite;
 * the four physical names are taken as they are.
 */
export function resolveDirection(dir: Direction | undefined, cssDirection: string): Axis {
  const rtl = cssDirection === "rtl";
  if (dir === undefined || dir === "inline") return rtl ? "rtl" : "ltr";
  if (dir === "inline-reverse") return rtl ? "ltr" : "rtl";
  return dir;
}

/** The unit vector of the sweep in screen space (y grows downwards), and the CSS gradient keyword that points the same way. */
export const axes: Record<Axis, { vec: readonly [number, number]; css: string; horizontal: boolean }> = {
  ltr: { vec: [1, 0], css: "to right", horizontal: true },
  rtl: { vec: [-1, 0], css: "to left", horizontal: true },
  ttb: { vec: [0, 1], css: "to bottom", horizontal: false },
  btt: { vec: [0, -1], css: "to top", horizontal: false },
};

// --- the band ---

/**
 * Half the width of the band in px. `bandWidth` is the band's width as a share of the element along the sweep (0.42 default,
 * smaller is a tighter band); a narrow card still gets a band that reads, a wide table one that does not swallow it.
 */
export function bandHalfWidth(bandWidth: number, length: number): number {
  return Math.min(420, Math.max(64, (bandWidth * length) / 2));
}

/**
 * Where the band's centre is along the element (px from the start edge) at eased progress `p`: from wholly off the start
 * edge to wholly off the end edge, so the light crosses the element from edge to edge and never stops short of one.
 */
export function bandCenter(p: number, length: number, half: number): number {
  const from = -half * 1.15;
  const to = length + half * 1.15;
  return from + (to - from) * p;
}

/**
 * The reveal: content is fully there before `opaqueEnd` and fully hidden after `hiddenStart` (px along the element), both
 * hugging the band's centre so the band covers the seam.
 */
export function revealEdges(center: number, half: number): { opaqueEnd: number; hiddenStart: number } {
  return { opaqueEnd: center - half * 0.25, hiddenStart: center + half * 0.3 };
}

/** The CSS mask for a reveal at the given edges; percentages of the length. */
export function revealMask(axis: Axis, length: number, edges: { opaqueEnd: number; hiddenStart: number }): string {
  const pct = (px: number) => `${(Math.round((px / Math.max(1, length)) * 1000) / 10).toFixed(1)}%`;
  return `linear-gradient(${axes[axis].css}, #000 ${pct(edges.opaqueEnd)}, transparent ${pct(edges.hiddenStart)})`;
}

/** The band's overall opacity: a quick rise at the start (so it does not pop in at the edge), full while it travels, then the fade-out. */
export function envelope(elapsed: number, duration: number, fadeOut: number, fadeIn = 140): number {
  if (elapsed <= 0) return 0;
  const rise = Math.min(1, elapsed / fadeIn);
  const rising = rise * rise * (3 - 2 * rise);
  if (elapsed <= duration) return rising;
  return rising * clamp01(1 - (elapsed - duration) / Math.max(1, fadeOut));
}
