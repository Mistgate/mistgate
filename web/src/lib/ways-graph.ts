import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import { type RelayMsg } from "./relay";
import { entryOffered, entryState, type Ipv6Entry, type Ipv6Fact, type Lane, type Ways } from "./ways";

// What the node page's "Ways in" says of each server (the lanes and relays the list draws), for the route map and the relay
// refusals: the network of a protocol, the way people have to a server, the IPv6 entry row. Pure.

export type Net = "udp" | "tcp";
/** The network a protocol's server listens on (the plugins' Listen.Network). */
export const networkOf = (protocol: string): Net | undefined =>
  (({ hysteria2: "udp", awg: "udp", vless: "tcp", mtproto: "tcp", trusttunnel: "tcp" }) as Record<string, Net>)[protocol];

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
  noRelay?: "cdn" | "net" | "off";
};

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

export function serverOf(i: GraphInput["inbounds"][number]): GServer {
  const net = networkOf(i.protocol);
  // GetNode gives a switched-off server as state DISABLED (older panels never filled enabled): the state decides
  const on = i.state !== InboundState.DISABLED;
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
    noRelay: i.cdn ? "cdn" : !net ? "net" : !on ? "off" : undefined,
  };
}

export const serverOrder = (a: GServer, b: GServer) => (a.net ?? "").localeCompare(b.net ?? "") || a.port - b.port || a.name.localeCompare(b.name);

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
