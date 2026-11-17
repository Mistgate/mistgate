// The live matrix behind the screen that takes over while the panel installs itself (the board's `mxStart`, PnUpdates): a field of
// dots on a canvas, a ring of light that spreads from the middle, a pulse at every step. Cheap on purpose: the geometry of the dots
// is worked out once per size, a frame is three sines per dot, the dots are drawn in batches by level of brightness (about 20
// changes of the fill style a frame, not one per dot), 30 frames a second, and nothing is drawn while the tab is hidden. With the
// system's "reduce motion" it draws one still frame.

export type MxRead = () => {
  /** The step the update is at (0..3); 4 when it is over. The field flares at every change. */
  k: number;
  mode: "" | "slow" | "out";
  light: boolean;
};

const S = 14;
const LV = 10;

type Grid = { W: number; H: number; N: number; X: Float32Array; Y: Float32Array; D: Float32Array; Q: Float32Array; P1: Float32Array; P2: Float32Array; SP: Float32Array; PH: Float32Array; C: Uint8Array; L: Uint8Array };

function build(W: number, H: number): Grid {
  const cols = Math.ceil(W / S);
  const rows = Math.ceil(H / S);
  const N = cols * rows;
  const X = new Float32Array(N), Y = new Float32Array(N), D = new Float32Array(N), Q = new Float32Array(N);
  const P1 = new Float32Array(N), P2 = new Float32Array(N), SP = new Float32Array(N), PH = new Float32Array(N);
  const C = new Uint8Array(N), L = new Uint8Array(N);
  const cx = W / 2;
  const cy = H * 0.46;
  const rx = Math.min(310, W * 0.5);
  let n = 0;
  for (let j = 0; j < rows; j++) {
    for (let i = 0; i < cols; i++) {
      const x = i * S + S / 2;
      const y = j * S + S / 2;
      const dx = x - cx;
      const dy = y - cy;
      const hh = Math.sin(x * 12.9898 + y * 78.233) * 43758.5453;
      const rnd = hh - Math.floor(hh);
      const qx = dx / rx;
      const qy = (y - H * 0.5) / 250;
      let q = (Math.sqrt(qx * qx + qy * qy) - 0.5) / 1.25;
      q = q < 0 ? 0 : q > 1 ? 1 : q;
      q = q * q * (3 - 2 * q);
      X[n] = x;
      Y[n] = y;
      D[n] = Math.sqrt(dx * dx + dy * dy);
      Q[n] = 0.04 + 0.96 * q;
      P1[n] = rnd * 0.9;
      P2[n] = x * 0.013 - y * 0.009;
      SP[n] = 1 + rnd * 2.6;
      PH[n] = rnd * 40;
      C[n] = rnd > 0.86 ? 1 : 0;
      n++;
    }
  }
  return { W, H, N: n, X, Y, D, Q, P1, P2, SP, PH, C, L };
}

/** Starts the matrix on `cv`; returns the function that stops it. */
export function runMatrix(cv: HTMLCanvasElement, read: MxRead): () => void {
  let stopped = false;
  let raf = 0;
  let t0 = 0;
  let last = 0;
  let pulse = -9;
  let lastK = -1;
  let G: Grid | null = null;
  let g: CanvasRenderingContext2D | null = null;
  const still = typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;

  const tick = (now: number) => {
    if (stopped) return;
    if (!t0) t0 = now;
    if (!still && (document.hidden || now - last < 31)) {
      raf = requestAnimationFrame(tick);
      return;
    }
    last = now;
    const W = cv.clientWidth;
    const H = cv.clientHeight;
    if (!W || !H) {
      raf = requestAnimationFrame(tick);
      return;
    }
    const dpr = Math.min(window.devicePixelRatio || 1, 1.5);
    if (!G || G.W !== W || G.H !== H || !g) {
      G = build(W, H);
      cv.width = Math.round(W * dpr);
      cv.height = Math.round(H * dpr);
      g = cv.getContext("2d", { alpha: true, desynchronized: true });
      if (!g) return;
      g.setTransform(dpr, 0, 0, dpr, 0, 0);
    }
    const { k, mode, light } = read();
    const t = (now - t0) / 1000;
    if (k !== lastK) {
      lastK = k;
      pulse = t;
    }
    const out = mode === "out";
    const R = Math.max(W, H) * 0.78 * (0.8 + Math.min(k, 3) * 0.07) * (out ? 1.4 : 1);
    const iR = 1 / R;
    const pr = (t - pulse) * (out ? 900 : 420);
    const dir = t * (out ? -2.6 : 1.5);
    const t6 = t * 0.6;
    const gain = mode === "slow" ? 0.55 : 1;
    const { N, D, Q, P1, P2, SP, PH, L, X, Y, C } = G;
    const sin = Math.sin;
    for (let i = 0; i < N; i++) {
      const d = D[i]!;
      if (d > R) {
        L[i] = 0;
        continue;
      }
      const fall = 1 - d * iR;
      const f = fall * Math.sqrt(Math.sqrt(fall));
      const w1 = 0.5 + 0.5 * sin(d * 0.022 + dir + P1[i]!);
      const w2 = 0.5 + 0.5 * sin(P2[i]! + t6);
      let tw = 0.5 + 0.5 * sin(t * SP[i]! + PH[i]!);
      tw *= tw;
      tw *= tw;
      tw *= tw;
      let a = f * (0.1 + 0.62 * w1 * w1 * (0.45 + 0.55 * w2) + 0.85 * tw) * gain;
      const e = d - pr;
      if (e > -130 && e < 130) {
        const u = e / 52;
        a += Math.exp(-u * u) * 0.85 * fall;
      }
      a *= out ? 0.3 + 0.7 * Q[i]! : Q[i]!;
      L[i] = a < 0.03 ? 0 : a >= 1 ? LV : Math.ceil(a * LV);
    }
    g.clearRect(0, 0, W, H);
    const cols = [mode === "slow" ? "245,185,90" : light ? "16,140,100" : "125,211,160", light ? "40,120,170" : "142,200,234"];
    const am = light ? 0.7 : 0.92;
    for (let c = 0; c < 2; c++) {
      for (let l = 1; l <= LV; l++) {
        const a = l / LV;
        const sz = 1.3 + a * 2.3;
        const h = sz / 2;
        let any = false;
        for (let i = 0; i < N; i++) {
          if (L[i] === l && C[i] === c) {
            if (!any) {
              g.fillStyle = `rgba(${cols[c]},${(a * am).toFixed(2)})`;
              any = true;
            }
            g.fillRect(X[i]! - h, Y[i]! - h, sz, sz);
          }
        }
      }
    }
    if (still) return;
    raf = requestAnimationFrame(tick);
  };
  raf = requestAnimationFrame(tick);
  return () => {
    stopped = true;
    cancelAnimationFrame(raf);
  };
}
