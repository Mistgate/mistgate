import type { PortRef } from "./types";
import { portKey } from "./types";

/** The keyboard wiring state machine: Enter on a port starts, Tab/arrows cycle through the valid targets, Enter connects, Esc cancels. */
export type WiringState = { phase: "idle" } | { phase: "picking"; source: PortRef; candidates: PortRef[]; index: number };

export type WiringAction =
  | { type: "start"; source: PortRef; candidates: PortRef[] }
  | { type: "next" }
  | { type: "prev" }
  | { type: "confirm" }
  | { type: "cancel" };

export type WiringEffect = { type: "connect"; source: PortRef; target: PortRef } | { type: "empty" } | null;

export const idle: WiringState = { phase: "idle" };

export function wiringStep(state: WiringState, action: WiringAction): { state: WiringState; effect: WiringEffect } {
  if (action.type === "start") {
    if (!action.candidates.length) return { state: idle, effect: { type: "empty" } };
    return { state: { phase: "picking", source: action.source, candidates: action.candidates, index: 0 }, effect: null };
  }
  if (state.phase !== "picking") return { state, effect: null };
  const n = state.candidates.length;
  switch (action.type) {
    case "next":
      return { state: { ...state, index: (state.index + 1) % n }, effect: null };
    case "prev":
      return { state: { ...state, index: (state.index - 1 + n) % n }, effect: null };
    case "confirm":
      return { state: idle, effect: { type: "connect", source: state.source, target: state.candidates[state.index]! } };
    case "cancel":
      return { state: idle, effect: null };
  }
}

export const currentTarget = (s: WiringState): PortRef | null => (s.phase === "picking" ? (s.candidates[s.index] ?? null) : null);
export const currentTargetKey = (s: WiringState) => {
  const t = currentTarget(s);
  return t ? portKey(t) : null;
};
