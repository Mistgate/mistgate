import { useSyncExternalStore } from "react";
import { readPref, writePref } from "./storage";

// "Reveal animation" (Settings -> Interface): the motion of the boards - the sweep when something is created, the entrance of
// pages, the assistant's waves. On by default (the interface is designed with it), turned off per device; never on the
// Cloudflare edition (the shell says so with setEffectsAllowed). `prefers-reduced-motion` is honoured on top of it (the sweep
// then is a quick fade, see components/fx/sweep.ts).
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

// The moments the effects play at, each its own switch under the main one (Settings -> Interface): "create" is a new thing
// appearing in its list (and the halo from the button that made it), "save" the halo when something is saved or the
// assistant has answered, "pages" a page opening (the first sweep after sign-in, the list entrance).
export type FxKind = "create" | "save" | "pages";
const kinds: Record<FxKind, boolean> = { create: readPref("fx-create") !== "off", save: readPref("fx-save") !== "off", pages: readPref("fx-pages") !== "off" };

/** Whether the effect plays for this moment: the main switch and the moment's own. */
export const effectsFor = (kind: FxKind) => effectsEnabled() && kinds[kind];

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
