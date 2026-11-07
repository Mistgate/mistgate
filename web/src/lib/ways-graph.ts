import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import { relayState, type RelayMsg } from "./relay";
import { entryOffered, entryState, type Ipv6Entry, type Ipv6Fact, type Lane, type Ring, type Ways } from "./ways";

// The node page's "Ways in" as a graph (ADR 0016): the same lanes and relays the list draws, as cards with typed ports
// and wires. Pure: the screen turns the model into the graph component's nodes and edges and the wires' actions into
// the RPCs the list already calls.
//
//   people ──▶ entry node ──relay──▶ ┐
//   people ───────── direct ───────▶ ├ this node's servers ──▶ Internet | WARP
//   people ───────── IPv6 ─────────▶ ┘                    └──relay──▶ exit node (on an entry node)

export type Net = "udp" | "tcp";
/** The network a protocol's server listens on (the plugins' Listen.Network). */
export const networkOf = (protocol: string): Net | undefined =>
  (({ hysteria2: "udp", awg: "udp", vless: "tcp", mtproto: "tcp" }) as Record<string, Net>)[protocol];

/** A port's type: a relay joins a network to the same network; people and exits join anything of theirs. */
export type PortType = Net | "people" | "egress";
export type WireState = "ok" | "warn" | "bad" | "off" | "pending" | "unknown";
export type Column = 0 | 1 | 2 | 3 | 4;

/** What a port says of the way through it: a working way, a way that works only in part, none, or one a country cuts. */
export type PortStatus = "ok" | "warn" | "none" | "blocked";
export type GPort = { id: string; side: "in" | "out"; type: PortType; status?: PortStatus };

/**
 * The way people have to a server at the node's own address (or its CDN host): works, works in part (some networks are
 * dark, or the lane is shaky), is cut by the audience country, the server is off, it has failed, it is kept out of
 * subscriptions, or nothing says yet.
 */
export type ServerWay = "works" | "partial" | "blocked" | "off" | "failed" | "hidden" | "unknown";

/**
 * The IPv6 entry row of the node: on and working, on while the server's IPv6 does not work, off but it would work (can be
 * turned on), the host has no IPv6, the host's IPv6 is broken, or the node cannot take an entry for another reason.
 */
export type Ipv6State = "on" | "on_down" | "can" | "none" | "broken" | "unavailable";

export type GServer = {
  inboundId: string;
  name: string;
  protocol: string;
  net?: Net;
  port: number;
  egress: "direct" | "warp";
  /** In subscriptions at the node's own address. */
  listed: boolean;
  cdn: boolean;
  on: boolean;
  failed: boolean;
  /** The way people have to it (see ServerWay); the chip on its row. */
  way: ServerWay;
  /** Why an entry cannot forward it; undefined when it can (the server decides finally: an ACME-IP certificate…). */
  noRelay?: "cdn" | "awg" | "net" | "off";
};

export type GCard =
  | { id: "people"; kind: "people"; column: 0; country: string; ports: GPort[] }
  | { id: `entry:${string}`; kind: "entry"; column: 1; nodeId: string; name: string; relays: RelayMsg[]; ports: GPort[] }
  | { id: "node"; kind: "node"; column: 2; nodeId: string; name: string; servers: GServer[]; ipv6?: Lane; v6?: { state: Ipv6State; reason: string }; ports: GPort[] }
  | { id: `exit:${string}`; kind: "exit"; column: 3; nodeId: string; name: string; relays: RelayMsg[]; ports: GPort[] }
  | { id: "internet" | "warp"; kind: "internet" | "warp"; column: 4; ports: GPort[] };

export type GWire = {
  id: string;
  from: { card: string; port: string };
  to: { card: string; port: string };
  kind: "direct" | "cdn" | "ipv6" | "reach" | "relay" | "egress";
  state: WireState;
  /** Traffic flows along it: a working relay, a direct way the country sees. */
  flowing: boolean;
  /** The audience country cuts this way off (as against a way that is broken on our side): drawn quiet, with a badge. */
  blocked?: boolean;
  relay?: RelayMsg;
  inboundId?: string;
};

export type WaysGraph = { cards: GCard[]; wires: GWire[] };

export type GraphInput = {
  node: { id: string; name: string };
  inbounds: readonly {
    id: string;
    profileName: string;
    protocol: string;
    port: number;
    state: InboundState;
    enabled: boolean;
    egress: string;
    cdn: boolean;
    directInSubscriptions: boolean;
  }[];
  ways: Ways;
  /** The audience country (ISO code), empty when the panel has none. */
  country: string;
  /** The IPv6 fact of the server and its entry (Node.ipv6): without it the row shows only when the lane exists. */
  ipv6?: { fact: Ipv6Fact; v6: Ipv6Entry };
  /** The node has WARP: its exit card shows even before a server leaves through it. */
  warp: boolean;
  /** Entry nodes the owner put on the canvas to draw a new relay from (no relay yet). */
  extraEntries?: readonly { id: string; name: string }[];
};

/** Port ids: stable strings the screen maps back to servers and relays. */
export const port = {
  people: "people",
  serverIn: (inboundId: string) => `in:${inboundId}`,
  serverOut: (inboundId: string) => `out:${inboundId}`,
  relay: (relayId: string) => `relay:${relayId}`,
  newRelay: "relay:new",
  reach: "reach",
  ipv6: "ipv6",
  exit: "exit",
} as const;

const fromRing = (r: Ring): WireState => (({ ok: "ok", half: "warn", warn: "warn", bad: "bad", busy: "pending", off: "off" }) as const)[r];

/** A relay as a wire: working (traffic), being switched on, failing, switched off. */
export function relayWire(r: Pick<RelayMsg, "enabled" | "state" | "check" | "monitor">): WireState {
  return (({ on: "ok", applying: "pending", failed: "bad", off: "off" }) as const)[relayState(r)];
}

/** The IPv6 row's state, and when it is off, why it cannot be turned on. */
export function ipv6StateOf(fact: Ipv6Fact, v6: Ipv6Entry): { state: Ipv6State; reason: string } {
  if (v6.entry) return { state: entryState(v6) === "down" ? "on_down" : "on", reason: v6.unavailableReason };
  if (fact === "none" || v6.unavailableReason === "no_ipv6") return { state: "none", reason: "no_ipv6" };
  if (fact === "broken" || v6.unavailableReason === "ipv6_broken") return { state: "broken", reason: "ipv6_broken" };
  if (entryOffered(v6)) return { state: "can", reason: "" };
  return { state: "unavailable", reason: v6.unavailableReason };
}

/** The way people have to a server from the lane that carries it. */
export function wayOf(s: Pick<GServer, "on" | "failed" | "listed" | "cdn">, lane: Lane | undefined): ServerWay {
  if (!s.on) return "off";
  if (s.failed) return "failed";
  if (!s.listed) return "hidden";
  if (!lane) return "unknown";
  if (lane.ring === "off") return "off";
  if (lane.ring === "busy") return "unknown";
  if (lane.state === "bad") return "failed";
  // the country sees a CDN host, not the node: its lane has no verdict of its own
  if (!s.cdn && lane.seen === "none") return "blocked";
  return lane.ring === "ok" ? "works" : "partial";
}

function serverOf(i: GraphInput["inbounds"][number]): GServer {
  const net = networkOf(i.protocol);
  const on = i.enabled && i.state !== InboundState.DISABLED;
  return {
    inboundId: i.id,
    name: i.profileName,
    protocol: i.protocol,
    net,
    port: i.port,
    egress: i.egress === "warp" ? "warp" : "direct",
    listed: i.directInSubscriptions,
    cdn: i.cdn,
    on,
    failed: i.state === InboundState.FAILED,
    way: "unknown",
    noRelay: i.cdn ? "cdn" : i.protocol === "awg" ? "awg" : !net ? "net" : !on ? "off" : undefined,
  };
}

const order = (a: GServer, b: GServer) => (a.net ?? "").localeCompare(b.net ?? "") || a.port - b.port || a.name.localeCompare(b.name);

export function waysGraph(g: GraphInput): WaysGraph {
  const servers = g.inbounds.map(serverOf).sort(order);
  const lane = (id: string) => g.ways.lanes.find((l) => l.id === id);
  for (const s of servers) s.way = wayOf(s, s.cdn ? lane("cdn") : lane("direct"));
  const ipv6 = lane("ipv6");
  const v6 = g.ipv6 ? ipv6StateOf(g.ipv6.fact, g.ipv6.v6) : ipv6 ? { state: (ipv6.state === "bad" ? "on_down" : ipv6.state === "off" ? "can" : "on") as Ipv6State, reason: "" } : undefined;
  const cards: GCard[] = [];
  const wires: GWire[] = [];

  cards.push({ id: "people", kind: "people", column: 0, country: g.country, ports: [{ id: port.people, side: "out", type: "people" }] });

  // the entries: one card per node that relays to this one, one output per relay, plus an output to draw a new relay
  const entryLanes = g.ways.lanes.filter((l) => l.kind === "via" && l.peer);
  const entries = new Map(entryLanes.map((l) => [l.peer!.id, { name: l.peer!.name, lane: l as Lane | undefined }]));
  for (const e of g.extraEntries ?? []) if (e.id !== g.node.id && !entries.has(e.id)) entries.set(e.id, { name: e.name, lane: undefined });
  for (const [id, e] of entries) {
    const relays = e.lane?.relays ?? [];
    const card = `entry:${id}` as const;
    cards.push({
      id: card,
      kind: "entry",
      column: 1,
      nodeId: id,
      name: e.name,
      relays,
      ports: [
        { id: port.reach, side: "in", type: "people" },
        ...relays.map((r) => ({ id: port.relay(r.id), side: "out" as const, type: (r.network === "udp" ? "udp" : "tcp") as PortType })),
        { id: port.newRelay, side: "out", type: "udp" }, // retyped by the screen's canConnect: any relayable server
      ],
    });
    wires.push({
      id: `reach:${id}`,
      from: { card: "people", port: port.people },
      to: { card, port: port.reach },
      kind: "reach",
      state: !e.lane ? "unknown" : e.lane.seen === "all" ? "ok" : e.lane.seen === "part" ? "warn" : e.lane.seen === "none" ? "bad" : "unknown",
      flowing: false,
      blocked: e.lane?.seen === "none" || undefined,
    });
    for (const r of relays) {
      const state = relayWire(r);
      wires.push({ id: `relay:${r.id}`, from: { card, port: port.relay(r.id) }, to: { card: "node", port: port.serverIn(r.inboundId) }, kind: "relay", state, flowing: state === "ok", relay: r, inboundId: r.inboundId });
    }
  }

  cards.push({
    id: "node",
    kind: "node",
    column: 2,
    nodeId: g.node.id,
    name: g.node.name,
    servers,
    ipv6,
    v6,
    ports: [
      ...(v6 ? [{ id: port.ipv6, side: "in" as const, type: "people" as PortType, status: ({ on: "ok", on_down: "warn", can: "none", none: "none", broken: "none", unavailable: "none" } as const)[v6.state] as PortStatus }] : []),
      ...servers.flatMap((s) => [
        { id: port.serverIn(s.inboundId), side: "in" as const, type: (s.net ?? "people") as PortType, status: ({ works: "ok", partial: "warn", blocked: "blocked", off: "none", failed: "none", hidden: "none", unknown: "none" } as const)[s.way] as PortStatus },
        { id: port.serverOut(s.inboundId), side: "out" as const, type: "egress" as PortType },
      ]),
      ...g.ways.out.flatMap((l) => l.relays.map((r) => ({ id: port.relay(r.id), side: "out" as const, type: (r.network === "udp" ? "udp" : "tcp") as PortType }))),
    ],
  });

  // straight from people: a server that is on and in subscriptions at the node's address (or its CDN host)
  const direct = lane("direct");
  const cdn = lane("cdn");
  for (const s of servers) {
    if (!s.on || !s.listed) continue;
    const l = s.cdn ? cdn : direct;
    const state = l ? fromRing(l.ring) : "unknown";
    wires.push({
      id: `${s.cdn ? "cdn" : "direct"}:${s.inboundId}`,
      from: { card: "people", port: port.people },
      to: { card: "node", port: port.serverIn(s.inboundId) },
      kind: s.cdn ? "cdn" : "direct",
      state: s.failed ? "bad" : state,
      flowing: !s.failed && state === "ok",
      blocked: s.way === "blocked" || undefined,
      inboundId: s.inboundId,
    });
  }
  // the IPv6 entry is a way in only while it is on
  if (ipv6 && v6 && (v6.state === "on" || v6.state === "on_down")) {
    const state = v6.state === "on_down" ? "bad" : fromRing(ipv6.ring);
    const blocked = v6.state === "on" && ipv6.seen === "none";
    wires.push({ id: "ipv6", from: { card: "people", port: port.people }, to: { card: "node", port: port.ipv6 }, kind: "ipv6", state, flowing: state === "ok", blocked: blocked || undefined });
  }

  // on an entry node: the exits it forwards to, with the servers it forwards
  for (const l of g.ways.out) {
    if (!l.peer) continue;
    const card = `exit:${l.peer.id}` as const;
    cards.push({
      id: card,
      kind: "exit",
      column: 3,
      nodeId: l.peer.id,
      name: l.peer.name,
      relays: l.relays,
      ports: l.relays.map((r) => ({ id: port.serverIn(r.inboundId), side: "in" as const, type: (r.network === "udp" ? "udp" : "tcp") as PortType })),
    });
    for (const r of l.relays) {
      const state = relayWire(r);
      wires.push({ id: `relay:${r.id}`, from: { card: "node", port: port.relay(r.id) }, to: { card, port: port.serverIn(r.inboundId) }, kind: "relay", state, flowing: state === "ok", relay: r, inboundId: r.inboundId });
    }
  }

  // the way out of each server that is on
  const leaving = servers.filter((s) => s.on);
  const exits: ("internet" | "warp")[] = [];
  if (leaving.some((s) => s.egress === "direct") || !g.warp) exits.push("internet");
  if (g.warp || leaving.some((s) => s.egress === "warp")) exits.push("warp");
  for (const id of exits) cards.push({ id, kind: id, column: 4, ports: [{ id: port.exit, side: "in", type: "egress" }] });
  for (const s of leaving) {
    wires.push({
      id: `egress:${s.inboundId}`,
      from: { card: "node", port: port.serverOut(s.inboundId) },
      to: { card: s.egress === "warp" ? "warp" : "internet", port: port.exit },
      kind: "egress",
      state: s.failed ? "bad" : "ok",
      flowing: false,
      inboundId: s.inboundId,
    });
  }
  return { cards, wires };
}

/**
 * Whether a new relay may be drawn from an entry to a server of this node: why not, or undefined when it may. The same
 * rules the relay dialog applies before the panel's own (one relay per server and entry, the entry is another node, the
 * server can be forwarded).
 */
export function relayRefusal(entryNodeId: string, nodeId: string, server: GServer, relays: readonly RelayMsg[]): "same_node" | "exists" | NonNullable<GServer["noRelay"]> | undefined {
  if (entryNodeId === nodeId) return "same_node";
  if (server.noRelay) return server.noRelay;
  if (relays.some((r) => r.viaNodeId === entryNodeId && r.inboundId === server.inboundId)) return "exists";
  return undefined;
}

/**
 * The picture's layout, in columns: people, the entries, the node, the exits. People sit on the node's own horizontal axis
 * and the entries above and below that band, so the straight ways from people to the servers never pass under a card;
 * the exits are centred on the same axis. Cards are looked up by their ids ("people", "entry:…", "node", "exit:…",
 * "internet"/"warp"); sizes are the measured ones.
 */
export function waysLayout(cards: readonly { id: string; w: number; h: number }[], opts: { colGap?: number; rowGap?: number; band?: number } = {}): Record<string, { x: number; y: number }> {
  const colGap = opts.colGap ?? 112;
  const rowGap = opts.rowGap ?? 28;
  const band = opts.band ?? 36;
  const hasExitCards = cards.some((c) => c.id.startsWith("exit:"));
  const col = (id: string) => (id === "people" ? 0 : id.startsWith("entry:") ? 1 : id === "node" ? 2 : id.startsWith("exit:") ? 3 : hasExitCards ? 4 : 3);
  const cols = new Map<number, { id: string; w: number; h: number }[]>();
  for (const c of cards) cols.set(col(c.id), [...(cols.get(col(c.id)) ?? []), c]);
  const xs = new Map<number, number>();
  let x = 0;
  for (const k of [...cols.keys()].sort((a, b) => a - b)) {
    xs.set(k, x);
    x += Math.max(...cols.get(k)!.map((c) => c.w)) + colGap;
  }
  const out: Record<string, { x: number; y: number }> = {};
  const node = cards.find((c) => c.id === "node");
  const top = 0;
  const bottom = node?.h ?? 0;
  const cy = (top + bottom) / 2;
  if (node) out.node = { x: xs.get(2)!, y: top };
  for (const c of cols.get(0) ?? []) out[c.id] = { x: xs.get(0)!, y: cy - c.h / 2 };
  // the entries alternate above and below the band of the node's servers
  let above = top - band;
  let below = bottom + band;
  (cols.get(1) ?? []).forEach((c, i) => {
    if (i % 2 === 0) {
      above -= c.h;
      out[c.id] = { x: xs.get(1)!, y: above };
      above -= rowGap;
    } else {
      out[c.id] = { x: xs.get(1)!, y: below };
      below += c.h + rowGap;
    }
  });
  for (const k of [3, 4]) {
    const list = cols.get(k) ?? [];
    const total = list.reduce((s, c) => s + c.h, 0) + rowGap * Math.max(0, list.length - 1);
    let y = cy - total / 2;
    for (const c of list) {
      out[c.id] = { x: xs.get(k)!, y };
      y += c.h + rowGap;
    }
  }
  return out;
}
