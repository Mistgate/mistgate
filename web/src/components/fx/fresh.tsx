import { createElement, useLayoutEffect, useRef, type HTMLAttributes, type ReactNode } from "react";
import { isFresh, subscribeFresh, takeFresh } from "./registry";
import { sweep, type SweepOptions } from "./sweep";

/** A dialog that is open and does not hold `el` (a thing created behind a window sweeps when the window is gone). */
const blockedByDialog = (el: Element) => [...document.querySelectorAll('[role="dialog"], [role="alertdialog"]')].some((d) => !d.contains(el));

/** Resolves when no such dialog is open (or after `max` ms: the sweep then plays behind it rather than never). */
function untilDialogsClose(el: Element, max: number): Promise<void> {
  return new Promise((resolve) => {
    const t0 = Date.now();
    const poll = () => (!el.isConnected || !blockedByDialog(el) || Date.now() - t0 > max ? resolve() : void setTimeout(poll, 120));
    poll();
  });
}

/** The stagger between items that appear together, ms per step, and how many steps count. */
const STAGGER_MS = 90;
const STAGGER_MAX = 8;

/**
 * Plays the sweep over the element behind the returned ref when the item `kind`/`id` is marked fresh (markFresh in registry.ts):
 * the first time it shows up, or, if it is already on screen, the moment it is marked. The mark is taken, so this happens once.
 * An element with no box (the hidden twin of a list that has a table and a card layout) leaves the mark for its sibling.
 */
export function useFresh<T extends HTMLElement>(kind: string, id: string, options?: SweepOptions) {
  const ref = useRef<T>(null);
  const opts = useRef(options);
  useLayoutEffect(() => {
    opts.current = options;
  });
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const play = (onMount: boolean) => {
      if (!isFresh(kind, id)) return;
      const r = el.getBoundingClientRect();
      if (r.width < 1 || r.height < 1) return;
      const taken = takeFresh(kind, id);
      if (!taken) return;
      const blocked = blockedByDialog(el);
      void sweep(el, {
        delay: Math.min(taken.order, STAGGER_MAX) * STAGGER_MS,
        ...opts.current,
        ...(blocked && { hold: untilDialogsClose(el, 20_000), hideWhileHeld: onMount }),
      });
    };
    play(true);
    // No cleanup that cancels: StrictMode runs this effect twice at mount, and a sweep ends by itself when its element leaves the page.
    return subscribeFresh((k, i) => k === kind && i === id && play(false));
  }, [kind, id]);
  return ref;
}

type Tag = "div" | "tr" | "li" | "section" | "article" | "span";
type FreshProps = Omit<HTMLAttributes<HTMLElement>, "children"> & {
  kind: string;
  id: string;
  /** The element it renders. Default "div". */
  as?: Tag;
  options?: SweepOptions;
  children?: ReactNode;
};

/**
 * The wrapper for a list item that may have just been created: it renders its own element (give it the item's classes so the
 * layout is the item's), and sweeps it in once when `kind`/`id` was marked fresh. Not marked: it is a plain element.
 */
export function Fresh({ kind, id, as = "div", options, children, ...rest }: FreshProps) {
  const ref = useFresh<HTMLElement>(kind, id, options);
  return createElement(as, { ...rest, ref }, children);
}
