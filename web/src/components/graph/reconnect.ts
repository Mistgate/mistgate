import { flattenRoute, nearestPoint, type Route } from "./geometry";
import { checkConnection, findPort } from "./rules";
import type { ConnectCheck, GraphEdge, GraphNode, Pos, PortRef } from "./types";

// Grabbing a wire by either end: a handle shows on the wire near a port, dragging it detaches that end (multi inputs
// included) and the connection UX takes over, then the end is dropped on a port, or on empty canvas (target null).

export type End = "from" | "to";
export const HANDLE_DIST = 18;
export const HANDLE_RADIUS = 12;

/** The point on the wire `dist` px (world units) from its end, along the wire: where the handle sits, clear of the port. */
export function handlePoint(route: Route, end: End, dist = HANDLE_DIST): Pos {
  const pts = flattenRoute(route, 32);
  const seq = end === "from" ? pts : [...pts].reverse();
  let walked = 0;
  for (let i = 1; i < seq.length; i++) {
    const a = seq[i - 1]!;
    const b = seq[i]!;
    const d = Math.hypot(b.x - a.x, b.y - a.y);
    if (walked + d >= dist) {
      const k = d === 0 ? 0 : (dist - walked) / d;
      return { x: a.x + (b.x - a.x) * k, y: a.y + (b.y - a.y) * k };
    }
    walked += d;
  }
  return seq[seq.length - 1]!;
}

export interface Handle extends Pos {
  edge: string;
  end: End;
}

/** The handle under the pointer, if any: the nearest within the radius. */
export function handleAt(p: Pos, handles: Handle[], radius = HANDLE_RADIUS): Handle | null {
  return nearestPoint(p, handles, radius);
}

export type DropDecision = { kind: "noop" } | { kind: "refuse"; reason: string } | { kind: "apply"; target: PortRef | null };

const same = (a: PortRef, b: PortRef) => a.node === b.node && a.port === b.port;

/** What dropping an end means: nothing (back where it was), a refusal with the reason, or the move (null: unhooked). */
export function decideDrop(original: PortRef, target: PortRef | null, verdict: ConnectCheck): DropDecision {
  if (target && same(original, target)) return { kind: "noop" };
  if (verdict !== true) return { kind: "refuse", reason: verdict };
  return { kind: "apply", target };
}

/**
 * The ports the moved end may land on: those of the side the end belongs to that `canReconnect` accepts (without it, the
 * ordinary connection rules with the other end fixed). The end's own port counts: dropping there changes nothing.
 */
export function grabTargets(
  nodes: GraphNode[],
  edges: Pick<GraphEdge, "id" | "from" | "to">[],
  edge: Pick<GraphEdge, "id" | "from" | "to">,
  end: End,
  canReconnect?: (edge: GraphEdge, end: End, target: PortRef | null) => ConnectCheck,
): PortRef[] {
  const side = end === "to" ? "in" : "out";
  const fixed = end === "to" ? edge.from : edge.to;
  const others = edges.filter((e) => e.id !== edge.id);
  const out: PortRef[] = [];
  for (const n of nodes)
    for (const p of n.ports) {
      if (p.side !== side || p.disabled) continue;
      const ref = { node: n.id, port: p.id };
      const ok = canReconnect
        ? canReconnect(edge as GraphEdge, end, ref) === true
        : findPort(nodes, fixed) !== undefined && (end === "to" ? checkConnection(nodes, others, fixed, ref) : checkConnection(nodes, others, ref, fixed)).ok;
      if (ok) out.push(ref);
    }
  return out;
}

// ---- the state machine of one grab

export type GrabState =
  | { phase: "idle" }
  | { phase: "hover"; handle: Handle }
  | { phase: "drag"; edge: string; end: End; snap: PortRef | null; refused: string | null };

export type GrabAction =
  | { type: "hover"; handle: Handle | null }
  | { type: "press" }
  | { type: "move"; snap: PortRef | null; refused: string | null }
  | { type: "release" }
  | { type: "cancel" };

export type GrabEffect = { type: "drop"; edge: string; end: End; target: PortRef | null; refused: string | null } | { type: "springBack" } | null;

export const grabIdle: GrabState = { phase: "idle" };

export function grabStep(state: GrabState, action: GrabAction): { state: GrabState; effect: GrabEffect } {
  switch (action.type) {
    case "hover":
      // while dragging the pointer is busy: hover only shows handles when nothing is grabbed
      if (state.phase === "drag") return { state, effect: null };
      return { state: action.handle ? { phase: "hover", handle: action.handle } : grabIdle, effect: null };
    case "press":
      return state.phase === "hover"
        ? { state: { phase: "drag", edge: state.handle.edge, end: state.handle.end, snap: null, refused: null }, effect: null }
        : { state, effect: null };
    case "move":
      return state.phase === "drag" ? { state: { ...state, snap: action.snap, refused: action.refused }, effect: null } : { state, effect: null };
    case "release":
      return state.phase === "drag"
        ? { state: grabIdle, effect: { type: "drop", edge: state.edge, end: state.end, target: state.snap, refused: state.refused } }
        : { state, effect: null };
    case "cancel":
      return state.phase === "drag" ? { state: grabIdle, effect: { type: "springBack" } } : { state: grabIdle, effect: null };
  }
}
