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

export function setTheme(theme: Theme) {
  current = theme;
  writePref("theme", theme);
  apply();
  listeners.forEach((l) => l());
}

export const toggleTheme = () => setTheme(current === "dark" ? "light" : "dark");

function subscribe(cb: () => void) {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

export function useTheme(): Theme {
  return useSyncExternalStore(subscribe, () => current);
}
