import { useSyncExternalStore } from "react";
import { readPref, writePref } from "./storage";

// "Reveal animation" (Settings -> Interface): the motion the boards are drawn with - the wave from a pressed control, the wash of
// what has just been created, the numbers that settle, the assistant's waves. One switch, on by default, turned off per device;
// never on the Cloudflare edition (the shell says so with setEffectsAllowed). `prefers-reduced-motion` is honoured on top of it
// (see prefersReducedMotion below, and the media query of index.css that stills every animation).
const listeners = new Set<() => void>();
let chosen = readPref("fx") !== "off";
let allowed = true;

export const effectsEnabled = () => chosen && allowed;

/** `remember: false` is for the dev kit's scenes: the address decides for this page only, the device's own choice stays. */
export function setEffectsEnabled(on: boolean, remember = true) {
  chosen = on;
  if (remember) writePref("fx", on ? null : "off");
  listeners.forEach((l) => l());
}

/** The edition decides: the Cloudflare edition has no reveal animation at all. */
export function setEffectsAllowed(on: boolean) {
  if (allowed === on) return;
  allowed = on;
  listeners.forEach((l) => l());
}

function subscribe(cb: () => void) {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

export function useEffectsEnabled(): boolean {
  return useSyncExternalStore(subscribe, effectsEnabled, () => false);
}

/** The system's "reduce motion" switch. */
export function prefersReducedMotion(): boolean {
  try {
    return typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
  } catch {
    return false;
  }
}
