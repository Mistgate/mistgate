import { useLayoutEffect, useSyncExternalStore } from "react";
import { readPref, writePref } from "./storage";

// The menu column of the shell: 264px open, 72px folded (a rail of icons), none on a phone (the dock and the burger take over).
// Below the `rail` breakpoint (1000px) it folds by itself; above it the person folds it with the button at the top of the
// menu, and the choice is remembered in this browser (localStorage, best-effort like every preference: lib/storage.ts).

export type MenuMode = "full" | "rail" | "none";
export const menuWidth: Record<MenuMode, string> = { full: "264px", rail: "72px", none: "0px" };

// the same two steps as the `md` and `rail` breakpoints of index.css (45rem, 62.5rem)
const phoneQuery = "(min-width: 45rem)";
const wideQuery = "(min-width: 62.5rem)";

const key = "menu-rail";
let folded = readPref(key) === "1";
const listeners = new Set<() => void>();

function subscribe(cb: () => void) {
  listeners.add(cb);
  const media = [phoneQuery, wideQuery].map((q) => (typeof matchMedia === "function" ? matchMedia(q) : null));
  media.forEach((m) => m?.addEventListener("change", cb));
  return () => {
    listeners.delete(cb);
    media.forEach((m) => m?.removeEventListener("change", cb));
  };
}

// no matchMedia (a test environment): a desktop
const matches = (q: string) => (typeof matchMedia === "function" ? matchMedia(q).matches : true);
const read = (): MenuMode => (!matches(phoneQuery) ? "none" : matches(wideQuery) && !folded ? "full" : "rail");

/** The person's own choice (the button at the top of the menu); on a narrow window the menu is a rail whatever it says. */
export function setMenuFolded(value: boolean) {
  folded = value;
  writePref(key, value ? "1" : null);
  listeners.forEach((l) => l());
}

export function useMenuFolded(): boolean {
  return useSyncExternalStore(subscribe, () => folded);
}

/** How the menu column looks right now: it follows the window and the person's choice. */
export function useMenuMode(): MenuMode {
  return useSyncExternalStore(subscribe, read);
}

/** Writes the column's width to --menu-w on <html>, where the toast and the bulk bar read it; call it once per page frame. */
export function useMenuWidth(): MenuMode {
  const mode = useMenuMode();
  useLayoutEffect(() => {
    document.documentElement.style.setProperty("--menu-w", menuWidth[mode]);
    return () => void document.documentElement.style.removeProperty("--menu-w");
  }, [mode]);
  return mode;
}
