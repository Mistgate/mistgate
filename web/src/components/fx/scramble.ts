// Scramble text, the redesign board's number effect (PnOverview, data-scr): every character is shuffled among its own kind
// (a digit stays a digit, a letter a letter) ever more slowly and then settles into place, one after another; the landing is a
// spring (280 / 22) and the fade-in another (420 / 32). The engine works on the DOM directly (one requestAnimationFrame loop
// for every element that plays), so a number that changes every ten seconds costs no React renders.
// Nothing here checks the effects switch or reduced motion: the caller does (scramble-react.tsx).

type Spring = { x: number; v: number; k: number; c: number };
type Cell = { ch: string; kind: 0 | 1 | 2 | 3; el: HTMLSpanElement; t: number; dur: number; wait: number; done: boolean; y: Spring; o: Spring };
type Run = { to: string; cells: Cell[] };

const spring = (k: number, c: number): Spring => ({ x: 0, v: 0, k, c });
const step = (s: Spring, h: number) => {
  s.v += (s.k * (1 - s.x) - s.c * s.v) * h;
  s.x += s.v * h;
};

const kindOf = (ch: string): Cell["kind"] => (/[0-9]/.test(ch) ? 1 : /[a-z]/.test(ch) ? 2 : /[A-Z]/.test(ch) ? 3 : 0);
const junk = (kind: Cell["kind"]) => {
  if (kind === 1) return String((Math.random() * 10) | 0);
  const l = String.fromCharCode(97 + ((Math.random() * 26) | 0));
  return kind === 2 ? l : l.toUpperCase();
};

const runs = new WeakMap<HTMLElement, Run>();
const live = new Set<HTMLElement>();
let raf = 0;
let last = 0;

function tick(now: number) {
  const dt = Math.min(1 / 30, (now - last) / 1000 || 1 / 60);
  last = now;
  live.forEach((el) => {
    const run = runs.get(el);
    if (!el.isConnected || !run) {
      live.delete(el);
      return;
    }
    let rest = true;
    for (const c of run.cells) {
      c.t += dt;
      if (c.t < 0) {
        rest = false;
        continue;
      }
      if (!c.done) {
        if (c.kind && c.t < c.dur) {
          rest = false;
          c.wait -= dt;
          if (c.wait <= 0) {
            c.el.textContent = junk(c.kind);
            c.wait = 0.035 + 0.11 * (c.t / c.dur) * (c.t / c.dur);
          }
          continue;
        }
        c.done = true;
        c.el.textContent = c.ch;
        c.el.className = "scr-c in";
      }
      for (let i = 0; i < 4; i++) {
        step(c.y, dt / 4);
        step(c.o, dt / 4);
      }
      const settled = Math.abs(1 - c.y.x) < 0.004 && Math.abs(c.y.v) < 0.02;
      if (!settled) rest = false;
      c.el.style.transform = settled ? "" : `translateY(${((1 - c.y.x) * 0.22).toFixed(3)}em)`;
      c.el.style.opacity = settled ? "" : String(Math.max(0, Math.min(1, 0.4 + 0.6 * c.o.x)));
    }
    if (rest) {
      el.textContent = run.to;
      runs.delete(el);
      live.delete(el);
    }
  });
  raf = live.size ? requestAnimationFrame(tick) : 0;
}

/** Stops whatever plays on `el` and shows `to` as plain text. */
export function settle(el: HTMLElement, to: string) {
  runs.delete(el);
  live.delete(el);
  if (!live.size && raf) {
    cancelAnimationFrame(raf);
    raf = 0;
  }
  if (el.textContent !== to) el.textContent = to;
}

/** Plays `to` on `el` after `delay` seconds: the characters shuffle, then land one by one. */
export function play(el: HTMLElement, to: string, delay = 0) {
  if (!to) return settle(el, to);
  const n = to.length;
  const gap = Math.min(0.05, 0.5 / n);
  el.textContent = "";
  const cells = [...to].map((ch, i): Cell => {
    const span = document.createElement("span");
    const kind = kindOf(ch);
    span.className = "scr-c";
    span.textContent = kind ? junk(kind) : ch;
    el.appendChild(span);
    return { ch, kind, el: span, t: -delay - i * gap, dur: kind ? 0.3 : 0, wait: 0, done: false, y: spring(280, 22), o: spring(420, 32) };
  });
  runs.set(el, { to, cells });
  live.add(el);
  if (!raf) {
    last = performance.now();
    raf = requestAnimationFrame(tick);
  }
}

/** Whether something is playing on `el` (tests, and a value that arrives mid-way). */
export const playing = (el: HTMLElement) => live.has(el);

// ---- watching: a number plays when it first shows (60% of it in view) and again whenever its value changes while in view

type Watch = { to: string; inView: boolean; once: boolean; played: boolean };
const watched = new WeakMap<HTMLElement, Watch>();
let io: IntersectionObserver | null = null;

function observer(): IntersectionObserver | null {
  if (io || typeof IntersectionObserver === "undefined") return io;
  io = new IntersectionObserver(
    (entries) => {
      let k = 0;
      for (const e of entries) {
        const el = e.target as HTMLElement;
        const w = watched.get(el);
        if (!w) continue;
        if (e.isIntersecting && !w.inView) {
          w.inView = true;
          if (w.once && w.played) continue;
          w.played = true;
          play(el, w.to, 0.04 * Math.min(k++, 10));
        } else if (!e.isIntersecting) w.inView = false;
      }
    },
    { threshold: 0.6 },
  );
  return io;
}

/**
 * Keeps `el` showing `to`: the first time the text is simply there, and plays once the element is in view (at once without
 * an IntersectionObserver); a later change plays at once while the element is in view and is set plainly while it is not.
 * With "once" it plays that first time only: coming into view again, and a later change (a figure that polling updates every
 * few seconds), just show the text.
 */
export function watch(el: HTMLElement, to: string, once = false) {
  const w = watched.get(el);
  if (!w) {
    watched.set(el, { to, inView: false, once, played: !observer() });
    el.textContent = to;
    const o = observer();
    if (o) o.observe(el);
    else play(el, to);
    return;
  }
  if (w.to === to) return;
  w.to = to;
  if (w.once && w.played) settle(el, to);
  else if (w.inView || !io) play(el, to);
  else settle(el, to);
}

/** Stops watching `el` (it left the page) and shows its value plainly. */
export function unwatch(el: HTMLElement) {
  const w = watched.get(el);
  io?.unobserve(el);
  watched.delete(el);
  if (w) settle(el, w.to);
}
