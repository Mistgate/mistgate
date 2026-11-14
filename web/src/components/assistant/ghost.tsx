import { useEffect, useRef } from "react";
import { prefersReducedMotion } from "@/lib/effects";

/**
 * The rest of a question the admin has started, offered in the message box: the first earlier question (this conversation's,
 * newest first, then the examples) that starts with what is typed. Nothing for a short start, several lines or a long text.
 */
export function suggest(text: string, pool: readonly string[]): string {
  if (text.length < 2 || text.length > 160 || text.includes("\n") || text !== text.trimStart()) return "";
  const head = text.toLowerCase();
  for (const p of pool) {
    const q = p.trim();
    if (q.length > text.length && !q.includes("\n") && q.toLowerCase().startsWith(head)) return q.slice(text.length);
  }
  return "";
}

// springs as "stiffness, damping"
const FADE = [300, 30] as const; // a letter appearing
const HOP = [520, 16] as const; // a letter landing when accepted
const KEY = [520, 24] as const; // the Tab key
const HOP_KICK = -70; // px/s upwards
const KEY_KICK = -9;

type Spring = { x: number; v: number; t: number; k: number; c: number };
const spring = (x: number, [k, c]: readonly [number, number], still: boolean): Spring => ({ x, v: 0, t: x, k, c: still ? 2 * Math.sqrt(k) : c });
const step = (s: Spring, dt: number) => {
  s.v += (-s.k * (s.x - s.t) - s.c * s.v) * dt;
  s.x += s.v * dt;
};
const clamp = (v: number, a: number, b: number) => Math.max(a, Math.min(b, v));

/**
 * The suggestion drawn over the message box, in the box's own type: the typed text (invisible while the box shows it), then the
 * rest dim, letter by letter, and a Tab key. `accepted` plays the acceptance: each letter turns to the text colour with a small
 * hop, left to right, the key goes down and away, then `onDone`. Words do not break in the middle.
 */
export function Ghost({ base, rest, accepted, onAccept, onDone, label, className }: { base: string; rest: string; accepted: boolean; onAccept: () => void; onDone: () => void; label: string; className?: string }) {
  const root = useRef<HTMLDivElement>(null);
  const done = useRef(onDone);
  useEffect(() => {
    done.current = onDone;
  });
  // the springs live across offer → accept of one suggestion, so the hop starts from where the letter is
  const state = useRef<{ key: string; o: Spring[]; y: Spring[]; tab: Spring } | null>(null);

  useEffect(() => {
    const el = root.current;
    if (!el) return;
    const letters = [...el.querySelectorAll<HTMLElement>("[data-g]")];
    const tab = el.querySelector<HTMLElement>("[data-tab]");
    const still = prefersReducedMotion();
    // typing along the suggestion keeps it lit: only a new sentence fades in
    const key = base + rest;
    const same = state.current?.key === key;
    if (!same || state.current!.o.length !== letters.length) {
      state.current = { key, o: letters.map(() => spring(same || accepted ? 1 : 0, FADE, still)), y: letters.map(() => spring(0, HOP, still)), tab: same ? state.current!.tab : spring(0, KEY, still) };
    }
    const s = state.current!;
    const timers: number[] = [];
    if (accepted) {
      s.tab.v = still ? 0 : KEY_KICK;
      s.tab.t = 0;
      letters.forEach((l, i) =>
        timers.push(
          window.setTimeout(() => {
            l.dataset.ok = "1";
            s.o[i]!.t = 1;
            if (!still) s.y[i]!.v = HOP_KICK;
          }, i * 16),
        ),
      );
      timers.push(window.setTimeout(() => done.current(), letters.length * 16 + 420));
    } else {
      s.tab.t = 1;
      letters.forEach((_, i) => timers.push(window.setTimeout(() => (s.o[i]!.t = 1), i * 8)));
    }
    let raf = 0;
    let last = performance.now();
    const frame = (now: number) => {
      const dt = Math.min((now - last) / 1000, 1 / 30) / 4;
      last = now;
      for (let n = 0; n < 4; n++) {
        step(s.tab, dt);
        for (let i = 0; i < letters.length; i++) {
          step(s.o[i]!, dt);
          step(s.y[i]!, dt);
        }
      }
      letters.forEach((l, i) => {
        l.style.opacity = clamp(s.o[i]!.x, 0, 1).toFixed(3);
        l.style.transform = `translateY(${s.y[i]!.x.toFixed(2)}px)`;
      });
      if (tab) {
        const t = clamp(s.tab.x, 0, 1.2);
        tab.style.opacity = clamp(t, 0, 1).toFixed(3);
        tab.style.transform = `scale(${(0.6 + 0.4 * t).toFixed(4)})`;
      }
      raf = requestAnimationFrame(frame);
    };
    raf = requestAnimationFrame(frame);
    return () => {
      cancelAnimationFrame(raf);
      timers.forEach(clearTimeout);
    };
  }, [base, rest, accepted]);

  return (
    <div ref={root} aria-hidden className={className}>
      <span className={accepted ? undefined : "invisible"}>{base}</span>
      {rest.split(/( )/).map((part, w) =>
        part === " " || part === "" ? (
          part
        ) : (
          <span key={w} className="whitespace-nowrap">
            {[...part].map((ch, i) => (
              <span key={i} data-g className="inline-block text-muted opacity-0 transition-colors duration-200 will-change-transform data-[ok]:text-fg">
                {ch}
              </span>
            ))}
          </span>
        ),
      )}
      <button
        type="button"
        data-tab
        tabIndex={-1}
        title={label}
        onMouseDown={(e) => e.preventDefault()}
        onClick={onAccept}
        className="pointer-events-auto ms-2 inline-block rounded-md bg-surface-2 px-1.5 align-[0.08em] text-[10.5px] leading-[18px] font-semibold text-muted opacity-0 will-change-transform hover:text-fg"
      >
        Tab
      </button>
    </div>
  );
}
