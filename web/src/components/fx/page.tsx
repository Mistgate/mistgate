// Pages: the sweep when a page opens (and, softer, when the shell first appears after sign-in), and the staggered entrance of a
// screen's main list on its first data. A screen gets one of the two: a list that is ready in time takes the page's moment
// for itself (the sweep then stays out), otherwise the sweep reveals the page and the list does not move again.
import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type RefObject } from "react";
import { effectsFor, prefersReducedMotion } from "@/lib/effects";
import { blockedByDialog } from "./fresh";
import { cancelSweep, sweep, type SweepOptions } from "./sweep";

/** How long a page waits for its first meaningful content before sweeping anyway. */
export const READY_CAP_MS = 400;
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

/** Resolves when nothing in `el` says "loading" any more (data-fx-pending), or after the cap. */
function whenReady(el: Element, cap: number): Promise<void> {
  return new Promise((resolve) => {
    const t0 = now();
    const poll = () => (!el.isConnected || !el.querySelector("[data-fx-pending]") || now() - t0 >= cap ? resolve() : void requestAnimationFrame(poll));
    poll();
  });
}

/** Plays the page (or first-load) sweep on `el` once its content is ready; skipped when a list took the moment, or a dialog is open. */
export function playPage(el: HTMLElement, first: boolean, extra?: SweepOptions): Promise<void> {
  if (!effectsFor("pages") || prefersReducedMotion()) return Promise.resolve();
  const since = state.navAt;
  state.pending = true;
  const ready = whenReady(el, READY_CAP_MS).then(() => {
    if (blockedByDialog(el) || (!first && state.listAt >= since)) cancelSweep(el);
  });
  return sweep(el, { ...(first ? firstSweepOptions(extra) : pageSweepOptions(extra)), hold: ready, hideWhileHeld: true, clipBox: contentBox(el.closest("main") ?? el) }).finally(() => {
    state.pending = false;
    state.endedAt = now();
  });
}

/**
 * Plays the page sweep on the element behind `ref` once per pathname change (not on search-only changes, so switching a tab
 * does not sweep) and once when it first mounts (the first-load sweep). `pathname` comes from the router.
 */
export function usePageTransition(ref: RefObject<HTMLElement | null>, pathname: string, enabled = true) {
  const first = useRef(true);
  const prev = useRef(pathname);
  useLayoutEffect(() => {
    const isFirst = first.current;
    first.current = false;
    const changed = prev.current !== pathname;
    prev.current = pathname;
    if (!enabled || (!isFirst && !changed) || !ref.current) return;
    // no cancel on cleanup: StrictMode runs this twice at mount, and a sweep ends by itself when its element goes
    void playPage(ref.current, isFirst);
  }, [pathname, enabled, ref]);
}

// --- lists ---

/**
 * The entrance of a screen's main list on its first data in this visit: `entrance(i)` is the style of row i (a fade with a 4 px
 * rise, 18 ms per row, the first 12 rows), undefined for the rest and for ever after. `ready` turns true when the first data is
 * in. Refetches, polling and filters do not play it again (decided once per mount, and over after a moment), and it does not
 * play when the page sweep is about to (or just did), under reduced motion or with the effects off.
 */
export function useListEntrance(ready: boolean): (i: number) => CSSProperties | undefined {
  const [mode, setMode] = useState<"wait" | "play" | "done">("wait");
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
  return (i) => (mode === "play" && i < 12 ? { animation: `mg-row-in 340ms var(--ease-out-soft) ${i * 18}ms both` } : undefined);
}
