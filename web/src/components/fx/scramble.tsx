import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties } from "react";
import { prefersReducedMotion } from "@/lib/effects";
import { cx } from "@/lib/cx";
import "./scramble.css";

// A number that settles in: every digit and letter of a figure is shuffled for a moment, slower and slower, and lands in its
// place one after the other with a small spring. It plays once, when the figure is first at least 60% in view (the redesign
// board's "Scramble text"); a changed value afterwards is simply set. Reduced motion, no IntersectionObserver (tests) and a
// figure that is already out of sight draw it still. The real text stays in the element all the time (screen readers read it,
// the layout does not move); the shuffle is a copy laid over it.

type Spring = { x: number; v: number; k: number; c: number };
type Cell = { ch: string; kind: 0 | 1 | 2 | 3; t: number; dur: number; wait: number; done: boolean; y: Spring; o: Spring };

const digits = "0123456789";
const letters = "abcdefghijklmnopqrstuvwxyz";
const kindOf = (ch: string): Cell["kind"] => (/[0-9]/.test(ch) ? 1 : /[a-z]/.test(ch) ? 2 : /[A-Z]/.test(ch) ? 3 : 0);
const junk = (kind: Cell["kind"]) => (kind === 1 ? digits[Math.floor(Math.random() * 10)]! : kind === 2 ? letters[Math.floor(Math.random() * 26)]! : letters[Math.floor(Math.random() * 26)]!.toUpperCase());
const spring = (k: number, c: number): Spring => ({ x: 0, v: 0, k, c });
const step = (s: Spring, h: number) => {
  s.v += (s.k * (1 - s.x) - s.c * s.v) * h;
  s.x += s.v * h;
};

function cellsOf(text: string, delay: number): Cell[] {
  const gap = Math.min(0.05, 0.5 / Math.max(1, text.length));
  return [...text].map((ch, i) => {
    const kind = kindOf(ch);
    return { ch, kind, t: -delay - i * gap, dur: kind ? 0.3 : 0, wait: 0, done: false, y: spring(280, 22), o: spring(420, 32) };
  });
}

// one observer for all the figures of a page; the figures that come into view together start a little apart
const starts = new Map<Element, (delay: number) => void>();
let observer: IntersectionObserver | null = null;
function watch(el: Element, start: (delay: number) => void): () => void {
  if (typeof IntersectionObserver === "undefined") return () => {};
  observer ??= new IntersectionObserver(
    (entries) => {
      let k = 0;
      for (const e of entries) {
        const go = starts.get(e.target);
        if (!e.isIntersecting || !go) continue;
        starts.delete(e.target);
        observer?.unobserve(e.target);
        go(0.04 * Math.min(k++, 10));
      }
    },
    { threshold: 0.6 },
  );
  starts.set(el, start);
  observer.observe(el);
  return () => {
    starts.delete(el);
    observer?.unobserve(el);
  };
}

/** A figure (`text`) that scrambles in when first seen. Give it the classes of the cell it fills (font, alignment). */
export function Scramble({ text, className, style, title }: { text: string; className?: string; style?: CSSProperties; title?: string }) {
  const host = useRef<HTMLSpanElement>(null);
  const [cells, setCells] = useState<Cell[] | null>(null);
  const spans = useRef<(HTMLSpanElement | null)[]>([]);
  const seen = useRef(false);
  const latest = useRef(text);
  useLayoutEffect(() => {
    latest.current = text;
  });

  useEffect(() => {
    const el = host.current;
    if (!el || seen.current || prefersReducedMotion()) return;
    return watch(el, (delay) => {
      seen.current = true;
      if (latest.current) setCells(cellsOf(latest.current, delay));
    });
  }, []);

  // the frames: shuffle the cells that are still scrambling, then let every cell settle on its springs
  useEffect(() => {
    if (!cells) return;
    let raf = 0;
    let last = performance.now();
    const tick = (now: number) => {
      const dt = Math.min(1 / 30, (now - last) / 1000 || 1 / 60);
      last = now;
      let rest = true;
      cells.forEach((c, i) => {
        const el = spans.current[i];
        if (!el) return;
        c.t += dt;
        if (c.t < 0) {
          rest = false;
          return;
        }
        if (!c.done) {
          if (c.kind && c.t < c.dur) {
            rest = false;
            c.wait -= dt;
            if (c.wait <= 0) {
              el.textContent = junk(c.kind);
              c.wait = 0.035 + 0.11 * (c.t / c.dur) * (c.t / c.dur);
            }
            return;
          }
          c.done = true;
          el.textContent = c.ch;
          el.classList.add("in");
        }
        for (let n = 0; n < 4; n++) {
          step(c.y, dt / 4);
          step(c.o, dt / 4);
        }
        const settled = Math.abs(1 - c.y.x) < 0.004 && Math.abs(c.y.v) < 0.02;
        if (!settled) rest = false;
        el.style.transform = settled ? "" : `translateY(${((1 - c.y.x) * 0.22).toFixed(3)}em)`;
        el.style.opacity = settled ? "" : String(Math.max(0, Math.min(1, 0.4 + 0.6 * c.o.x)));
      });
      if (rest) setCells(null);
      else raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [cells]);

  return (
    <span ref={host} title={title} style={style} className={cx("relative", className)}>
      {/* the text keeps its place and is hidden only while the copy plays over it */}
      <span className={cells ? "text-transparent" : undefined}>{text}</span>
      {cells && (
        <span aria-hidden className="pointer-events-none absolute inset-0 [text-align:inherit] whitespace-pre">
          {cells.map((c, i) => (
            <span
              key={i}
              ref={(el) => {
                spans.current[i] = el;
                // the first look of a cell: a glyph of its kind, or the mark itself
                if (el && !el.textContent) el.textContent = c.kind ? junk(c.kind) : c.ch;
              }}
              className="fz-scr-c"
            />
          ))}
        </span>
      )}
    </span>
  );
}
