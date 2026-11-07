import { NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { relayState, type RelayMsg } from "./relay";
import type { RmState, Say } from "./route-map";

// The fleet as one map (the Nodes page's Map view): People on the left, the nodes in columns by role, the exits on the
// right; a wire per way people reach a node, per pair of nodes a relay joins, and per node to its exits. Pure: the view
// draws the cards and wires and words every `Say`; the calm visual language is the route map's.

export type FmStatus = "online" | "blip" | "offline" | "pending" | "updating";
/** entry: only passes traffic on; mixed: serves and passes on; serve: serves (and receives relays); idle: nothing deployed. */
export type FmRole = "entry" | "mixed" | "serve" | "idle";
export type FmVerdict = "ok" | "partial" | "blocked" | "unknown";

export type FleetNodeIn = {
  id: string;
  name: string;
  countryCode: string;
  location: string;
  status: NodeStatus;
  /** Plugin ids deployed on it. */
  protocols: readonly string[];
  /** People online now. */
  online: number;
  /** CPU % when it has fresh metrics. */
  cpu: number | null;
  warp: WarpState;
};

export type FleetInput = {
  nodes: readonly FleetNodeIn[];
  /** Every relay of the fleet. */
  relays: readonly RelayMsg[];
  /** What people of the audience country see of a node, by node id. */
  verdicts: ReadonlyMap<string, FmVerdict>;
  country: string;
};

export type FmNode = {
  /** The card id: "node:<id>". */
  id: string;
  nodeId: string;
  name: string;
  countryCode: string;
  location: string;
  status: FmStatus;
  role: FmRole;
  protocols: string[];
  online: number;
  load: number | null;
  /** The way people reach it: the country's verdict. */
  verdict: FmVerdict;
  /** Relays through it / to it. */
  relaysOut: number;
  relaysIn: number;
  /** The column: 1 entry only, 2 mixed, 3 the rest. */
  layer: 1 | 2 | 3;
};

export type FmWire = {
  id: string;
  from: string;
  to: string;
  kind: "people" | "relay" | "exit";
  /** plain: plumbing in the neutral colour (a node to the internet), no flow. */
  style: "flow" | "partial" | "muted" | "fail" | "plain";
  count: number;
  label?: Say;
  /** The relays of a relay wire (the drawer lists them). */
  relays?: RelayMsg[];
  state?: RmState;
};

export type FleetMap = {
  people: { country: string };
  nodes: FmNode[];
  exits: ("internet" | "warp")[];
  wires: FmWire[];
};

const statusOf = (s: NodeStatus): FmStatus =>
  s === NodeStatus.ONLINE || s === NodeStatus.NO_TRAFFIC ? "online" : s === NodeStatus.BLIP ? "blip" : s === NodeStatus.PENDING ? "pending" : s === NodeStatus.UPDATING ? "updating" : "offline";

const pairState = (rs: readonly RelayMsg[]): RmState => {
  const s = rs.map(relayState);
  if (s.includes("applying")) return "checking";
  const down = s.filter((x) => x === "failed").length;
  if (down > 0) return down === s.length ? "bad" : "partial";
  return s.includes("on") ? "ok" : "off";
};

const styleOf = (s: RmState): FmWire["style"] => (s === "ok" ? "flow" : s === "partial" || s === "checking" ? "partial" : s === "bad" ? "fail" : "muted");

export function fleetMap(g: FleetInput): FleetMap {
  const live = g.nodes.filter((n) => n.status !== NodeStatus.RETIRED);
  const ids = new Set(live.map((n) => n.id));
  const relays = g.relays.filter((r) => ids.has(r.viaNodeId) && ids.has(r.exitNodeId));
  const out = new Map<string, number>();
  const into = new Map<string, number>();
  for (const r of relays) {
    out.set(r.viaNodeId, (out.get(r.viaNodeId) ?? 0) + 1);
    into.set(r.exitNodeId, (into.get(r.exitNodeId) ?? 0) + 1);
  }
  const nodes: FmNode[] = live.map((n) => {
    const entry = (out.get(n.id) ?? 0) > 0;
    const serves = n.protocols.length > 0;
    const role: FmRole = entry && !serves ? "entry" : entry ? "mixed" : serves ? "serve" : "idle";
    return {
      id: `node:${n.id}`,
      nodeId: n.id,
      name: n.name,
      countryCode: n.countryCode,
      location: n.location,
      status: statusOf(n.status),
      role,
      protocols: [...n.protocols],
      online: n.online,
      load: n.cpu === null ? null : Math.round(n.cpu),
      verdict: g.verdicts.get(n.id) ?? "unknown",
      relaysOut: out.get(n.id) ?? 0,
      relaysIn: into.get(n.id) ?? 0,
      layer: role === "entry" ? 1 : role === "mixed" ? 2 : 3,
    };
  });

  const wires: FmWire[] = [];
  const warpOn = (id: string) => {
    const w = live.find((n) => n.id === id)?.warp;
    return w !== undefined && w !== WarpState.NOT_CONFIGURED && w !== WarpState.UNSPECIFIED;
  };
  // people to a node: the way the country sees it (a node behind relays is reached through them: its direct way is faint)
  for (const n of nodes) {
    if (n.role === "idle") continue;
    const up = n.status === "online";
    const style: FmWire["style"] = !up ? "muted" : n.verdict === "ok" ? "flow" : n.verdict === "partial" ? "partial" : "muted";
    wires.push({ id: `people:${n.nodeId}`, from: "people", to: n.id, kind: "people", style, count: 1, state: n.verdict === "ok" ? "ok" : n.verdict === "partial" ? "partial" : n.verdict === "blocked" ? "blocked" : "off" });
  }
  // a wire per pair a relay joins
  const pairs = new Map<string, RelayMsg[]>();
  for (const r of relays) pairs.set(`${r.viaNodeId}>${r.exitNodeId}`, [...(pairs.get(`${r.viaNodeId}>${r.exitNodeId}`) ?? []), r]);
  for (const [key, rs] of pairs) {
    const [a, b] = key.split(">") as [string, string];
    const state = pairState(rs);
    wires.push({ id: `relay:${a}:${b}`, from: `node:${a}`, to: `node:${b}`, kind: "relay", style: styleOf(state), count: rs.length, label: { key: "fm.wire.relays", n: rs.length }, relays: rs, state });
  }
  // the exits: the nodes that serve, to the internet; to WARP when the node has it
  const exits: FleetMap["exits"] = [];
  const serving = nodes.filter((n) => n.role === "serve" || n.role === "mixed");
  if (serving.length > 0) exits.push("internet");
  if (serving.some((n) => warpOn(n.nodeId))) exits.push("warp");
  for (const n of serving) {
    wires.push({ id: `exit:${n.nodeId}:internet`, from: n.id, to: "exit:internet", kind: "exit", style: n.status === "online" ? "plain" : "muted", count: 1 });
    if (warpOn(n.nodeId)) wires.push({ id: `exit:${n.nodeId}:warp`, from: n.id, to: "exit:warp", kind: "exit", style: n.status === "online" ? "plain" : "muted", count: 1 });
  }
  return { people: { country: g.country }, nodes, exits, wires };
}

/** Why a relay may not be drawn from `entry` to `exit` (before the dialog's own per-server reasons), or undefined. */
export function relayPairRefusal(entryId: string, exitId: string, nodes: readonly FleetNodeIn[]): "same_node" | "retired" | "no_servers" | undefined {
  if (entryId === exitId) return "same_node";
  const a = nodes.find((n) => n.id === entryId);
  const b = nodes.find((n) => n.id === exitId);
  if (!a || !b || a.status === NodeStatus.RETIRED || b.status === NodeStatus.RETIRED) return "retired";
  if (b.protocols.length === 0) return "no_servers";
  return undefined;
}

/** The nodes whose name starts with, then contains, the query (a search box that focuses one). */
export function findNodes(nodes: readonly { name: string; id: string }[], q: string): string[] {
  const s = q.trim().toLowerCase();
  if (!s) return [];
  const starts = nodes.filter((n) => n.name.toLowerCase().startsWith(s));
  const has = nodes.filter((n) => !n.name.toLowerCase().startsWith(s) && n.name.toLowerCase().includes(s));
  return [...starts, ...has].map((n) => n.id);
}
