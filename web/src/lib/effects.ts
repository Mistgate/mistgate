import { useSyncExternalStore } from "react";
import { readPref, writePref } from "./storage";

// "Interface effects" (Settings -> Interface): the sweep that plays when something is created. On by default; the choice is
// per device. `prefers-reduced-motion` is honoured on top of it (the sweep then is a quick fade, see components/fx/sweep.ts).
const listeners = new Set<() => void>();
let enabled = readPref("fx") !== "off";

export const effectsEnabled = () => enabled;

export function setEffectsEnabled(on: boolean) {
  enabled = on;
  writePref("fx", on ? null : "off");
  listeners.forEach((l) => l());
}

function subscribe(cb: () => void) {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

export function useEffectsEnabled(): boolean {
  return useSyncExternalStore(subscribe, effectsEnabled, () => true);
}

/** The system's "reduce motion" switch. */
export function prefersReducedMotion(): boolean {
  try {
    return typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
  } catch {
    return false;
  }
}
