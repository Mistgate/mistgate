import type { GraphEdge } from "./types";

/** What a focus keeps bright: the cards and the wires; everything else is dimmed. */
export interface FocusSet {
  nodes: Set<string>;
  edges: Set<string>;
}

/**
 * The paths through the focused cards: everything upstream of them (what reaches them), everything downstream (where
 * they lead), and the wires along those paths. On the fleet map a focused exit node keeps the people column and the entry
 * nodes that relay to it, and its own exits; a wire from the people to an unrelated node is dimmed even though both ends
 * are on some path, because that wire is not on a path through the focus.
 *
 * Wires run from an out port to an in port, so "upstream" follows them backwards. A cycle is fine: every node is visited
 * once each way. Empty `focus` is no focus (null).
 */
export function pathsThrough(focus: readonly string[], edges: readonly Pick<GraphEdge, "id" | "from" | "to">[]): FocusSet | null {
  if (focus.length === 0) return null;
  const outs = new Map<string, Pick<GraphEdge, "id" | "from" | "to">[]>();
  const ins = new Map<string, Pick<GraphEdge, "id" | "from" | "to">[]>();
  for (const e of edges) {
    (outs.get(e.from.node) ?? outs.set(e.from.node, []).get(e.from.node)!).push(e);
    (ins.get(e.to.node) ?? ins.set(e.to.node, []).get(e.to.node)!).push(e);
  }
  const walk = (start: readonly string[], next: (n: string) => string[]) => {
    const seen = new Set<string>(start);
    const queue = [...start];
    while (queue.length) {
      for (const m of next(queue.pop()!)) {
        if (!seen.has(m)) {
          seen.add(m);
          queue.push(m);
        }
      }
    }
    return seen;
  };
  // `up` and `down` include the focus itself
  const up = walk(focus, (n) => (ins.get(n) ?? []).map((e) => e.from.node));
  const down = walk(focus, (n) => (outs.get(n) ?? []).map((e) => e.to.node));
  const nodes = new Set<string>([...up, ...down]);
  const kept = new Set<string>();
  for (const e of edges) {
    // a wire between two upstream cards leads towards the focus; one between two downstream cards leads away from it
    if ((up.has(e.from.node) && up.has(e.to.node)) || (down.has(e.from.node) && down.has(e.to.node))) kept.add(e.id);
  }
  return { nodes, edges: kept };
}

/** The focus of one wire: the wire and its two ends. */
export function wireFocus(edge: Pick<GraphEdge, "id" | "from" | "to">): FocusSet {
  return { nodes: new Set([edge.from.node, edge.to.node]), edges: new Set([edge.id]) };
}
