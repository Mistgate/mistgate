// The motion of the boards that CSS cannot do (the script of Main.dc.html, ported): the figures that are written in letter by
// letter (scramble) and the QR code that leans after the pointer. The page is rebuilt on every change, so what a figure played
// is remembered by its key, not by its element. Nothing runs under prefers-reduced-motion, and nothing on a phone (the phone
// boards have no .u-anim).

const calm = () => typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;

/** The page plays its entrance (the boards' .u-anim) from the computer layout up, and not for people who asked for calm. */
export const animated = () => typeof matchMedia === "function" && matchMedia("(min-width: 720px)").matches;

// ---- scramble: every character is shuffled ever slower and settles in turn; the landing is the "settle" spring (280 / 22), the fade the "fade" one (420 / 32) ----

type Spring = { x: number; v: number; k: number; c: number };
type Cell = { ch: string; k: number; el: HTMLElement; t: number; dur: number; w: number; done: boolean; y: Spring; o: Spring };
type Live = HTMLElement & { scrRun?: { to: string; cs: Cell[] } };

const digits = "0123456789";
const letters = "abcdefghijklmnopqrstuvwxyz";
const kind = (ch: string) => (/[0-9]/.test(ch) ? 1 : /[a-z]/.test(ch) ? 2 : /[A-Z]/.test(ch) ? 3 : 0);
const junk = (k: number) => (k === 1 ? digits[(Math.random() * 10) | 0]! : k === 2 ? letters[(Math.random() * 26) | 0]! : letters[(Math.random() * 26) | 0]!.toUpperCase());
const spring = (k: number, c: number): Spring => ({ x: 0, v: 0, k, c });
const step = (sp: Spring, h: number) => {
  sp.v += (sp.k * (1 - sp.x) - sp.c * sp.v) * h;
  sp.x += sp.v * h;
};

const live = new Set<Live>();
let raf = 0;
let last = 0;

function tick(now: number) {
  const dt = Math.min(1 / 30, (now - last) / 1000 || 1 / 60);
  last = now;
  live.forEach((el) => {
    const a = el.scrRun;
    if (!el.isConnected || !a) {
      live.delete(el);
      return;
    }
    let rest = true;
    a.cs.forEach((c) => {
      c.t += dt;
      if (c.t < 0) {
        rest = false;
        return;
      }
      if (!c.done) {
        if (c.k && c.t < c.dur) {
          rest = false;
          c.w -= dt;
          if (c.w <= 0) {
            c.el.textContent = junk(c.k);
            c.w = 0.035 + 0.11 * (c.t / c.dur) * (c.t / c.dur);
          }
          return;
        }
        c.done = true;
        c.el.textContent = c.ch;
        c.el.className = "scr-c in";
      }
      for (let i = 0; i < 4; i++) {
        step(c.y, dt / 4);
        step(c.o, dt / 4);
      }
      const yr = Math.abs(1 - c.y.x) < 0.004 && Math.abs(c.y.v) < 0.02;
      if (!yr) rest = false;
      c.el.style.transform = yr ? "" : `translateY(${((1 - c.y.x) * 0.22).toFixed(3)}em)`;
      c.el.style.opacity = yr ? "" : String(Math.max(0, Math.min(1, 0.4 + 0.6 * c.o.x)));
    });
    if (rest) {
      el.textContent = a.to;
      live.delete(el);
    }
  });
  raf = live.size ? requestAnimationFrame(tick) : 0;
}

function play(el: Live, delay: number) {
  const to = el.dataset.scr ?? "";
  if (!to || calm()) {
    live.delete(el);
    el.textContent = to;
    return;
  }
  const n = to.length;
  const gap = Math.min(0.05, 0.5 / n);
  el.textContent = "";
  const cs = to.split("").map((ch, i): Cell => {
    const sp = document.createElement("span");
    const k = kind(ch);
    sp.className = "scr-c";
    sp.textContent = k ? junk(k) : ch;
    el.append(sp);
    return { ch, k, el: sp, t: -delay - i * gap, dur: k ? 0.3 : 0, w: 0, done: false, y: spring(280, 22), o: spring(420, 32) };
  });
  el.scrRun = { to, cs };
  live.add(el);
  if (!raf) {
    last = performance.now();
    raf = requestAnimationFrame(tick);
  }
}

/** What each figure (by its key) last showed: a figure that shows the same again is not played again, one that changed is. */
const shown = new Map<string, string>();
const entered = new WeakSet<Element>();
let io: IntersectionObserver | null = null;

/**
 * Called after the page is drawn: every [data-scr] under `.u-anim` is written in when it comes into view for the first time and
 * written in again when its value changes; one that did not change shows its text at once. [data-scr-k] names the figure.
 */
export function scramble(root: ParentNode) {
  if (!animated()) return;
  const els = [...root.querySelectorAll<Live>(".u-anim [data-scr]")];
  if (els.length === 0) return;
  const seen = (el: Live) => `${el.dataset.scrK ?? ""}`;
  if (!io && typeof IntersectionObserver !== "undefined") {
    let k = 0;
    io = new IntersectionObserver(
      (es) => {
        k = 0;
        for (const e of es) {
          const el = e.target as Live;
          if (!el.isConnected || !e.isIntersecting || entered.has(el)) continue;
          entered.add(el);
          const key = seen(el);
          // a figure plays when it first comes into view; the one that was already played shows as it is
          if (key && shown.get(key) === el.dataset.scr) el.textContent = el.dataset.scr ?? "";
          else play(el, 0.04 * Math.min(k++, 10));
          if (key) shown.set(key, el.dataset.scr ?? "");
        }
      },
      { threshold: 0.6 },
    );
  }
  for (const el of els) {
    const to = el.dataset.scr ?? "";
    const key = seen(el);
    el.textContent = to;
    const before = key ? shown.get(key) : undefined;
    if (before === to) continue; // the same figure, drawn again: no replay
    if (before === undefined || !io) {
      // first time: written in when it is on screen
      if (io) io.observe(el);
      else {
        if (key) shown.set(key, to);
        play(el, 0);
      }
    } else {
      // the value changed: written in again where it stands
      shown.set(key, to);
      entered.add(el);
      play(el, 0);
    }
  }
}

// ---- the QR card leans after the pointer: the "follow" spring (900 / 50) with up to 14 degrees, the return the "settle" one (280 / 22), the logo 12 px nearer ----

const q = { el: null as HTMLElement | null, x: 0, y: 0, h: 0, vx: 0, vy: 0, vh: 0, tx: 0, ty: 0, th: 0, k: 900, c: 50, raf: 0, last: 0 };

function qTick(now: number) {
  const dt = Math.min(1 / 30, (now - q.last) / 1000 || 1 / 60);
  q.last = now;
  for (let i = 0; i < 4; i++) {
    const h = dt / 4;
    q.vx += (q.k * (q.tx - q.x) - q.c * q.vx) * h;
    q.x += q.vx * h;
    q.vy += (q.k * (q.ty - q.y) - q.c * q.vy) * h;
    q.y += q.vy * h;
    q.vh += (280 * (q.th - q.h) - 22 * q.vh) * h;
    q.h += q.vh * h;
  }
  const el = q.el;
  const rest = !q.th && Math.abs(q.x) < 0.002 && Math.abs(q.y) < 0.002 && Math.abs(q.h) < 0.002 && Math.abs(q.vx) + Math.abs(q.vy) + Math.abs(q.vh) < 0.02;
  if (el && el.isConnected) {
    if (rest) for (const k of ["--rx", "--ry", "--hv", "--gx", "--gy"]) el.style.removeProperty(k);
    else {
      el.style.setProperty("--rx", `${(-q.y * 14).toFixed(2)}deg`);
      el.style.setProperty("--ry", `${(q.x * 14).toFixed(2)}deg`);
      el.style.setProperty("--hv", Math.max(0, q.h).toFixed(3));
      el.style.setProperty("--gx", `${(50 + q.x * 50).toFixed(1)}%`);
      el.style.setProperty("--gy", `${(50 + q.y * 50).toFixed(1)}%`);
    }
  }
  q.raf = rest || !el || !el.isConnected ? 0 : requestAnimationFrame(qTick);
}
const qRun = () => {
  if (!q.raf) {
    q.last = performance.now();
    q.raf = requestAnimationFrame(qTick);
  }
};

/** The handlers of a .u-tilt element (the card with the glare child). */
export const tilt = {
  move(e: Event) {
    if (calm()) return;
    const ev = e as MouseEvent;
    const el = ev.currentTarget as HTMLElement;
    const r = el.getBoundingClientRect();
    q.el = el;
    q.tx = Math.max(-1, Math.min(1, ((ev.clientX - r.left) / r.width - 0.5) * 2));
    q.ty = Math.max(-1, Math.min(1, ((ev.clientY - r.top) / r.height - 0.5) * 2));
    q.th = 1;
    q.k = 900;
    q.c = 50;
    qRun();
  },
  leave() {
    q.tx = 0;
    q.ty = 0;
    q.th = 0;
    q.k = 280;
    q.c = 22;
    qRun();
  },
};
