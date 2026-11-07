import { relayState, type RelayMsg } from "./relay";
import type { Lane, WayId } from "./ways";
import { ipv6StateOf, serverOf, serverOrder, type GraphInput, type GServer, type Ipv6State } from "./ways-graph";

// The Ways in tab as a route map (ADR 0016, revised): four columns that answer one question, how do people of the audience
// country get to the internet through this node. Who (people) -> How they get in (a card per way: direct, CDN, each entry
// node, IPv6) -> the node (its servers as chips) -> where they exit (internet, WARP); on an entry node a fifth column,
// the nodes it forwards to. Pure: the view draws the cards and wires and words every `Say`.

/** A sentence for the view to say: an i18n key, its params, and a count when the key has plural forms. */
export type Say = { key: string; params?: Record<string, string | number>; n?: number };

export type RmState = "ok" | "partial" | "blocked" | "off" | "bad" | "checking";
export type RmChipState = "ok" | "warn" | "bad" | "off";
export type RmChip = { id: string; label: string; port?: number; net?: string; state: RmChipState; inboundId: string; relay?: RelayMsg; server?: GServer };
export type AltRef = { kind: "direct" | "cdn" | "via" | "ipv6"; name?: string };

export type RmWay = {
  /** The card id: "way:direct", "way:cdn", "way:via:<node id>", "way:ipv6". */
  id: string;
  wayId: WayId;
  kind: "direct" | "cdn" | "via" | "ipv6";
  /** The entry node of a via way. */
  peer?: { id: string; name: string };
  state: RmState;
  badge: Say;
  sub: Say;
  /** Why this way matters now: it is cut or broken, and people go through these instead (or through nothing). */
  consequence?: { kind: "blocked" | "bad"; alt: AltRef[] };
  chips: RmChip[];
  relays: RelayMsg[];
  /** The servers (or relays) the way carries: the thickness of its wires. */
  count: number;
};

export type RmForward = { id: string; wayId: WayId; peer: { id: string; name: string }; state: RmState; badge: Say; sub: Say; chips: RmChip[]; relays: RelayMsg[]; count: number };
export type RmExit = { id: "internet" | "warp"; count: number; sub: Say };
export type RmWire = { id: string; from: string; to: string; style: "flow" | "partial" | "muted" | "fail"; count: number; label?: Say };

export type RouteMap = {
  /** An entry node: a fifth column of the nodes it forwards to. */
  hasForwards: boolean;
  people: { country: string };
  ways: RmWay[];
  /** What the "add a way" card can offer without asking anything of the panel. */
  canAddIpv6: boolean;
  node: { id: string; name: string; online: boolean; address: string; countryCode: string; chips: RmChip[] };
  exits: RmExit[];
  forwards: RmForward[];
  wires: RmWire[];
};

/** `relaysKnown` false: the viewer is not given the relays (a helper), so "people go through..." cannot be said. */
export type RouteMapInput = GraphInput & { node: { id: string; name: string; address?: string; countryCode?: string }; relaysKnown?: boolean };

const relayChip = (r: RelayMsg): RmChip => {
  const s = relayState(r);
  return { id: `r:${r.id}`, label: r.profileName, port: r.network === "udp" || r.network === "tcp" ? r.port : r.port, net: r.network, state: s === "on" ? "ok" : s === "failed" ? "bad" : s === "applying" ? "warn" : "off", inboundId: r.inboundId, relay: r };
};

/** A server's dot: running (ok), failed, or off / kept out of subscriptions (grey). */
export const serverDot = (s: GServer): RmChipState => (s.way === "off" ? "off" : s.way === "failed" ? "bad" : s.way === "hidden" ? "off" : "ok");

const serverChip = (s: GServer): RmChip => ({ id: `s:${s.inboundId}`, label: s.name, port: s.port, net: s.net, state: serverDot(s), inboundId: s.inboundId, server: s });

/** The state of a direct or CDN way from its lane. */
function directState(l: Lane, offline: boolean, cdn: boolean): RmState {
  if (offline || l.hidden || l.state === "off") return "off";
  if (l.state === "bad") return "bad";
  if (l.state === "busy") return "checking";
  if (!cdn && l.seen === "none") return "blocked";
  if (l.state === "warn" || l.ring === "half" || l.ring === "warn" || (!cdn && l.seen === "part")) return "partial";
  return "ok";
}

/** The state of a way through an entry node (or, on an entry node, to an exit): its relays, then how much of the country sees the entry. */
function viaState(l: Lane, withSeen: boolean): RmState {
  if (l.state === "busy") return "checking";
  if (l.state === "bad") return "bad";
  if (l.state === "off") return "off";
  if (l.state === "warn") return "partial";
  if (withSeen && l.seen === "none") return "blocked";
  if (withSeen && l.seen === "part") return "partial";
  return "ok";
}

const flowOf = (s: RmState): RmWire["style"] => (s === "ok" ? "flow" : s === "partial" ? "partial" : "muted");

export function routeMap(g: RouteMapInput): RouteMap {
  const servers = g.inbounds.map((i) => serverOf(i)).sort(serverOrder);
  // each server's own way comes from the direct or CDN lane, as in the graph's mapping
  const { ways: ws } = g;
  const lane = (id: string) => ws.lanes.find((l) => l.id === id);
  const direct = lane("direct");
  const cdnLane = lane("cdn");
  const ipv6Lane = lane("ipv6");
  for (const s of servers) {
    if (!s.on) s.way = "off";
    else if (s.failed) s.way = "failed";
    else if (!s.listed) s.way = "hidden";
    else s.way = "works";
  }
  const offline = ws.offline;
  const online = servers.filter((s) => s.on && !s.failed);
  const listedDirect = servers.filter((s) => s.on && s.listed && !s.cdn);
  const listedCdn = servers.filter((s) => s.on && s.listed && s.cdn);
  const ways: RmWay[] = [];

  if (direct) {
    const state = directState(direct, offline, false);
    const sub: Say = offline
      ? { key: "rm.sub.offline" }
      : direct.hidden
        ? { key: "rm.sub.hidden" }
        : { key: "rm.sub.direct", params: { address: g.node.address || g.node.name }, n: listedDirect.length };
    ways.push({ id: "way:direct", wayId: "direct", kind: "direct", state, badge: badge(state, g.country), sub, chips: [], relays: [], count: listedDirect.length });
  }
  if (cdnLane && servers.some((s) => s.cdn)) {
    const state = directState(cdnLane, offline, true);
    const cdnServers = servers.filter((s) => s.cdn);
    ways.push({
      id: "way:cdn",
      wayId: "cdn",
      kind: "cdn",
      state,
      badge: badge(state, g.country),
      sub: cdnLane.hidden ? { key: "rm.sub.hidden" } : { key: "rm.sub.cdn", n: listedCdn.length },
      chips: cdnServers.map(serverChip),
      relays: [],
      count: listedCdn.length,
    });
  }
  for (const l of ws.lanes.filter((x) => x.kind === "via" && x.peer)) {
    const state = viaState(l, true);
    const seen = l.seen === "all" ? "all" : l.seen === "part" ? "part" : l.seen === "none" ? "none" : "unknown";
    ways.push({
      id: `way:${l.id}`,
      wayId: l.id,
      kind: "via",
      peer: l.peer,
      state,
      badge: badge(state, g.country),
      sub: { key: `rm.sub.via.${seen}`, n: l.relays.length },
      chips: l.relays.map(relayChip),
      relays: l.relays,
      count: l.relays.length,
    });
  }
  for (const e of g.extraEntries ?? []) {
    if (e.id === g.node.id || ways.some((w) => w.peer?.id === e.id)) continue;
    ways.push({ id: `way:via:${e.id}`, wayId: `via:${e.id}`, kind: "via", peer: e, state: "off", badge: badge("off", g.country), sub: { key: "rm.sub.viaNone" }, chips: [], relays: [], count: 0 });
  }
  const v6 = g.ipv6 ? ipv6StateOf(g.ipv6.fact, g.ipv6.v6) : ipv6Lane ? { state: (ipv6Lane.state === "bad" ? "on_down" : ipv6Lane.state === "off" ? "can" : "on") as Ipv6State, reason: "" } : undefined;
  if (v6 && (v6.state === "on" || v6.state === "on_down" || v6.state === "can" || v6.state === "broken")) {
    const state: RmState =
      v6.state === "on" ? (ipv6Lane?.seen === "none" ? "blocked" : ipv6Lane?.seen === "part" ? "partial" : "ok") : v6.state === "on_down" || v6.state === "broken" ? "bad" : "off";
    const sub: Say = { key: `rm.sub.v6.${v6.state}`, params: { addr: g.ipv6?.v6.entry ?? "" } };
    ways.push({ id: "way:ipv6", wayId: "ipv6", kind: "ipv6", state, badge: v6.state === "can" ? { key: "rm.badge.off" } : badge(state, g.country), sub, chips: [], relays: [], count: listedDirect.length });
  }

  // what a cut or broken way means: the others that work
  for (const w of ways) {
    if (g.relaysKnown === false || (w.state !== "blocked" && w.state !== "bad")) continue;
    const alt: AltRef[] = ways.filter((o) => o !== w && o.state === "ok").map((o) => ({ kind: o.kind, name: o.peer?.name }));
    w.consequence = { kind: w.state, alt };
  }

  const exits: RmExit[] = [];
  const toInternet = online.filter((s) => s.egress === "direct");
  const toWarp = online.filter((s) => s.egress === "warp");
  if (toInternet.length > 0 || toWarp.length === 0) exits.push({ id: "internet", count: toInternet.length, sub: { key: "rm.exit.internet", params: { node: g.node.name }, n: toInternet.length } });
  if (toWarp.length > 0) exits.push({ id: "warp", count: toWarp.length, sub: { key: "rm.exit.warp", n: toWarp.length } });

  const forwards: RmForward[] = ws.out
    .filter((l) => l.peer)
    .map((l) => {
      const state = viaState(l, false);
      return { id: `fwd:${l.peer!.id}`, wayId: l.id, peer: l.peer!, state, badge: badge(state, g.country), sub: { key: "rm.sub.fwd", n: l.relays.length }, chips: l.relays.map(relayChip), relays: l.relays, count: l.relays.length };
    });

  const wires: RmWire[] = [];
  const anyOk = !offline && ways.some((w) => w.state === "ok");
  for (const w of ways) {
    const style = offline ? "muted" : w.state === "bad" && w.kind === "via" ? "fail" : flowOf(w.state);
    const n = Math.max(1, w.count);
    wires.push({ id: `people:${w.id}`, from: "people", to: w.id, style, count: n });
    wires.push({ id: `${w.id}:node`, from: w.id, to: "node", style, count: n, label: w.kind === "via" || w.kind === "cdn" ? { key: "rm.wire.n", n: w.count } : undefined });
  }
  for (const e of exits) wires.push({ id: `node:exit:${e.id}`, from: "node", to: `exit:${e.id}`, style: anyOk ? "flow" : "muted", count: Math.max(1, e.count) });
  for (const f of forwards) wires.push({ id: `node:${f.id}`, from: "node", to: f.id, style: offline ? "muted" : f.state === "bad" ? "fail" : flowOf(f.state), count: Math.max(1, f.count) });

  return {
    hasForwards: forwards.length > 0,
    people: { country: g.country },
    ways,
    canAddIpv6: v6?.state === "can",
    node: { id: g.node.id, name: g.node.name, online: !offline, address: g.node.address ?? "", countryCode: g.node.countryCode ?? "", chips: servers.map(serverChip) },
    exits,
    forwards,
    wires,
  };
}

function badge(state: RmState, cc: string): Say {
  return state === "blocked" ? { key: "rm.badge.blocked", params: { cc } } : { key: `rm.badge.${state}` };
}

/**
 * The route map's layout: the columns evenly spaced, each column's cards stacked and centred on the middle line; the
 * headers of the columns on one line above the tallest column. On an entry node the exits are top-aligned and the forwards bottom-aligned. Ids: "col:<n>" headers, "people", "way:*", "ghost:way",
 * "node", "exit:*", "ghost:exit", "fwd:*".
 */
export function routeLayout(cards: readonly { id: string; w: number; h: number }[], opts: { colGap?: number; gaps?: number[]; rowGap?: number } = {}): Record<string, { x: number; y: number }> {
  const colGap = opts.colGap ?? 56;
  const rowGap = opts.rowGap ?? 14;
  const hasFwd = cards.some((c) => c.id.startsWith("fwd:"));
  const colOf = (id: string) => (id.startsWith("col:") ? Number(id.slice(4)) : id === "people" ? 0 : id.startsWith("way:") || id === "ghost:way" || id === "hint:way" ? 1 : id === "node" ? 2 : id.startsWith("exit:") || id === "ghost:exit" ? 3 : 4);
  const cols = new Map<number, { id: string; w: number; h: number }[]>();
  for (const c of cards) if (!c.id.startsWith("col:")) cols.set(colOf(c.id), [...(cols.get(colOf(c.id)) ?? []), c]);
  const count = hasFwd ? 5 : 4;
  const xs: number[] = [];
  let x = 0;
  for (let k = 0; k < count; k++) {
    xs.push(x);
    x += Math.max(0, ...(cols.get(k) ?? [{ w: 0 }]).map((c) => c.w), cards.find((c) => c.id === `col:${k}`)?.w ?? 0) + (opts.gaps?.[k] ?? colGap);
  }
  const heights = [...cols.values()].map((list) => list.reduce((s, c) => s + c.h, 0) + rowGap * (list.length - 1));
  const tallest = Math.max(0, ...heights);
  const out: Record<string, { x: number; y: number }> = {};
  const mid = tallest / 2;
  for (const [k, list] of cols) {
    const total = list.reduce((s, c) => s + c.h, 0) + rowGap * (list.length - 1);
    // on an entry node the exits sit at the top and the forwards at the bottom, so the wires from the node to them never cross
    let y = hasFwd && k === 3 ? 0 : hasFwd && k === 4 ? tallest - total : mid - total / 2;
    for (const c of list) {
      out[c.id] = { x: xs[k]!, y };
      y += c.h + rowGap;
    }
  }
  for (let k = 0; k < count; k++) if (cards.some((c) => c.id === `col:${k}`)) out[`col:${k}`] = { x: xs[k]!, y: -34 };
  return out;
}
