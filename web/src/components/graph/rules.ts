import type { ConnectCheck, GraphEdge, GraphNode, GraphPort, PortRef, PortType } from "./types";

/** Same type, or either side "any". */
export const typesCompatible = (a: PortType, b: PortType) => a === b || a === "any" || b === "any";

export type Refusal = "unknown" | "self" | "side" | "type" | "duplicate" | "occupied" | "disabled" | "custom";
export type Verdict = { ok: true } | { ok: false; code: Refusal; message?: string };

export function findPort(nodes: GraphNode[], ref: PortRef): GraphPort | undefined {
  return nodes.find((n) => n.id === ref.node)?.ports.find((p) => p.id === ref.port);
}

const isMulti = (p: GraphPort) => p.multi ?? p.side === "out";

/** Puts a pair of port refs in wire order (out, in), or null when they are on the same side or do not exist. */
export function orient(nodes: GraphNode[], a: PortRef, b: PortRef): { from: PortRef; to: PortRef } | null {
  const pa = findPort(nodes, a);
  const pb = findPort(nodes, b);
  if (!pa || !pb || pa.side === pb.side) return null;
  return pa.side === "out" ? { from: a, to: b } : { from: b, to: a };
}

/**
 * Can `from` (an out port) be wired to `to` (an in port)? Checks, in order: both exist and face each other, no
 * self-loop, types, disabled ports, a duplicate wire, a single port already taken, then the caller's canConnect.
 */
export function checkConnection(
  nodes: GraphNode[],
  edges: Pick<GraphEdge, "from" | "to">[],
  from: PortRef,
  to: PortRef,
  canConnect?: (from: PortRef, to: PortRef) => ConnectCheck,
): Verdict {
  const pf = findPort(nodes, from);
  const pt = findPort(nodes, to);
  if (!pf || !pt) return { ok: false, code: "unknown" };
  if (pf.side !== "out" || pt.side !== "in") return { ok: false, code: "side" };
  if (from.node === to.node) return { ok: false, code: "self" };
  if (!typesCompatible(pf.type, pt.type)) return { ok: false, code: "type" };
  if (pf.disabled || pt.disabled) return { ok: false, code: "disabled" };
  if (edges.some((e) => e.from.node === from.node && e.from.port === from.port && e.to.node === to.node && e.to.port === to.port)) {
    return { ok: false, code: "duplicate" };
  }
  if (!isMulti(pt) && edges.some((e) => e.to.node === to.node && e.to.port === to.port)) return { ok: false, code: "occupied" };
  if (!isMulti(pf) && edges.some((e) => e.from.node === from.node && e.from.port === from.port)) return { ok: false, code: "occupied" };
  if (canConnect) {
    const r = canConnect(from, to);
    if (r !== true) return { ok: false, code: "custom", message: r };
  }
  return { ok: true };
}

/** Every port that could take a wire from `source` (the ports of the other side are tested). */
export function validTargets(
  nodes: GraphNode[],
  edges: Pick<GraphEdge, "from" | "to">[],
  source: PortRef,
  canConnect?: (from: PortRef, to: PortRef) => ConnectCheck,
): PortRef[] {
  const sp = findPort(nodes, source);
  if (!sp) return [];
  const out: PortRef[] = [];
  for (const n of nodes) {
    for (const p of n.ports) {
      if (p.side === sp.side) continue;
      const ref = { node: n.id, port: p.id };
      const v = sp.side === "out" ? checkConnection(nodes, edges, source, ref, canConnect) : checkConnection(nodes, edges, ref, source, canConnect);
      if (v.ok) out.push(ref);
    }
  }
  return out;
}
