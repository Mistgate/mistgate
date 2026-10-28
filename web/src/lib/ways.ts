import type { StatusKind } from "@/components/ui/status";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CheckStatus, CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import type { CountryCheck } from "./health";
import { isCut, relayState, type CountryHint, type RelayMsg } from "./relay";

// The ways people reach a node (the node page's "Ways in" tab): straight to its address, through another node (a relay),
// over IPv6 (later), and, on an entry node, the nodes people reach through it. One lane per way, each with its own state
// and whether people of the audience country see it. The verdicts are the ones Health and the relay dialog use
// (countryLook, relayState, the server's relay hints): the lanes add no judgement of their own.

export type WayId = "direct" | "ipv6" | `via:${string}` | `out:${string}`;
/** How much of the audience country sees the lane: every network, some, none, or no data. */
export type Seen = "all" | "part" | "none" | "unknown";
/** The ring on the line: its state, and for a lane that works, how much of the country sees it. */
export type Ring = "ok" | "half" | "warn" | "bad" | "busy" | "off";
/** What the doctor's ipv6 check says of the server: works, none, an address that does not work, no report. */
export type Ipv6Fact = "ok" | "none" | "broken" | "unknown";

export type Lane = {
  id: WayId;
  kind: "direct" | "via" | "ipv6" | "out";
  /** The lane's own state: ours to fix when it is bad. */
  state: StatusKind;
  seen: Seen;
  ring: Ring;
  /** Broken on our side (a relay down, a port that does not answer): counts in the tab's badge. A block by a country does not. */
  fault: boolean;
  /** The other node: the entry of a "via" lane, the exit of an "out" one. */
  peer?: { id: string; name: string };
  /** The relays of a via / out lane, oldest first. */
  relays: RelayMsg[];
  /** The networks that see the lane dark: the node's own (direct), the entry's among the exit's (via). */
  dark: string[];
};

export type WaysInput = {
  node: { id: string; status: NodeStatus };
  inbounds: readonly { state: InboundState }[];
  portChecks?: readonly { verdict: string }[];
  tcpChecks?: readonly { verdict: string }[];
  /** The client-side checks of the node's profiles (Health). */
  cells?: readonly { deployed: boolean; last?: { status: CheckStatus } }[];
  /** The node seen from the audience country (IPv4). */
  country?: CountryCheck;
  /** Every relay with this node at either end; undefined for a helper (RelayService is the owner's). */
  relays?: readonly RelayMsg[];
  /** Another node's country check: the entries'. */
  countryOf?: (nodeId: string) => CountryCheck | undefined;
  ipv6?: Ipv6Fact;
  hasIpv6?: boolean;
};

export type Ways = {
  /** The ways in: direct, through each entry, IPv6. */
  lanes: Lane[];
  /** On an entry node: the exits reached through it. */
  out: Lane[];
  /** The lanes broken on our side. */
  badge: number;
  /** The lane a wide screen opens without ?way: the first with a problem, else direct. */
  first: WayId;
  offline: boolean;
};

/** The hint of the relay dialog for a country check (internal/panel/health/relay_hints.go): its verdict word and dark networks. */
export function hintOf(c?: CountryCheck): CountryHint | undefined {
  if (!c) return undefined;
  const verdict = (
    {
      [CountryVerdict.OK]: "ok",
      [CountryVerdict.PARTIAL]: "partial",
      [CountryVerdict.TCP_BLOCKED]: "blocked",
      [CountryVerdict.BLOCKED]: "blocked",
      [CountryVerdict.LISTED]: "listed",
    } as Partial<Record<CountryVerdict, string>>
  )[c.verdict];
  if (!verdict) return undefined;
  const dark: string[] = [];
  const add = (n: string) => n && !dark.includes(n) && dark.push(n);
  for (const v of c.vantages) if (v.state === "dark") add(v.network || v.key);
  if (verdict === "blocked") for (const p of c.probes) if (p.valid) add(p.network);
  return { verdict, dark: dark.sort(), atUnix: c.atUnix };
}

const seenOf = (h?: CountryHint): Seen => (h?.verdict === "ok" ? "all" : h?.verdict === "partial" ? "part" : h?.verdict === "blocked" ? "none" : "unknown");

const rank: Record<StatusKind, number> = { ok: 0, off: 0, blip: 0, busy: 1, warn: 2, bad: 3 };
const worst = (...ks: StatusKind[]): StatusKind => ks.reduce((a, b) => (rank[b] > rank[a] ? b : a), "ok");

/** A group of relays as one lane state: all down bad, some down warn, any turning on busy, any working ok, else off. */
export function relaysState(list: readonly Pick<RelayMsg, "enabled" | "state" | "check" | "monitor">[]): StatusKind {
  const s = list.map(relayState);
  if (s.includes("applying")) return "busy";
  const down = s.filter((x) => x === "failed").length;
  if (down > 0) return down === s.length ? "bad" : "warn";
  return s.includes("on") ? "ok" : "off";
}

function ringOf(state: StatusKind, seen: Seen): Ring {
  if (state === "bad" || state === "busy" || state === "off" || state === "warn") return state;
  return seen === "none" ? "bad" : seen === "part" ? "half" : "ok";
}

function lane(l: Omit<Lane, "ring" | "relays" | "dark"> & Partial<Pick<Lane, "relays" | "dark">>): Lane {
  return { relays: [], dark: [], ...l, ring: ringOf(l.state, l.seen) };
}

/** The relays grouped by the node at the other end, in their order (oldest first). */
function byPeer(list: readonly RelayMsg[], peer: (r: RelayMsg) => [string, string]): { id: string; name: string; relays: RelayMsg[] }[] {
  const out = new Map<string, { id: string; name: string; relays: RelayMsg[] }>();
  for (const r of list) {
    const [id, name] = peer(r);
    const g = out.get(id) ?? { id, name, relays: [] };
    g.relays.push(r);
    out.set(id, g);
  }
  return [...out.values()];
}

export function lanesOf(w: WaysInput): Ways {
  const offline = w.node.status === NodeStatus.DOWN || w.node.status === NodeStatus.BLIP;
  const enabled = w.inbounds.filter((i) => i.state !== InboundState.DISABLED);
  const failed = enabled.filter((i) => i.state === InboundState.FAILED).length;
  const udp = (w.portChecks ?? []).map((c) => c.verdict);
  const tcp = (w.tcpChecks ?? []).map((c) => c.verdict);
  const cells = (w.cells ?? []).filter((c) => c.deployed && c.last);
  const cellsDown = cells.filter((c) => c.last!.status === CheckStatus.FAILED).length;
  const portsBroken = udp.includes("broken") || tcp.some((v) => v === "refused" || v === "timeout");
  const direct: StatusKind =
    offline || enabled.length === 0
      ? "off"
      : worst(
          failed === 0 ? "ok" : failed === enabled.length ? "bad" : "warn",
          portsBroken ? "bad" : udp.includes("lossy") || tcp.some((v) => v === "other" || v === "busy") ? "warn" : "ok",
          cellsDown === 0 ? "ok" : cellsDown === cells.length ? "bad" : "warn",
        );
  const own = hintOf(w.country);
  const lanes: Lane[] = [lane({ id: "direct", kind: "direct", state: direct, seen: seenOf(own), fault: !offline && portsBroken, dark: own?.dark ?? [] })];

  // through an entry: the entry against the networks that see this node dark, else the entry's own verdict
  const cut = isCut(own) && own!.dark.length > 0;
  for (const g of byPeer((w.relays ?? []).filter((r) => r.exitNodeId === w.node.id), (r) => [r.viaNodeId, r.viaNodeName])) {
    const entry = hintOf(w.countryOf?.(g.id));
    let seen = seenOf(entry);
    let dark = entry?.dark ?? [];
    if (cut && entry) {
      dark = own!.dark.filter((n) => entry.dark.includes(n));
      seen = dark.length > 0 ? "part" : "all";
    }
    const state = relaysState(g.relays);
    lanes.push(lane({ id: `via:${g.id}`, kind: "via", state, seen, fault: g.relays.some((r) => relayState(r) === "failed"), peer: { id: g.id, name: g.name }, relays: g.relays, dark }));
  }

  // the IPv6 entry is not built yet: a server with IPv6 shows the lane switched off, with why
  if (w.ipv6 === "ok" || w.ipv6 === "broken" || w.hasIpv6) lanes.push(lane({ id: "ipv6", kind: "ipv6", state: "off", seen: seenOf(hintOf(w.country?.ipv6)), fault: false }));

  const out = byPeer((w.relays ?? []).filter((r) => r.viaNodeId === w.node.id), (r) => [r.exitNodeId, r.exitNodeName]).map((g) =>
    lane({ id: `out:${g.id}`, kind: "out", state: relaysState(g.relays), seen: "unknown", fault: g.relays.some((r) => relayState(r) === "failed"), peer: { id: g.id, name: g.name }, relays: g.relays }),
  );

  const all = [...lanes, ...out];
  const problem = all.find((l) => l.ring === "bad" || l.ring === "warn" || l.ring === "half");
  return { lanes, out, badge: all.filter((l) => l.fault).length, first: problem?.id ?? "direct", offline };
}

/** The doctor's ipv6 check (internal/node/doctor checkIPv6) in a word: ipv6=no|yes, connect=ok|failed. */
export function ipv6FactOf(items: readonly { id: string; params: Record<string, string> }[] | undefined): Ipv6Fact {
  const p = items?.find((i) => i.id === "ipv6")?.params;
  if (p?.ipv6 === "no") return "none";
  if (p?.ipv6 === "yes") return p.connect === "ok" ? "ok" : p.connect === "failed" ? "broken" : "unknown";
  return "unknown";
}

/** A ?way= value of the URL, kept only in its known shapes. */
export const isWayId = (s: unknown): s is WayId => typeof s === "string" && (s === "direct" || s === "ipv6" || /^(via|out):[\w-]+$/.test(s));
