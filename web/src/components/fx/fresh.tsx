import { createElement, useLayoutEffect, useRef, type HTMLAttributes, type ReactNode } from "react";
import { isFresh, subscribeFresh, takeFresh } from "./registry";

/** A dialog that is open and does not hold `el` (a thing created behind a window is washed when the window is gone). */
export const blockedByDialog = (el: Element) => [...document.querySelectorAll('[role="dialog"], [role="alertdialog"]')].some((d) => !d.contains(el));

/** Resolves when no such dialog is open (or after `max` ms: the wash then plays behind it rather than never). */
function untilDialogsClose(el: Element, max: number): Promise<void> {
  return new Promise((resolve) => {
    const t0 = Date.now();
    const poll = () => (!el.isConnected || !blockedByDialog(el) || Date.now() - t0 > max ? resolve() : void setTimeout(poll, 120));
    poll();
  });
}

/** How long the wash lasts (the board's dr-fresh, 2.4 s; index.css has the keyframes) and how long the attribute stays on. */
const WASH_MS = 2500;

/**
 * Washes the element behind the returned ref when the item `kind`/`id` is marked fresh (markFresh in registry.ts): the first time
 * it shows up, or, if it is already on screen, the moment it is marked. The mark is taken, so this happens once. An element with
 * no box (the hidden twin of a list that has a table and a card layout) leaves the mark for its sibling. The wash is an attribute
 * that React does not own, so a re-render of the row never takes it away early.
 */
export function useFresh<T extends HTMLElement>(kind: string, id: string) {
  const ref = useRef<T>(null);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const wash = () => {
      el.setAttribute("data-mg-fresh", "");
      setTimeout(() => el.removeAttribute("data-mg-fresh"), WASH_MS);
    };
    const play = () => {
      if (!isFresh(kind, id)) return;
      const r = el.getBoundingClientRect();
      if (r.width < 1 || r.height < 1) return;
      if (!takeFresh(kind, id)) return;
      if (blockedByDialog(el)) void untilDialogsClose(el, 20_000).then(() => el.isConnected && wash());
      else wash();
    };
    play();
    // no cleanup that cancels: StrictMode runs this effect twice at mount, and a wash ends by itself
    return subscribeFresh((k, i) => k === kind && i === id && play());
  }, [kind, id]);
  return ref;
}

type Tag = "div" | "tr" | "li" | "section" | "article" | "span";
type FreshProps = Omit<HTMLAttributes<HTMLElement>, "children"> & {
  kind: string;
  id: string;
  /** The element it renders. Default "div". */
  as?: Tag;
  children?: ReactNode;
};

/**
 * The wrapper for a list item that may have just been created: it renders its own element (give it the item's classes so the
 * layout is the item's), and washes it once when `kind`/`id` was marked fresh. Not marked: it is a plain element.
 */
export function Fresh({ kind, id, as = "div", children, ...rest }: FreshProps) {
  const ref = useFresh<HTMLElement>(kind, id);
  return createElement(as, { ...rest, ref }, children);
}
