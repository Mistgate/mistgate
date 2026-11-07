// What draws the band. One layer (a fixed, click-through, full-window div) holds either ONE shared WebGL canvas (a band per
// active sweep, drawn into its own scissored rectangle) or, where WebGL is missing, one blurred gradient div per sweep. The
// layer is in the page only while something plays, and the GL context is let go after a while without use.
import { axes, paletteStops, type Axis, type CosinePalette } from "./palette";

export type Rect = { x: number; y: number; w: number; h: number };

/** One band to draw this frame. All lengths are CSS px, in the window's coordinates; `center` and `half` are along the sweep. */
export type PaintJob = {
  id: number;
  rect: Rect; // the element
  visible: Rect; // the part of it that is not clipped by a scroll container or the window
  axis: Axis;
  center: number;
  half: number;
  alpha: number;
  brightness: number;
  wave: number;
  ripple: number;
  time: number; // seconds since this sweep began
  seed: number;
  radius: number; // the element's corner radius: the band is cut to the same rounded shape
  palette: CosinePalette;
};

export interface Painter {
  /** Called once per frame before the jobs. */
  begin(blend: string): void;
  paint(job: PaintJob): void;
  /** Called once per frame after the jobs; drops what no job drew this frame. */
  end(): void;
  /** Takes the layer out of the page (nothing plays). */
  detach(): void;
  /** Frees the GL context. */
  dispose(): void;
}

const LAYER_Z = 60; // over the modal (z-50) and the toasts (z-40): a band on a card inside a dialog is seen

function makeLayer(): HTMLDivElement {
  const el = document.createElement("div");
  el.setAttribute("aria-hidden", "true");
  el.dataset.fxLayer = "";
  Object.assign(el.style, {
    position: "fixed",
    inset: "0",
    zIndex: String(LAYER_Z),
    pointerEvents: "none",
    contain: "strict",
    overflow: "hidden",
  } satisfies Partial<CSSStyleDeclaration>);
  return el;
}

// --- WebGL ---

const VERT = `attribute vec2 p; void main(){ gl_Position = vec4(p, 0.0, 1.0); }`;

// The band: a soft bright spine with a wide halo and a trailing wash, coloured by the cosine palette across its width, its
// edge pushed about by two sines and two octaves of value noise so it is never a straight line. Premultiplied output.
const FRAG = `
precision highp float;
uniform vec2 uBuf;
uniform float uDpr;
uniform vec2 uOrigin;
uniform vec2 uSize;
uniform vec2 uDir;
uniform float uCenter, uHalf, uAlpha, uBright, uWave, uRipple, uTime, uSeed, uRadius;
uniform vec3 pA, pB, pC, pD;
uniform vec2 pT;

float hash(vec2 p){ p = fract(p * vec2(123.34, 456.21)); p += dot(p, p + 45.32); return fract(p.x * p.y); }
float vnoise(vec2 p){
  vec2 i = floor(p), f = fract(p);
  f = f * f * (3.0 - 2.0 * f);
  return mix(mix(hash(i), hash(i + vec2(1.0, 0.0)), f.x), mix(hash(i + vec2(0.0, 1.0)), hash(i + vec2(1.0, 1.0)), f.x), f.y);
}
vec3 pal(float t){ return clamp(pA + pB * cos(6.28318 * (pC * t + pD)), 0.0, 1.0); }

void main(){
  vec2 css = vec2(gl_FragCoord.x, uBuf.y - gl_FragCoord.y) / uDpr;
  vec2 pos = css - uOrigin - 0.5 * uSize;
  float len = abs(dot(uSize, uDir));
  float along = dot(pos, uDir) + 0.5 * len;
  float across = dot(pos, vec2(-uDir.y, uDir.x));

  float w1 = sin(across * 0.021 + uTime * 2.6 + uSeed);
  float w2 = sin(across * 0.0073 - uTime * 1.7 + uSeed * 2.0);
  float n1 = vnoise(vec2(across * 0.016 + uSeed, uTime * 1.2)) - 0.5;
  float n2 = vnoise(vec2(across * 0.047 - uTime * 0.8, along * 0.004 + uSeed)) - 0.5;
  float warp = (w1 * 0.45 + w2 * 0.55) * uWave + (n1 * 1.7 + n2 * 0.8) * uRipple;

  float x = (along - uCenter) / uHalf + warp * 0.3;
  float body = smoothstep(x < 0.0 ? 1.35 : 1.0, 0.12, abs(x));
  body = body * body * (1.0 + 0.7 * body);
  float core = exp(-x * x * 5.5);
  float wash = (x < 0.0 ? exp(x * 1.15) : exp(-x * x * 4.0)) * smoothstep(-3.6, -2.4, x);
  float inten = pow(clamp(body * 0.9 + core * 0.3 + wash * 0.07, 0.0, 1.0), 0.72);

  float tt = mix(pT.x, pT.y, clamp(x * 0.55 + 0.5, 0.0, 1.0)) + n1 * 0.14 * uRipple;
  vec3 col = pal(tt);
  col = mix(col, vec3(1.0), 0.24 + core * 0.3); // pastel and luminous: a saturated colour added at low alpha only looks muddy

  // cut to the element's rounded corners (a signed-distance rounded box, one px of anti-aliasing)
  float r = min(uRadius, 0.5 * min(uSize.x, uSize.y));
  vec2 q = abs(pos) - (0.5 * uSize - r);
  float sd = length(max(q, 0.0)) + min(max(q.x, q.y), 0.0) - r;
  float a = inten * uAlpha * (1.0 - smoothstep(-0.7, 0.4, sd));
  float g = (hash(gl_FragCoord.xy + fract(uTime) * 37.0) - 0.5) * (1.5 / 255.0);
  gl_FragColor = vec4((col * uBright + g) * a, a);
}`;

const uniformNames = ["uBuf", "uDpr", "uOrigin", "uSize", "uDir", "uCenter", "uHalf", "uAlpha", "uBright", "uWave", "uRipple", "uTime", "uSeed", "uRadius", "pA", "pB", "pC", "pD", "pT"] as const;

class GlPainter implements Painter {
  private layer = makeLayer();
  private canvas: HTMLCanvasElement;
  private gl: WebGLRenderingContext;
  private loc: Record<(typeof uniformNames)[number], WebGLUniformLocation | null>;
  private dpr = 1;
  private bufW = 0;
  private bufH = 0;
  lost = false;

  private constructor(gl: WebGLRenderingContext, loc: GlPainter["loc"], canvas: HTMLCanvasElement) {
    this.gl = gl;
    this.loc = loc;
    this.canvas = canvas;
    canvas.style.cssText = "position:absolute;inset:0;width:100%;height:100%;display:block";
    this.layer.append(canvas);
    canvas.addEventListener("webglcontextlost", (e) => {
      e.preventDefault();
      this.lost = true;
    });
  }

  /** null when there is no WebGL (or the shader does not build): the CSS painter takes over. */
  static create(): GlPainter | null {
    try {
      const canvas = document.createElement("canvas");
      const gl = canvas.getContext("webgl", { alpha: true, premultipliedAlpha: true, antialias: false, depth: false, stencil: false, powerPreference: "low-power" });
      if (!gl) return null;
      const compile = (type: number, src: string) => {
        const s = gl.createShader(type)!;
        gl.shaderSource(s, src);
        gl.compileShader(s);
        if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) throw new Error(gl.getShaderInfoLog(s) ?? "shader");
        return s;
      };
      const prog = gl.createProgram()!;
      gl.attachShader(prog, compile(gl.VERTEX_SHADER, VERT));
      gl.attachShader(prog, compile(gl.FRAGMENT_SHADER, FRAG));
      gl.linkProgram(prog);
      if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) throw new Error("link");
      gl.useProgram(prog);
      const buf = gl.createBuffer();
      gl.bindBuffer(gl.ARRAY_BUFFER, buf);
      gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 3, -1, -1, 3]), gl.STATIC_DRAW);
      const at = gl.getAttribLocation(prog, "p");
      gl.enableVertexAttribArray(at);
      gl.vertexAttribPointer(at, 2, gl.FLOAT, false, 0, 0);
      gl.enable(gl.SCISSOR_TEST);
      gl.disable(gl.BLEND);
      const loc = Object.fromEntries(uniformNames.map((n) => [n, gl.getUniformLocation(prog, n)])) as GlPainter["loc"];
      return new GlPainter(gl, loc, canvas);
    } catch {
      return null;
    }
  }

  get element() {
    return this.layer;
  }

  begin(blend: string) {
    if (!this.layer.isConnected) document.body.append(this.layer);
    this.layer.style.mixBlendMode = blend;
    const gl = this.gl;
    this.dpr = Math.min(window.devicePixelRatio || 1, 2);
    // the layer's own box, not innerWidth: the page keeps a scrollbar gutter, and a buffer as wide as the window would be
    // squeezed into the narrower layer, so every band would land short of its element's end edge and be cut there
    const { w: cssW, h: cssH } = viewportBox();
    const w = Math.max(1, Math.round(cssW * this.dpr));
    const h = Math.max(1, Math.round(cssH * this.dpr));
    if (w !== this.bufW || h !== this.bufH) {
      this.canvas.width = this.bufW = w;
      this.canvas.height = this.bufH = h;
    }
    gl.viewport(0, 0, w, h);
    gl.scissor(0, 0, w, h);
    gl.clearColor(0, 0, 0, 0);
    gl.clear(gl.COLOR_BUFFER_BIT);
  }

  paint(j: PaintJob) {
    const { gl, loc, dpr } = this;
    const vis = j.visible;
    if (vis.w < 1 || vis.h < 1) return;
    const x0 = Math.floor(vis.x * dpr);
    const x1 = Math.ceil((vis.x + vis.w) * dpr);
    const y0 = Math.floor(vis.y * dpr);
    const y1 = Math.ceil((vis.y + vis.h) * dpr);
    gl.scissor(x0, this.bufH - y1, x1 - x0, y1 - y0);
    const [dx, dy] = axes[j.axis].vec;
    const p = j.palette;
    gl.uniform2f(loc.uBuf, this.bufW, this.bufH);
    gl.uniform1f(loc.uDpr, dpr);
    gl.uniform2f(loc.uOrigin, j.rect.x, j.rect.y);
    gl.uniform2f(loc.uSize, j.rect.w, j.rect.h);
    gl.uniform2f(loc.uDir, dx, dy);
    gl.uniform1f(loc.uCenter, j.center);
    gl.uniform1f(loc.uHalf, j.half);
    gl.uniform1f(loc.uAlpha, j.alpha);
    gl.uniform1f(loc.uBright, j.brightness);
    gl.uniform1f(loc.uWave, j.wave);
    gl.uniform1f(loc.uRipple, j.ripple);
    gl.uniform1f(loc.uTime, j.time);
    gl.uniform1f(loc.uSeed, j.seed);
    gl.uniform1f(loc.uRadius, j.radius);
    gl.uniform3f(loc.pA, ...p.a);
    gl.uniform3f(loc.pB, ...p.b);
    gl.uniform3f(loc.pC, ...p.c);
    gl.uniform3f(loc.pD, ...p.d);
    gl.uniform2f(loc.pT, p.t0, p.t1);
    gl.drawArrays(gl.TRIANGLES, 0, 3);
  }

  end() {}

  detach() {
    this.layer.remove();
  }

  dispose() {
    this.layer.remove();
    this.gl.getExtension("WEBGL_lose_context")?.loseContext();
  }
}

// --- CSS fallback ---

const FILTER_ID = "mg-fx-wave";

/** The wavy edge of the fallback: a static turbulence pushing the blurred band about. One <svg>, made on first use. */
function ensureFilter(): void {
  if (document.getElementById(FILTER_ID)) return;
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("width", "0");
  svg.setAttribute("height", "0");
  svg.style.cssText = "position:absolute;width:0;height:0";
  svg.innerHTML = `<filter id="${FILTER_ID}" x="-20%" y="-20%" width="140%" height="140%" color-interpolation-filters="sRGB"><feTurbulence type="fractalNoise" baseFrequency="0.006 0.02" numOctaves="2" seed="7" result="n"/><feDisplacementMap in="SourceGraphic" in2="n" scale="46" xChannelSelector="R" yChannelSelector="G"/></filter>`;
  document.body.append(svg);
}

class CssPainter implements Painter {
  private layer = makeLayer();
  private nodes = new Map<number, { wrap: HTMLDivElement; band: HTMLDivElement; axis: Axis; palette: CosinePalette }>();
  private seen = new Set<number>();

  begin(blend: string) {
    if (!this.layer.isConnected) {
      ensureFilter();
      document.body.append(this.layer);
    }
    this.layer.style.mixBlendMode = blend;
    this.seen.clear();
  }

  paint(j: PaintJob) {
    this.seen.add(j.id);
    let n = this.nodes.get(j.id);
    if (!n || n.axis !== j.axis || n.palette !== j.palette) {
      n?.wrap.remove();
      const wrap = document.createElement("div");
      const band = document.createElement("div");
      wrap.style.cssText = "position:absolute;overflow:hidden;contain:paint";
      wrap.style.borderRadius = `${j.radius}px`;
      const stops = ["transparent", ...paletteStops(j.palette, 7, 0.24), "transparent"].join(",");
      band.style.cssText = `position:absolute;background:linear-gradient(${axes[j.axis].css},${stops});filter:url(#${FILTER_ID}) blur(14px);will-change:transform`;
      wrap.append(band);
      this.layer.append(wrap);
      n = { wrap, band, axis: j.axis, palette: j.palette };
      this.nodes.set(j.id, n);
    }
    const vis = j.visible;
    const { wrap, band } = n;
    wrap.style.left = `${vis.x}px`;
    wrap.style.top = `${vis.y}px`;
    wrap.style.width = `${vis.w}px`;
    wrap.style.height = `${vis.h}px`;
    const r = j.rect;
    const horizontal = axes[j.axis].horizontal;
    const span = j.half * 2;
    const pad = 24; // across the band: past the element's edges, so the blur and the wave do not show a seam there
    // the band's box in window coordinates, then relative to the visible rectangle
    let bx: number, by: number, bw: number, bh: number;
    if (horizontal) {
      bw = span;
      bh = r.h + pad * 2;
      by = r.y - pad;
      bx = j.axis === "ltr" ? r.x + j.center - j.half : r.x + r.w - j.center - j.half;
    } else {
      bh = span;
      bw = r.w + pad * 2;
      bx = r.x - pad;
      by = j.axis === "ttb" ? r.y + j.center - j.half : r.y + r.h - j.center - j.half;
    }
    band.style.width = `${bw}px`;
    band.style.height = `${bh}px`;
    band.style.transform = `translate3d(${bx - vis.x}px,${by - vis.y}px,0)`;
    // the fallback has no spine and wash of its own: its overall glow follows the same opacity
    band.style.opacity = String(Math.min(1, j.alpha * j.brightness * 0.62));
  }

  end() {
    for (const [id, n] of this.nodes) {
      if (this.seen.has(id)) continue;
      n.wrap.remove();
      this.nodes.delete(id);
    }
  }

  detach() {
    for (const n of this.nodes.values()) n.wrap.remove();
    this.nodes.clear();
    this.layer.remove();
  }

  dispose() {
    this.detach();
  }
}

// --- the shared painter ---

let painter: Painter | null = null;
let useCss = false;
let idle: ReturnType<typeof setTimeout> | undefined;
const IDLE_MS = 20_000;

/** The painter for this frame: GL if it builds (and keeps), else CSS. Made on first use, reused by every sweep. */
export function getPainter(): Painter {
  clearTimeout(idle);
  if (painter instanceof GlPainter && painter.lost) {
    painter.dispose();
    painter = null;
    useCss = true;
  }
  if (!painter) {
    const gl = useCss ? null : GlPainter.create();
    if (!gl) useCss = true;
    painter = gl ?? new CssPainter();
  }
  return painter;
}

/** Nothing plays: the layer leaves the page now and the GL context goes after a while. */
export function releasePainter(): void {
  painter?.detach();
  clearTimeout(idle);
  idle = setTimeout(() => {
    painter?.dispose();
    painter = null;
  }, IDLE_MS);
}

/** Tests and dev kit: which painter is in use. */
export const painterKind = (): "gl" | "css" | "none" => (painter instanceof GlPainter ? "gl" : painter ? "css" : "none");

/** For tests: forget the painter. */
export function resetPainter(): void {
  painter?.dispose();
  painter = null;
  useCss = false;
  clearTimeout(idle);
}

/** The area the fixed effects layer covers, in CSS px: the viewport without its scrollbars. */
export function viewportBox(): { w: number; h: number } {
  const root = document.documentElement;
  return { w: root.clientWidth || window.innerWidth, h: root.clientHeight || window.innerHeight };
}
