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

// The two moments the effect plays at, each its own switch under the main one (Settings -> Interface): "create" is a new thing
// appearing in its list, "pages" is a page opening (the route sweep, the first sweep after sign-in, the list entrance).
export type FxKind = "create" | "pages";
const kinds: Record<FxKind, boolean> = { create: readPref("fx-create") !== "off", pages: readPref("fx-pages") !== "off" };

/** Whether the effect plays for this moment: the main switch and the moment's own. */
export const effectsFor = (kind: FxKind) => enabled && kinds[kind];

export function setEffectKind(kind: FxKind, on: boolean) {
  kinds[kind] = on;
  writePref(`fx-${kind}`, on ? null : "off");
  listeners.forEach((l) => l());
}

export function useEffectKind(kind: FxKind): boolean {
  return useSyncExternalStore(subscribe, () => kinds[kind], () => true);
}

/** The system's "reduce motion" switch. */
export function prefersReducedMotion(): boolean {
  try {
    return typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
  } catch {
    return false;
  }
}
