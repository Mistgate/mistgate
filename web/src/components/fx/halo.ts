// The halo: a soft ring of light spreads from where the person clicked, and the blocks it passes (cards, rows, headings,
// buttons) are pushed out a few pixels and back, as if the action had landed on the page. It plays when something is
// created, saved, or the assistant has answered; a new one replaces the one playing. Only transforms and one canvas of
// light: nothing of the page is copied, so it starts in the frame it is asked for. Off with the effects, off with reduced
// motion (it is decoration only: nothing appears or changes under it).
import { effectsEnabled, effectsFor, prefersReducedMotion, type FxKind } from "@/lib/effects";
import { ringColors } from "./palette";

type Point = { x: number; y: number };

const DURATION = 1000;
/** The ring's width scale, CSS px: the light and the push share it, so the blocks move exactly where the light is. */
const RING = 110;
/** How far a block is pushed at most, px, and how much it grows at the crest. */
const PUSH = 7;
const LIFT = 0.012;
/** A block is the outermost element no taller than this; a taller one is split into its children (a long table moves row by row). */
const MAX_BLOCK = 220;
/** A halo asked for while a window is open waits for it to close this long, then gives up. */
const WAIT_MS = 1500;

// Where the person last pressed: the halo starts at the button that did the thing.
let pointer = { x: 0, y: 0, at: -Infinity };
if (typeof addEventListener === "function") {
  addEventListener("pointerdown", (e) => void (pointer = { x: e.clientX, y: e.clientY, at: performance.now() }), { capture: true, passive: true });
}

let stopCurrent: (() => void) | null = null;

/**
 * Plays the halo for `kind` (null: whenever the effects are on, the settings preview) over `target`, by default the page's
 * main column. Inside an element marked data-fx-halo (the assistant's panel) the light stays in that element; on the page it
 * covers the window except the bars (data-fx-chrome), and waits for an open window to close first.
 */
export function halo(kind: FxKind | null, opts: { target?: HTMLElement | null; origin?: Point } = {}): void {
  if (!(kind ? effectsFor(kind) : effectsEnabled()) || prefersReducedMotion()) return;
  const target = opts.target ?? document.getElementById("main");
  if (!target?.isConnected) return;
  stopCurrent?.();
  const host = target.closest<HTMLElement>("[data-fx-halo]");
  let stopped = false;
  let stopRun = () => {};
  const stop = () => {
    stopped = true;
    stopRun();
    if (stopCurrent === stop) stopCurrent = null;
  };
  stopCurrent = stop;
  void whenClear(!host).then((clear) => {
    if (stopped || !clear || !target.isConnected) return stop();
    stopRun = run(target, host, opts.origin ?? originFor(target), stop);
  });
}

/** Stops the halo that is playing, if any (tests). */
export const stopHalo = () => stopCurrent?.();

const windowOpen = () => [...document.querySelectorAll('[role="dialog"], [role="alertdialog"]')].some((d) => !d.closest("[data-fx-halo]"));

function whenClear(page: boolean): Promise<boolean> {
  if (!page || !windowOpen()) return Promise.resolve(true);
  return new Promise((resolve) => {
    const t0 = performance.now();
    const poll = () => (!windowOpen() ? resolve(true) : performance.now() - t0 > WAIT_MS ? resolve(false) : void setTimeout(poll, 50));
    poll();
  });
}

/** The press that led here (within 10 s), else the middle of what is visible of the target. */
function originFor(target: HTMLElement): Point {
  if (performance.now() - pointer.at < 10_000) return pointer;
  const r = target.getBoundingClientRect();
  return { x: r.left + r.width / 2, y: (Math.max(r.top, 0) + Math.min(r.bottom, innerHeight)) / 2 };
}

function blocks(root: Element, top: number, bottom: number): HTMLElement[] {
  const out: HTMLElement[] = [];
  const walk = (el: Element) => {
    for (const child of el.children) {
      if (!(child instanceof HTMLElement)) continue;
      const r = child.getBoundingClientRect();
      if (r.width <= 0 || r.height <= 0 || r.bottom < top || r.top > bottom) continue;
      if (r.height <= MAX_BLOCK || child.children.length === 0) out.push(child);
      else walk(child);
    }
  };
  walk(root);
  return out;
}

/** Starts the light and the push; returns what stops both at once, putting every block back where it was. */
function run(target: HTMLElement, host: HTMLElement | null, origin: Point, done: () => void): () => void {
  const area = host ? host.getBoundingClientRect() : { left: 0, top: 0, bottom: innerHeight, width: innerWidth, height: innerHeight };
  const view = target.getBoundingClientRect();
  const items = blocks(target, Math.max(area.top, view.top), Math.min(area.bottom, view.bottom)).map((el) => {
    const r = el.getBoundingClientRect();
    const dx = r.left + r.width / 2 - origin.x;
    const dy = r.top + r.height / 2 - origin.y;
    const dist = Math.hypot(dx, dy);
    return { el, dist, ux: dist > 0.1 ? dx / dist : 0, uy: dist > 0.1 ? dy / dist : 0, transform: el.style.transform };
  });
  const ox = origin.x - area.left;
  const oy = origin.y - area.top;
  const maxRadius = Math.max(Math.hypot(ox, oy), Math.hypot(area.width - ox, oy), Math.hypot(ox, area.height - oy), Math.hypot(area.width - ox, area.height - oy)) + 150;
  const light = glow(host, area, { x: ox, y: oy });

  const start = performance.now();
  let raf = requestAnimationFrame(function frame(now) {
    const t = Math.min(1, (now - start) / DURATION);
    if (t >= 1) return done();
    const radius = (maxRadius * (1 - 2 ** (-9 * t))) / (1 - 2 ** -9);
    const strength = Math.min(1, t / 0.03) * (1 - t ** 2.2);
    light?.draw(radius, strength);
    for (const it of items) {
      // a push outward just ahead of the ring and a pull back behind it, the same curve the light follows
      const z = ((it.dist - radius) / RING) * 0.75;
      const bump = Math.exp(-z * z) * strength;
      const push = PUSH * 2.33 * z * bump;
      it.el.style.transform = Math.abs(push) < 0.05 ? it.transform : `${it.transform} translate3d(${(it.ux * push).toFixed(2)}px,${(it.uy * push).toFixed(2)}px,0) scale(${(1 + LIFT * bump).toFixed(4)})`;
    }
    raf = requestAnimationFrame(frame);
  });
  return () => {
    cancelAnimationFrame(raf);
    light?.remove();
    for (const it of items) it.el.style.transform = it.transform;
  };
}

// The light: the four colours blended across the ring, fading out on both sides of it.
const FRAGMENT = `precision mediump float;
uniform vec2 size, center; uniform float radius, strength, ring, tint; uniform vec3 c0, c1, c2, c3;
void main(){
  vec2 pos=vec2(gl_FragCoord.x,size.y-gl_FragCoord.y);
  float x=(length(pos-center)-radius)/ring;
  float w0=exp(-pow((x+1.5)/0.8,2.0)), w1=exp(-pow((x+0.5)/0.8,2.0)), w2=exp(-pow((x-0.5)/0.8,2.0)), w3=exp(-pow((x-1.5)/0.8,2.0));
  vec3 color=(w0*c0+w1*c1+w2*c2+w3*c3)/(w0+w1+w2+w3+.0001);
  gl_FragColor=vec4(color,strength*exp(-1.1*pow(x/2.0,2.0))*tint);
}`;

/** The canvas of light over `area` (in the host, or over the window without the bars); null where WebGL is not available. */
function glow(host: HTMLElement | null, area: { width: number; height: number }, center: Point) {
  const dpr = Math.min(devicePixelRatio || 1, 1.5);
  const canvas = document.createElement("canvas");
  canvas.setAttribute("aria-hidden", "true");
  canvas.width = Math.ceil(area.width * dpr);
  canvas.height = Math.ceil(area.height * dpr);
  canvas.style.cssText = `position:${host ? "absolute" : "fixed"};left:0;top:0;width:${area.width}px;height:${area.height}px;pointer-events:none;z-index:${host ? 50 : 15}`;
  if (!host) {
    // the window minus the bars: the light passes under them, never over them
    const bars = [...document.querySelectorAll("[data-fx-chrome]")].map((e) => e.getBoundingClientRect()).filter((r) => r.width > 0 && r.height > 0);
    canvas.style.clipPath = `path(evenodd, "M0 0H${area.width}V${area.height}H0Z${bars.map((r) => `M${r.left} ${r.top}H${r.right}V${r.bottom}H${r.left}Z`).join("")}")`;
  }
  const gl = canvas.getContext("webgl", { alpha: true, premultipliedAlpha: false, antialias: false });
  if (!gl) return null;
  const program = gl.createProgram()!;
  for (const [type, src] of [
    [gl.VERTEX_SHADER, "attribute vec2 p;void main(){gl_Position=vec4(p,0.,1.);}"],
    [gl.FRAGMENT_SHADER, FRAGMENT],
  ] as const) {
    const s = gl.createShader(type)!;
    gl.shaderSource(s, src);
    gl.compileShader(s);
    gl.attachShader(program, s);
  }
  gl.linkProgram(program);
  if (!gl.getProgramParameter(program, gl.LINK_STATUS)) return null;
  gl.useProgram(program);
  gl.bindBuffer(gl.ARRAY_BUFFER, gl.createBuffer());
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);
  const p = gl.getAttribLocation(program, "p");
  gl.enableVertexAttribArray(p);
  gl.vertexAttribPointer(p, 2, gl.FLOAT, false, 0, 0);
  const at = (name: string) => gl.getUniformLocation(program, name);
  const dark = document.documentElement.dataset.theme !== "light";
  gl.uniform2f(at("size"), canvas.width, canvas.height);
  gl.uniform2f(at("center"), center.x * dpr, center.y * dpr);
  gl.uniform1f(at("ring"), RING * dpr);
  gl.uniform1f(at("tint"), dark ? 0.25 : 0.15);
  ringColors(document.documentElement.style.getPropertyValue("--accent").trim()).forEach((c, i) => gl.uniform3f(at(`c${i}`), ...c));
  gl.viewport(0, 0, canvas.width, canvas.height);
  const radius = at("radius");
  const strength = at("strength");
  (host ?? document.body).appendChild(canvas);
  return {
    draw(r: number, s: number) {
      gl.uniform1f(radius, r * dpr);
      gl.uniform1f(strength, s);
      gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
    },
    remove() {
      canvas.remove();
      gl.getExtension("WEBGL_lose_context")?.loseContext();
    },
  };
}
