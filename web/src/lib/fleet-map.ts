import type { StatusKind } from "@/components/ui/status";
import { NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import type { CountryCheck } from "./health";
import { relayState, type RelayMsg } from "./relay";
import { activeOf } from "./relay-bridge";
import type { RmState, Say } from "./route-map";
import { hintOf, relaysState, type Seen } from "./ways";

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

// ---------------------------------------------------------------------------------------------------
// The ways out of the country, node by node: the "From Russia" column of the Nodes list and the Ways view (the exit x entry
// table). Pure: the screens word the result. The verdicts are the node page's (hintOf for what the country sees, relaysState for a
// pair of relays, the entry judged against the networks the exit is dark on, as lanesOf does); no screen has a rule of its own.

/** The look of a way: works, partly, does not, nothing is known. */
export type WayTone = "ok" | "part" | "bad" | "none";

/** What the audience country sees of one node at its own address. */
export type Direct = {
  tone: WayTone;
  /** The networks that do not see it. */
  dark: string[];
  atUnix: number;
};

export function directOf(c?: CountryCheck): Direct {
  const h = hintOf(c);
  return { tone: h?.verdict === "ok" ? "ok" : h?.verdict === "partial" ? "part" : h?.verdict === "blocked" ? "bad" : "none", dark: h?.dark ?? [], atUnix: c?.atUnix ?? 0 };
}

/** All relays of one exit through one entry: the unit the owner thinks in ("de1 through msk1"). */
export type WayPair = {
  exitId: string;
  entryId: string;
  entryName: string;
  relays: RelayMsg[];
  /** Relays that run, of those that are meant to (the switched-on ones; every one when none is). */
  ok: number;
  n: number;
  down: number;
  state: StatusKind;
  /** The switched-on relays that stand in the bridge now. */
  bridge: number;
  /** Do people see the entry on the networks where the exit is dark (all of them, some, none, unknown), and the networks where they do not. */
  seen: Seen;
  dark: string[];
  /** The entry carries nobody at all: its node does not answer, or the country does not see it. */
  dead: "" | "offline" | "hidden";
};

export type WayKind =
  /** Opens directly (perhaps through entries too). */
  | "direct"
  /** Does not open directly (or not everywhere), an entry carries every network. */
  | "bypass"
  /** Some relays through an entry fail, or the entry is not seen on every network. */
  | "partly"
  /** Opens directly on some networks, and no entry helps. */
  | "directPart"
  | "noway"
  /** Closed, and the only ways are through entries that carry nobody. */
  | "deadEntry"
  | "nodata"
  /** An entry: what the country sees of it and what it passes. */
  | "entry"
  /** Nothing deployed here: no way to speak of. */
  | "idle";

export type NodeWays = {
  nodeId: string;
  name: string;
  role: FmRole;
  tone: WayTone;
  kind: WayKind;
  direct: Direct;
  /** An exit's pairs, the working ones first. */
  pairs: WayPair[];
  /** "Get around…" is offered: closed, no relay through any entry, nothing turning on. */
  noWay: boolean;
  /** Closed and the pairs are all through dead entries: the first and why. */
  deadBy?: { entry: string; why: "offline" | "hidden" };
  /** An entry's (a node with no servers that takes relays, or that could): `passes` exits go through it, `broken` of them with a relay down. */
  entry?: { offline: boolean; hidden: boolean; oldAgent: boolean; passes: number; broken: number };
  /** Days the node's measurement is old, when over a day; 0 when it is fresh or there is none. */
  staleDays: number;
  /** A relay of it is turning on. */
  busy: boolean;
};

export type WaysIn = {
  nodes: readonly FleetNodeIn[];
  /** Every relay of the fleet. undefined: a helper (RelayService is the owner's): only what the country sees of each node. */
  relays: readonly RelayMsg[] | undefined;
  /** What the country saw of each node. */
  checks: ReadonlyMap<string, CountryCheck>;
  /** The audience country: an idle node inside it can take relays. */
  country: string;
  nowS: number;
};

export type FleetWays = {
  byNode: ReadonlyMap<string, NodeWays>;
  /** The nodes that pass relays on (or could), in the panel's order. */
  entries: NodeWays[];
  /** The nodes people use: one row each. */
  exits: NodeWays[];
  relayN: number;
  downN: number;
  reachN: number;
  noWayN: number;
  partN: number;
  bridgeN: number;
  /** The country measured nothing at all (the integration is off): no exit has a verdict. */
  unmeasured: boolean;
};

const dayS = 86400;
const rankOf: Record<WayTone, number> = { bad: 0, part: 1, none: 2, ok: 3 };

/** The rows of the Ways view: no way first, then partly, no data, then the rest; by name inside. */
export const sortExits = (exits: readonly NodeWays[]): NodeWays[] => [...exits].sort((a, b) => rankOf[a.tone] - rankOf[b.tone] || a.name.localeCompare(b.name, undefined, { numeric: true }));

export type WayFilter = "all" | "noway" | "part" | "bridge";

/** The exits a filter of the Ways view keeps. */
export const filterExits = (exits: readonly NodeWays[], f: WayFilter): NodeWays[] =>
  exits.filter((x) => (f === "all" ? true : f === "noway" ? x.tone === "bad" : f === "part" ? x.tone === "part" : x.pairs.some((p) => p.bridge > 0)));

export function fleetWays(w: WaysIn): FleetWays {
  const live = w.nodes.filter((n) => n.status !== NodeStatus.RETIRED);
  const ids = new Set(live.map((n) => n.id));
  const relays = (w.relays ?? []).filter((r) => ids.has(r.viaNodeId) && ids.has(r.exitNodeId));
  const out = new Map<string, number>();
  for (const r of relays) out.set(r.viaNodeId, (out.get(r.viaNodeId) ?? 0) + 1);
  const unmeasured = w.checks.size === 0;
  const direct = (id: string) => directOf(w.checks.get(id));
  const staleOf = (id: string) => {
    const at = w.checks.get(id)?.atUnix ?? 0;
    return at > 0 && w.nowS - at >= dayS ? Math.floor((w.nowS - at) / dayS) : 0;
  };
  const roleOf = (n: FleetNodeIn): FmRole => {
    const entry = (out.get(n.id) ?? 0) > 0;
    const serves = n.protocols.length > 0;
    return entry && !serves ? "entry" : entry ? "mixed" : serves ? "serve" : "idle";
  };
  const down = (id: string) => live.find((n) => n.id === id)?.status === NodeStatus.DOWN;
  const deadOf = (entryId: string): WayPair["dead"] => (down(entryId) ? "offline" : direct(entryId).tone === "bad" ? "hidden" : "");

  const pairsOf = (exit: FleetNodeIn, own: Direct): WayPair[] => {
    const groups = new Map<string, RelayMsg[]>();
    for (const r of relays) if (r.exitNodeId === exit.id) groups.set(r.viaNodeId, [...(groups.get(r.viaNodeId) ?? []), r]);
    const cut = (own.tone === "part" || own.tone === "bad") && own.dark.length > 0;
    return [...groups].map(([entryId, rs]): WayPair => {
      const entry = direct(entryId);
      const on = rs.filter((r) => r.enabled);
      const meant = on.length > 0 ? on : rs;
      // the entry against the networks that see the exit dark (lanesOf), else the entry's own verdict
      let seen: Seen = entry.tone === "ok" ? "all" : entry.tone === "part" ? "part" : entry.tone === "bad" ? "none" : "unknown";
      let dark = entry.dark;
      if (cut && entry.tone !== "none") {
        dark = own.dark.filter((x) => entry.dark.includes(x));
        seen = dark.length > 0 ? "part" : "all";
      }
      return {
        exitId: exit.id,
        entryId,
        entryName: rs[0]!.viaNodeName,
        relays: rs,
        ok: meant.filter((r) => relayState(r) === "on").length,
        n: meant.length,
        down: rs.filter((r) => relayState(r) === "failed").length,
        state: relaysState(rs),
        bridge: on.filter((r) => activeOf(r) === "reverse").length,
        seen,
        dark,
        dead: deadOf(entryId),
      };
    });
  };

  const byNode = new Map<string, NodeWays>();
  for (const n of live) {
    const role = roleOf(n);
    const d = direct(n.id);
    const base = { nodeId: n.id, name: n.name, role, direct: d, pairs: [] as WayPair[], noWay: false, staleDays: staleOf(n.id), busy: false };
    // a helper has no relays to read: every node shows what the country sees of it
    if (w.relays === undefined) {
      byNode.set(n.id, { ...base, tone: d.tone, kind: d.tone === "none" ? "nodata" : "direct" });
      continue;
    }
    const serves = role === "serve" || role === "mixed";
    const takes = role === "entry" || (role === "idle" && !!w.country && n.countryCode === w.country);
    if (takes && !serves) {
      const bySrc = new Map<string, RelayMsg[]>();
      for (const r of relays) if (r.viaNodeId === n.id) bySrc.set(r.exitNodeId, [...(bySrc.get(r.exitNodeId) ?? []), r]);
      const broken = [...bySrc.values()].filter((rs) => rs.some((r) => relayState(r) === "failed")).length;
      const offline = down(n.id);
      const hidden = !offline && d.tone === "bad";
      const oldAgent = [...bySrc.values()].some((rs) => rs.some((r) => !r.agentSupported));
      byNode.set(n.id, { ...base, tone: offline || hidden ? "bad" : oldAgent && d.tone === "ok" ? "part" : d.tone, kind: "entry", entry: { offline, hidden, oldAgent, passes: bySrc.size, broken } });
      continue;
    }
    if (!serves) {
      byNode.set(n.id, { ...base, tone: "none", kind: "idle" });
      continue;
    }
    const pairs = pairsOf(n, d).sort((a, b) => Number(b.ok === b.n && b.n > 0) - Number(a.ok === a.n && a.n > 0));
    const alive = pairs.filter((p) => !p.dead);
    const busy = pairs.some((p) => p.state === "busy");
    const full = alive.some((p) => p.n > 0 && p.ok === p.n && p.seen !== "part");
    const partial = alive.some((p) => p.ok > 0);
    // a pair that works is a way even where the country measured nothing; without a pair, the measurement is all there is
    const tone: WayTone = d.tone === "ok" ? "ok" : pairs.length === 0 ? d.tone : full ? "ok" : partial || d.tone === "part" ? "part" : "bad";
    const dead = pairs.length > 0 && alive.length === 0 && tone === "bad" ? pairs[0]! : undefined;
    const kind: WayKind = tone === "ok" ? (d.tone === "ok" ? "direct" : "bypass") : tone === "none" ? "nodata" : tone === "part" ? (partial ? "partly" : "directPart") : dead ? "deadEntry" : "noway";
    byNode.set(n.id, { ...base, tone, kind, pairs, busy, noWay: tone === "bad" && !dead && !busy, ...(dead && { deadBy: { entry: dead.entryName, why: dead.dead as "offline" | "hidden" } }) });
  }

  const all = [...byNode.values()];
  // a helper has no ways to speak of: the rows and the columns of the Ways view are the owner's
  const exits = w.relays === undefined ? [] : all.filter((x) => x.role === "serve" || x.role === "mixed");
  return {
    byNode,
    entries: w.relays === undefined ? [] : all.filter((x) => x.entry !== undefined),
    exits,
    relayN: relays.length,
    downN: relays.filter((r) => relayState(r) === "failed").length,
    reachN: exits.filter((x) => x.tone === "ok").length,
    noWayN: exits.filter((x) => x.tone === "bad").length,
    partN: exits.filter((x) => x.tone === "part").length,
    bridgeN: exits.filter((x) => x.pairs.some((p) => p.bridge > 0)).length,
    unmeasured,
  };
}
