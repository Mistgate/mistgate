import { useSyncExternalStore } from "react";
import { readPref, writePref } from "./storage";

export type Theme = "light" | "dark";

// Dark is the default. The choice is per device; data-theme is always set on <html>
// and the tokens in index.css switch on it.
const listeners = new Set<() => void>();
let current: Theme = readPref("theme") === "light" ? "light" : "dark";

function apply() {
  document.documentElement.dataset.theme = current;
}
apply();

let thxTimer = 0;

export function setTheme(theme: Theme) {
  current = theme;
  writePref("theme", theme);
  // the change is instant: for 0.9 s no transition runs (screens/shell.css), as on the redesign board
  const root = document.documentElement;
  root.classList.add("mg-thx");
  clearTimeout(thxTimer);
  thxTimer = window.setTimeout(() => root.classList.remove("mg-thx"), 900);
  apply();
  listeners.forEach((l) => l());
}

/** The theme in use right now (outside a render). */
export const themeNow = (): Theme => current;

export const toggleTheme = () => setTheme(current === "dark" ? "light" : "dark");

function subscribe(cb: () => void) {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

export function useTheme(): Theme {
  return useSyncExternalStore(subscribe, () => current);
}
