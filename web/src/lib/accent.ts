import { useSyncExternalStore } from "react";
import { readPref, writePref } from "./storage";

// One accent at a time, chosen per device in Settings -> Interface: six presets or any #rrggbb.
// index.css derives every tint from --accent, so setting it on <html> is all the theming there is.
export const accentPresets = [
  { id: "mint", color: "#7dd3a0" },
  { id: "lavender", color: "#b8acf2" },
  { id: "sky", color: "#8ec8ea" },
  { id: "sand", color: "#dcc68c" },
  { id: "rose", color: "#e6a6b0" },
  { id: "sage", color: "#b0cc90" },
] as const;

export const defaultAccent = "#b8acf2";
export const isHex = (v: string) => /^#[0-9a-f]{6}$/i.test(v);

const listeners = new Set<() => void>();
const saved = readPref("accent");
let current = saved && isHex(saved) ? saved.toLowerCase() : defaultAccent;

function apply() {
  document.documentElement.style.setProperty("--accent", current);
}
apply();

export function setAccent(hex: string) {
  if (!isHex(hex)) return;
  current = hex.toLowerCase();
  own = true;
  writePref("accent", current);
  apply();
  listeners.forEach((l) => l());
}

// The owner's default for the whole installation, remembered even while this device has its own: "like the panel"
// (followInstanceAccent) goes back to it.
let instance = defaultAccent;
let own = !!(saved && isHex(saved));

/**
 * The instance owner's default accent. It only fills the gap: a device that picked its own accent
 * (Settings -> Interface) keeps it, and the default is never written to this device's preferences.
 */
export function applyInstanceAccent(hex: string) {
  if (!isHex(hex)) return;
  instance = hex.toLowerCase();
  if (!readPref("accent")) {
    current = instance;
    writePref("instance-accent", current); // only for the splash (public/splash.js); a device's own choice wins over it there too
    apply();
  }
  listeners.forEach((l) => l()); // the "like the panel" swatch shows the new default either way
}

function subscribe(cb: () => void) {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

export function useAccent(): string {
  return useSyncExternalStore(subscribe, () => current);
}

/** Forgets this device's own accent: it follows the panel's default again ("like the panel"). */
export function followInstanceAccent() {
  writePref("accent", null);
  own = false;
  current = instance;
  writePref("instance-accent", current); // for the splash, as applyInstanceAccent does
  apply();
  listeners.forEach((l) => l());
}

/** The panel's default accent, and whether this device picked one of its own instead (Settings -> Interface). */
export function useAccentChoice(): { own: boolean; instance: string } {
  const isOwn = useSyncExternalStore(subscribe, () => own);
  const inst = useSyncExternalStore(subscribe, () => instance);
  return { own: isOwn, instance: inst };
}
