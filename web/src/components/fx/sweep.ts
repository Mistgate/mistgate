// The sweep: a soft aurora band crosses an element and the element's new content appears behind it. Imperative and
// element-scoped; the React helpers are in sweep-react.tsx and the "something was created" glue in fresh.ts.
import { defaultAccent, isHex } from "@/lib/accent";
import { effectsEnabled, prefersReducedMotion } from "@/lib/effects";
import {
  axes,
  bandCenter,
  bandHalfWidth,
  envelope,
  resolveDirection,
  resolveEasing,
  resolvePalette,
  revealEdges,
  revealMask,
  type Axis,
  type CosinePalette,
  type Direction,
  type EasingFn,
  type EasingName,
  type PaletteName,
} from "./palette";
import { getPainter, releasePainter, viewportBox, type PaintJob } from "./renderer";

export type SweepOptions = {
  /** Which way the band goes. "inline" (default) follows the reading direction: right to left in Persian. */
  direction?: Direction;
  /** The travel in ms (the fade-out comes after it). Default 1100. */
  duration?: number;
  easing?: EasingName | EasingFn;
  /** A preset, "accent" (the panel's accent colour) or a palette of your own. Default "spectrum". */
  palette?: PaletteName | CosinePalette;
  /** The band's width as a share of the element along the sweep; smaller is tighter. Default 0.42. */
  bandWidth?: number;
  /** The band's strongest opacity, 0..1. Default 0.9 on light, 0.88 on dark. */
  peakAlpha?: number;
  /** Colour gain. Default 1 on light, 0.95 on dark (the band is added to a dark page, so it is a little dimmer there). */
  brightness?: number;
  /** How far the sine waves push the band's edge, 0..1. Default 0.5. */
  wave?: number;
  /** How far the noise ripples it, 0..1. Default 0.5. */
  ripple?: number;
  /** The fade-out after the travel, ms. Default 220. */
  fadeOut?: number;
  /** Reveal the element's content behind the band (a mask that follows it). Default true; false lays the glow over it. */
  reveal?: boolean;
  /** Wait this long before starting; a revealing sweep keeps the element hidden meanwhile. */
  delay?: number;
  /** Start only when this settles (a modal closing, say); a revealing sweep keeps the element hidden meanwhile unless `hideWhileHeld` is false. */
  hold?: Promise<unknown>;
  hideWhileHeld?: boolean;
  /** Cancels the sweep: the element is as it was and the promise resolves. */
  signal?: AbortSignal;
  /** Dev kit: paint the band paused at this progress (0..1) and never finish (until cancelled). */
  freeze?: number;
};

type Task = {
  id: number;
  el: HTMLElement;
  opts: SweepOptions;
  axis: Axis;
  palette: CosinePalette;
  ease: EasingFn;
  duration: number;
  fadeOut: number;
  dark: boolean;
  clips: HTMLElement[];
  born: number;
  start: number | null; // when the travel began; null while delayed or held
  holding: boolean;
  seed: number;
  radius: number;
  masked: boolean;
  prevMask: [string, string];
  done: () => void;
};

/** At most this many at once; a sweep asked for beyond it is skipped (the content simply appears). */
export const MAX_CONCURRENT = 6;

const tasks = new Map<HTMLElement, Task>();
let raf = 0;
let nextId = 1;

/** How many sweeps are running (tests, dev kit). */
export const activeSweeps = () => tasks.size;

/** The panel's accent as it is on the page now (index.css derives every tint from --accent on <html>). */
const accentNow = () => {
  const v = document.documentElement.style.getPropertyValue("--accent").trim();
  return isHex(v) ? v : defaultAccent;
};

/**
 * Plays the sweep over `element` and resolves when it is over, was cancelled, or was skipped (effects off, element gone,
 * too many at once). One sweep per element: a new one replaces the running one. Nothing is left behind: the mask is put back,
 * the overlay leaves the page and the frame loop stops with the last sweep.
 */
export function sweep(element: Element, options: SweepOptions = {}): Promise<void> {
  const el = element as HTMLElement;
  if (!effectsEnabled() || !el.isConnected || options.signal?.aborted) return Promise.resolve();
  if (prefersReducedMotion()) return fade(el, options);
  const running = tasks.get(el);
  if (running) finish(running);
  if (tasks.size >= MAX_CONCURRENT) return Promise.resolve();

  return new Promise<void>((resolve) => {
    const cs = getComputedStyle(el);
    const dark = document.documentElement.dataset.theme !== "light";
    const task: Task = {
      id: nextId++,
      el,
      opts: options,
      axis: resolveDirection(options.direction, cs.direction),
      palette: resolvePalette(options.palette, accentNow()),
      ease: resolveEasing(options.easing),
      duration: Math.max(50, options.duration ?? 1100),
      fadeOut: Math.max(1, options.fadeOut ?? 220),
      dark,
      clips: clipAncestors(el),
      born: performance.now(),
      start: null,
      holding: !!options.hold,
      seed: Math.random() * 100,
      radius: Number.parseFloat(cs.borderTopLeftRadius) || 0,
      masked: false,
      prevMask: [el.style.maskImage, el.style.getPropertyValue("-webkit-mask-image")],
      done: resolve,
    };
    tasks.set(el, task);
    options.signal?.addEventListener("abort", () => finish(task), { once: true });

    const delay = options.delay ?? 0;
    task.born = performance.now() + delay;
    if (options.hold) {
      const go = () => {
        task.holding = false;
        task.born = performance.now() + delay;
      };
      void options.hold.then(go, go);
    }
    const waiting = delay > 0 || task.holding;
    // hide the content now (a layout effect runs before paint): no flash of the finished state before the band arrives
    if (options.reveal !== false && (!waiting || options.hideWhileHeld !== false)) hide(task);
    if (!waiting) task.start = task.born;
    if (!raf) raf = requestAnimationFrame(frame);
  });
}

/** Stops the sweep on `element`, if one is running. */
export function cancelSweep(element: Element): void {
  const t = tasks.get(element as HTMLElement);
  if (t) finish(t);
}

function hide(t: Task) {
  if (t.masked) return;
  t.masked = true;
  const len = lengthOf(t.el, t.axis);
  const half = bandHalfWidth(t.opts.bandWidth ?? 0.42, len);
  setMask(t.el, revealMask(t.axis, len, revealEdges(bandCenter(0, len, half), half)));
}

function setMask(el: HTMLElement, v: string) {
  el.style.maskImage = v;
  el.style.setProperty("-webkit-mask-image", v);
}

function lengthOf(el: HTMLElement, axis: Axis): number {
  const r = el.getBoundingClientRect();
  return axes[axis].horizontal ? r.width : r.height;
}

function finish(t: Task) {
  if (tasks.get(t.el) !== t) return;
  tasks.delete(t.el);
  if (t.masked) {
    t.el.style.maskImage = t.prevMask[0];
    if (t.prevMask[1]) t.el.style.setProperty("-webkit-mask-image", t.prevMask[1]);
    else t.el.style.removeProperty("-webkit-mask-image");
  }
  t.done();
  if (tasks.size === 0) {
    cancelAnimationFrame(raf);
    raf = 0;
    releasePainter();
  }
}

/** Scroll containers (and anything else that clips) between the element and the page: the band must not paint outside them. */
function clipAncestors(el: HTMLElement): HTMLElement[] {
  const out: HTMLElement[] = [];
  for (let p = el.parentElement; p && p !== document.body && p !== document.documentElement; p = p.parentElement) {
    const cs = getComputedStyle(p);
    if ((cs.overflowX !== "visible" && cs.overflowX !== "") || (cs.overflowY !== "visible" && cs.overflowY !== "")) out.push(p);
  }
  return out;
}

function frame(now: number) {
  raf = 0;
  if (tasks.size === 0) return;
  const painter = getPainter();
  // dark pages get the band added to them (screen); light ones a normal, softer blend
  const dark = document.documentElement.dataset.theme !== "light";
  painter.begin(dark ? "screen" : "normal");
  const { w: vw, h: vh } = viewportBox();
  for (const t of [...tasks.values()]) {
    const el = t.el;
    if (!el.isConnected) {
      finish(t);
      continue;
    }
    if (t.start === null) {
      if (!t.holding && now >= t.born) {
        t.start = now;
        if (t.opts.reveal !== false) hide(t);
      } else {
        continue;
      }
    }
    const frozen = t.opts.freeze !== undefined;
    const elapsed = frozen ? Math.max(400, t.duration * t.opts.freeze!) : now - t.start;
    if (!frozen && elapsed >= t.duration + t.fadeOut) {
      finish(t);
      continue;
    }
    const r = el.getBoundingClientRect();
    const len = axes[t.axis].horizontal ? r.width : r.height;
    if (len < 1) continue; // laid out nowhere (hidden): nothing to draw this frame
    const half = bandHalfWidth(t.opts.bandWidth ?? 0.42, len);
    const p = frozen ? t.opts.freeze! : t.ease(Math.min(1, elapsed / t.duration));
    const center = bandCenter(p, len, half);
    if (t.opts.reveal !== false) setMask(el, revealMask(t.axis, len, revealEdges(center, half)));

    let x0 = Math.max(0, r.left);
    let y0 = Math.max(0, r.top);
    let x1 = Math.min(vw, r.right);
    let y1 = Math.min(vh, r.bottom);
    for (const c of t.clips) {
      const cr = c.getBoundingClientRect();
      x0 = Math.max(x0, cr.left);
      y0 = Math.max(y0, cr.top);
      x1 = Math.min(x1, cr.right);
      y1 = Math.min(y1, cr.bottom);
    }
    if (x1 <= x0 || y1 <= y0) continue;
    const job: PaintJob = {
      id: t.id,
      rect: { x: r.left, y: r.top, w: r.width, h: r.height },
      radius: t.radius,
      visible: { x: x0, y: y0, w: x1 - x0, h: y1 - y0 },
      axis: t.axis,
      center,
      half,
      alpha: (t.opts.peakAlpha ?? (t.dark ? 0.5 : 0.58)) * envelope(elapsed, t.duration, t.fadeOut),
      brightness: t.opts.brightness ?? (dark ? 0.8 : 0.95),
      wave: t.opts.wave ?? 0.5,
      ripple: t.opts.ripple ?? 0.5,
      time: elapsed / 1000,
      seed: t.seed,
      palette: t.palette,
    };
    painter.paint(job);
  }
  painter.end();
  if (tasks.size > 0) raf = requestAnimationFrame(frame);
}

/** Reduced motion: no band, no travel; what is new fades in over a moment (and only if the sweep would have revealed it). */
function fade(el: HTMLElement, options: SweepOptions): Promise<void> {
  if (options.reveal === false || typeof el.animate !== "function") return Promise.resolve();
  const wait = options.hold ? options.hold.then(undefined, () => undefined) : Promise.resolve();
  return wait.then(
    () =>
      new Promise<void>((resolve) => {
        if (!el.isConnected) return resolve();
        const a = el.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 160, easing: "ease-out" });
        a.onfinish = a.oncancel = () => resolve();
      }),
  );
}
