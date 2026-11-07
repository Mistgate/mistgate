// Pages. Opening a page is quiet: the whole screen settles in at once (.screen-enter, 200 ms, from the first frame) and nothing
// that is already drawn is hidden again. The aurora band plays once, softly, when the panel first appears after sign-in, as
// light passing over content that is already there (no mask), and never over a "Loading…". A screen's main list that had to
// be waited for appears as one; a list that came from the cache is simply there.
import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type RefObject } from "react";
import { effectsFor, prefersReducedMotion } from "@/lib/effects";
import { blockedByDialog } from "./fresh";
import { cancelSweep, sweep, type SweepOptions } from "./sweep";

/** How long the first sweep waits for the page's first content; a page still loading after that gets no sweep. */
export const READY_CAP_MS = 1500;
/** A list's rows after a sweep (or while one is under way) keep still for this long. */
const QUIET_MS = 1500;

const state = { navAt: 0, listAt: -Infinity, pending: false, endedAt: -Infinity };
const now = () => performance.now();

/** Called when a navigation begins (the shell subscribes the router to it). */
/** A list took its entrance: the page sweep of this navigation stays out. */
const noteListEntrance = () => void (state.listAt = now());
export const noteNavigation = () => void (state.navAt = now());
/** Tests. */
export const resetPageFx = () => Object.assign(state, { navAt: 0, listAt: -Infinity, pending: false, endedAt: -Infinity });

const sweepRecent = () => state.pending || now() - state.endedAt < QUIET_MS;

const dark = () => document.documentElement.dataset.theme !== "light";

/** A page opening: light and quick, over the content column. */
export function pageSweepOptions(extra: SweepOptions = {}): SweepOptions {
  return { duration: 720, fadeOut: 200, bandWidth: 0.5, wave: 0.4, ripple: 0.4, radius: 16, peakAlpha: dark() ? 0.44 : 0.44, ...extra };
}

/** The first sweep of the shell after sign-in or a reload: slower and softer still. */
export function firstSweepOptions(extra: SweepOptions = {}): SweepOptions {
  return pageSweepOptions({ duration: 950, peakAlpha: dark() ? 0.34 : 0.36, ...extra });
}

/**
 * The window box a page sweep may paint in: the content area, never the sidebar (it starts at the page column's left edge),
 * the sticky top bars or the dock (marked data-fx-chrome="top" | "bottom").
 */
export function contentBox(main: Element): () => { left: number; top: number; right: number; bottom: number } {
  return () => {
    const r = main.getBoundingClientRect();
    let top = 0;
    document.querySelectorAll('[data-fx-chrome="top"]').forEach((e) => void (top = Math.max(top, e.getBoundingClientRect().bottom)));
    let bottom = document.documentElement.clientHeight;
    document.querySelectorAll('[data-fx-chrome="bottom"]').forEach((e) => {
      const b = e.getBoundingClientRect();
      if (b.height > 0) bottom = Math.min(bottom, b.top);
    });
    return { left: r.left, top, right: r.right, bottom };
  };
}

/** Resolves true when nothing in `el` says "loading" any more (data-fx-pending), false when the cap passes first. */
function whenReady(el: Element, cap: number): Promise<boolean> {
  return new Promise((resolve) => {
    const t0 = now();
    const poll = () => {
      if (!el.isConnected) return resolve(false);
      if (!el.querySelector("[data-fx-pending]")) return resolve(true);
      if (now() - t0 >= cap) return resolve(false);
      requestAnimationFrame(poll);
    };
    poll();
  });
}

/** Plays the page (or first-load) sweep on `el` once its content is ready; skipped when a list took the moment, or a dialog is open. */
export function playPage(el: HTMLElement, first: boolean, extra?: SweepOptions): Promise<void> {
  if (!effectsFor("pages") || prefersReducedMotion()) return Promise.resolve();
  const since = state.navAt;
  state.pending = true;
  // light over what is drawn, never hiding it: no mask, nothing held invisible, no band over a page still loading
  const ready = whenReady(el, READY_CAP_MS).then((ok) => {
    if (!ok || blockedByDialog(el) || (!first && state.listAt >= since)) cancelSweep(el);
  });
  return sweep(el, { ...(first ? firstSweepOptions(extra) : pageSweepOptions(extra)), reveal: false, hold: ready, hideWhileHeld: false, clipBox: contentBox(el.closest("main") ?? el) }).finally(() => {
    state.pending = false;
    state.endedAt = now();
  });
}

/**
 * Plays the band once, when the shell first appears (after sign-in or a reload). Moving between pages does not sweep: the
 * screen settles in by itself (.screen-enter); a band on every page was too much. `pathname` comes from the router.
 */
export function usePageTransition(ref: RefObject<HTMLElement | null>, _pathname: string, enabled = true) {
  const first = useRef(true);
  useLayoutEffect(() => {
    const isFirst = first.current;
    first.current = false;
    if (!enabled || !isFirst || !ref.current) return;
    // no cancel on cleanup: StrictMode runs this twice at mount, and a sweep ends by itself when its element goes
    void playPage(ref.current, isFirst);
  }, [enabled, ref]);
}

// --- lists ---

/**
 * The entrance of a screen's main list when its first data had to be waited for: every row fades in together (the list
 * appears as one, not row by row). A list ready on the first render (from the cache) is simply there; refetches, polling
 * and filters never play it; nor do reduced motion, the effects off, or a band that is playing.
 */
export function useListEntrance(ready: boolean): (i: number) => CSSProperties | undefined {
  const [mode, setMode] = useState<"wait" | "play" | "done">(ready ? "done" : "wait");
  if (ready && mode === "wait") {
    // decided once, while rendering the first data (so the rows are drawn with their style, never without it first)
    const go = effectsFor("pages") && !prefersReducedMotion() && !sweepRecent();
    if (go) noteListEntrance();
    setMode(go ? "play" : "done");
  }
  useEffect(() => {
    if (mode !== "play") return;
    const t = setTimeout(() => setMode("done"), 1000);
    return () => clearTimeout(t);
  }, [mode]);
  return () => (mode === "play" ? { animation: "mg-list-in 200ms ease-out both" } : undefined);
}
