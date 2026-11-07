import { createElement, useCallback, useLayoutEffect, useRef, type HTMLAttributes, type ReactNode } from "react";
import { sweep, type SweepOptions } from "./sweep";

/**
 * `const { ref, play } = useSweep<HTMLDivElement>(defaults)`: ref goes on the element; play(options?) sweeps it and resolves
 * when it is over (or at once when the effects are off). A sweep whose element unmounts ends by itself.
 */
export function useSweep<T extends HTMLElement = HTMLElement>(defaults?: SweepOptions) {
  const ref = useRef<T>(null);
  const base = useRef(defaults);
  useLayoutEffect(() => {
    base.current = defaults;
  });
  const play = useCallback((options?: SweepOptions) => (ref.current ? sweep(ref.current, { ...base.current, ...options }) : Promise.resolve()), []);
  return { ref, play };
}

/** The ref for <Sweep>: sweeps its element when `play` changes to something truthy (and, with `onMount`, when it first renders truthy). */
function useSweepWhen(play: unknown, onMount: boolean | undefined, opts: SweepOptions) {
  const ref = useRef<HTMLElement>(null);
  const first = useRef(true);
  const prev = useRef<unknown>(play);
  const latest = useRef(opts);
  useLayoutEffect(() => {
    latest.current = opts;
  });
  useLayoutEffect(() => {
    const el = ref.current;
    const changed = prev.current !== play;
    const wasFirst = first.current;
    first.current = false;
    prev.current = play;
    if (!el || !play || !(changed || (wasFirst && onMount))) return;
    const clean = Object.fromEntries(Object.entries(latest.current).filter(([, v]) => v !== undefined)) as SweepOptions;
    void sweep(el, clean);
  }, [play, onMount]);
  // no cancel on unmount: a sweep ends by itself when its element has left the page (and StrictMode mounts twice)
  return ref;
}

type Tag = "div" | "section" | "span" | "li" | "article";
type SweepProps = SweepOptions &
  Omit<HTMLAttributes<HTMLElement>, "children"> & {
    /** The sweep plays whenever this changes to something truthy (a counter, a status that became "ready", an id). Not on first render, unless `onMount`. */
    play?: unknown;
    onMount?: boolean;
    as?: Tag;
    children?: ReactNode;
  };

/**
 * `<Sweep play={key}>content</Sweep>`: sweeps over its content each time `play` changes to a truthy value. The change and the
 * new content come in the same render, so the content that arrives is the content revealed behind the band.
 */
export function Sweep({ play, onMount, as = "div", children, direction, duration, easing, palette, bandWidth, peakAlpha, brightness, wave, ripple, fadeOut, reveal, delay, hold, hideWhileHeld, signal, freeze, ...rest }: SweepProps) {
  const ref = useSweepWhen(play, onMount, { direction, duration, easing, palette, bandWidth, peakAlpha, brightness, wave, ripple, fadeOut, reveal, delay, hold, hideWhileHeld, signal, freeze });
  return createElement(as, { ...rest, ref }, children);
}
