import { describe, expect, it } from "vitest";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import type { CountryCheck } from "./health";
import type { RelayMsg } from "./relay";
import { routeLayout, routeMap, serverDot } from "./route-map";
import { lanesOf, type Ipv6Entry, type Ipv6Fact } from "./ways";
import type { GraphInput } from "./ways-graph";

const country = (verdict: CountryVerdict, extra: object = {}) => ({ nodeId: "", verdict, atUnix: 100, probes: [], vantages: [], ...extra }) as unknown as CountryCheck;
const inbound = (id: string, protocol: string, port: number, over: Partial<GraphInput["inbounds"][number]> = {}) => ({
  id, profileName: id.toUpperCase(), protocol, port, state: InboundState.ACTIVE, enabled: true, egress: "direct", cdn: false, directInSubscriptions: true, ...over,
});
const relay = (over: Partial<RelayMsg>) =>
  ({ id: "r1", inboundId: "hy2", exitNodeId: "be", exitNodeName: "be", profileName: "HY2", viaNodeId: "ru", viaNodeName: "ru", network: "udp", port: 443, enabled: true, state: InboundState.ACTIVE, ...over }) as RelayMsg;
const v6 = (over: Partial<Ipv6Entry> = {}): Ipv6Entry => ({ entry: "", entrySupported: true, unavailableReason: "", addresses: [], src: "", ...over });

type Opts = {
  nodeId?: string;
  status?: NodeStatus;
  inbounds?: GraphInput["inbounds"];
  relays?: RelayMsg[];
  verdict?: CountryVerdict;
  fact?: Ipv6Fact;
  v6?: Ipv6Entry;
  entry?: "on" | "down";
  entryVerdict?: CountryVerdict;
  warp?: boolean;
};
function map(o: Opts = {}) {
  const nodeId = o.nodeId ?? "be";
  const inbounds = o.inbounds ?? [inbound("hy2", "hysteria2", 443), inbound("vless", "vless", 8443)];
  const ways = lanesOf({
    node: { id: nodeId, status: o.status ?? NodeStatus.ONLINE },
    inbounds,
    relays: o.relays ?? [],
    country: { ...country(o.verdict ?? CountryVerdict.BLOCKED, { dark: [] }) } as CountryCheck,
    countryOf: () => country(o.entryVerdict ?? CountryVerdict.OK),
    ipv6: o.fact,
    hasIpv6: o.fact === "ok" || o.fact === "broken",
    entry: o.entry,
  });
  return routeMap({ node: { id: nodeId, name: nodeId, address: "203.0.113.4", countryCode: "BE" }, inbounds, ways, country: "RU", warp: o.warp ?? false, ipv6: o.fact ? { fact: o.fact, v6: o.v6 ?? v6() } : undefined });
}
const by = (m: ReturnType<typeof map>) => Object.fromEntries(m.ways.map((w) => [w.id, w]));
const wire = (m: ReturnType<typeof map>, id: string) => m.wires.find((w) => w.id === id)!;

describe("routeMap: the be example", () => {
  const inbounds = [
    inbound("hy2", "hysteria2", 443),
    inbound("vless", "vless", 8443),
    inbound("xhttp", "vless", 4443),
    inbound("awg", "awg", 14491),
    inbound("awg3", "awg", 14492),
    inbound("mtproto", "mtproto", 443),
    inbound("trj", "vless", 9443, { enabled: false, state: InboundState.DISABLED }),
  ];
  const relays = [relay({ id: "r1", inboundId: "hy2" }), relay({ id: "r2", inboundId: "vless", profileName: "VLESS TCP", network: "tcp", port: 8443 }), relay({ id: "r3", inboundId: "xhttp", profileName: "VLESS XHTTP", network: "tcp", port: 4443 })];
  const m = map({ inbounds, relays, fact: "broken", v6: v6({ entrySupported: false, unavailableReason: "ipv6_broken" }) });

  it("has four columns' cards: people, three ways, the node, the internet", () => {
    expect(m.people).toEqual({ country: "RU" });
    expect(m.ways.map((w) => w.id)).toEqual(["way:direct", "way:via:ru", "way:ipv6"]);
    expect(m.hasForwards).toBe(false);
    expect(m.node.name).toBe("be");
    expect(m.node.online).toBe(true);
    expect(m.exits.map((e) => [e.id, e.count])).toEqual([["internet", 6]]);
  });

  it("says each way's state: direct blocked in the country, the relays working, IPv6 broken", () => {
    const w = by(m);
    expect(w["way:direct"]).toMatchObject({ state: "blocked", badge: { key: "rm.badge.blocked", params: { cc: "RU" } }, sub: { key: "rm.sub.direct", n: 6, params: { address: "203.0.113.4" } } });
    expect(w["way:via:ru"]).toMatchObject({ state: "ok", badge: { key: "rm.badge.ok" }, sub: { key: "rm.sub.via.all", n: 3 }, count: 3 });
    expect(w["way:ipv6"]).toMatchObject({ state: "bad", sub: { key: "rm.sub.v6.broken" } });
  });

  it("explains a cut or broken way by the ones that work", () => {
    const w = by(m);
    expect(w["way:direct"]!.consequence).toEqual({ kind: "blocked", alt: [{ kind: "via", name: "ru" }] });
    expect(w["way:ipv6"]!.consequence).toEqual({ kind: "bad", alt: [{ kind: "via", name: "ru" }] });
    expect(w["way:via:ru"]!.consequence).toBeUndefined();
  });

  it("carries the relayed servers as chips on the entry card and all the servers on the node's, 6 running and 1 off", () => {
    expect(by(m)["way:via:ru"]!.chips.map((c) => [c.label, c.state])).toEqual([["HY2", "ok"], ["VLESS TCP", "ok"], ["VLESS XHTTP", "ok"]]);
    const dots = m.node.chips.map((c) => c.state);
    expect(dots.filter((d) => d === "ok")).toHaveLength(6);
    expect(dots.filter((d) => d === "off")).toHaveLength(1);
    expect(m.node.chips.find((c) => c.inboundId === "hy2")).toMatchObject({ port: 443, net: "udp" });
  });

  it("draws one wire per way, with the servers carried as its weight and a pill on the entry's", () => {
    expect(wire(m, "people:way:direct")).toMatchObject({ style: "muted", count: 6 });
    expect(wire(m, "people:way:via:ru")).toMatchObject({ style: "flow", count: 3 });
    expect(wire(m, "way:via:ru:node")).toMatchObject({ style: "flow", count: 3, label: { key: "rm.wire.n", n: 3 } });
    expect(wire(m, "way:direct:node").label).toBeUndefined();
    expect(wire(m, "node:exit:internet")).toMatchObject({ style: "flow", count: 6 });
    expect(m.wires.filter((w) => w.from === "people")).toHaveLength(3);
  });

  it("a failing relay is the one rose wire; a way that is partly seen is amber", () => {
    const failing = map({ inbounds, relays: [relay({ id: "r1", state: InboundState.FAILED }), relay({ id: "r2", inboundId: "vless", state: InboundState.FAILED })] });
    expect(by(failing)["way:via:ru"]).toMatchObject({ state: "bad", badge: { key: "rm.badge.bad" } });
    expect(wire(failing, "people:way:via:ru").style).toBe("fail");
    expect(by(failing)["way:via:ru"]!.chips.map((c) => c.state)).toEqual(["bad", "bad"]);
    const part = map({ inbounds, relays, entryVerdict: CountryVerdict.PARTIAL });
    expect(by(part)["way:via:ru"]).toMatchObject({ state: "partial", sub: { key: "rm.sub.via.part" } });
    expect(wire(part, "way:via:ru:node").style).toBe("partial");
  });

  it("the servers' dots: running, failed, off, kept out of subscriptions", () => {
    const x = map({ inbounds: [inbound("a", "vless", 1), inbound("b", "vless", 2, { state: InboundState.FAILED }), inbound("c", "vless", 3, { enabled: false, state: InboundState.DISABLED }), inbound("d", "vless", 4, { directInSubscriptions: false })] });
    expect(Object.fromEntries(x.node.chips.map((c) => [c.inboundId, c.state]))).toEqual({ a: "ok", b: "bad", c: "off", d: "off" });
    expect(serverDot(x.node.chips[0]!.server!)).toBe("ok");
  });
});

describe("routeMap: a viewer without the relays", () => {
  it("says no consequence: it cannot know the other ways", () => {
    const base = map();
    const m = routeMap({ ...(base as never as Parameters<typeof routeMap>[0]), node: { id: "be", name: "be" }, inbounds: [], ways: lanesOf({ node: { id: "be", status: NodeStatus.ONLINE }, inbounds: [inbound("a", "vless", 1)], country: country(CountryVerdict.BLOCKED) }), country: "RU", warp: false, relaysKnown: false });
    expect(m.ways[0]).toMatchObject({ state: "blocked" });
    expect(m.ways[0]!.consequence).toBeUndefined();
  });
});

describe("routeMap: IPv6 states", () => {
  it("works, can be turned on, broken, on but broken; none here has no card", () => {
    const on = map({ fact: "ok", entry: "on", v6: v6({ entry: "2001:db8::7" }), verdict: CountryVerdict.OK });
    expect(by(on)["way:ipv6"]).toMatchObject({ state: "ok", sub: { key: "rm.sub.v6.on", params: { addr: "2001:db8::7" } } });
    const can = map({ fact: "ok", v6: v6(), verdict: CountryVerdict.OK });
    expect(by(can)["way:ipv6"]).toMatchObject({ state: "off", badge: { key: "rm.badge.off" }, sub: { key: "rm.sub.v6.can" } });
    expect(can.canAddIpv6).toBe(true);
    const down = map({ fact: "broken", entry: "down", v6: v6({ entry: "2001:db8::7", unavailableReason: "ipv6_broken" }) });
    expect(by(down)["way:ipv6"]).toMatchObject({ state: "bad", sub: { key: "rm.sub.v6.on_down" } });
    const none = map({ fact: "none", v6: v6({ entrySupported: false, unavailableReason: "no_ipv6" }) });
    expect(by(none)["way:ipv6"]).toBeUndefined();
    expect(none.canAddIpv6).toBe(false);
  });
});

describe("routeMap: a node with a CDN", () => {
  const m = map({ verdict: CountryVerdict.OK, inbounds: [inbound("hy2", "hysteria2", 443), inbound("cdn1", "vless", 2053, { cdn: true }), inbound("cdn2", "vless", 2083, { cdn: true })] });
  it("adds a CDN way carrying the CDN servers as chips", () => {
    expect(m.ways.map((w) => w.id)).toEqual(["way:direct", "way:cdn"]);
    expect(by(m)["way:cdn"]).toMatchObject({ state: "ok", sub: { key: "rm.sub.cdn", n: 2 }, count: 2 });
    expect(by(m)["way:cdn"]!.chips.map((c) => c.label)).toEqual(["CDN1", "CDN2"]);
    expect(wire(m, "way:cdn:node")).toMatchObject({ style: "flow", count: 2, label: { key: "rm.wire.n", n: 2 } });
  });
  it("has no CDN way without CDN servers", () => {
    expect(map().ways.some((w) => w.kind === "cdn")).toBe(false);
  });
});

describe("routeMap: an entry node", () => {
  const out = [
    relay({ id: "o1", exitNodeId: "be", exitNodeName: "be", viaNodeId: "ru", viaNodeName: "ru" }),
    relay({ id: "o2", inboundId: "x", exitNodeId: "de1", exitNodeName: "de1", viaNodeId: "ru", viaNodeName: "ru", state: InboundState.FAILED }),
    relay({ id: "o3", inboundId: "y", exitNodeId: "de1", exitNodeName: "de1", viaNodeId: "ru", viaNodeName: "ru" }),
  ];
  const m = map({ nodeId: "ru", inbounds: [], relays: out });
  it("has a fifth column of the nodes it forwards to, with the relayed servers and the state", () => {
    expect(m.hasForwards).toBe(true);
    expect(m.forwards.map((f) => [f.id, f.state, f.count])).toEqual([["fwd:be", "ok", 1], ["fwd:de1", "partial", 2]]);
    expect(m.forwards[1]!.chips.map((c) => c.state)).toEqual(["bad", "ok"]);
    expect(m.forwards[0]!.sub).toEqual({ key: "rm.sub.fwd", n: 1 });
  });
  it("draws a wire from the node to each of them", () => {
    expect(wire(m, "node:fwd:be")).toMatchObject({ from: "node", to: "fwd:be", style: "flow" });
    expect(wire(m, "node:fwd:de1")).toMatchObject({ style: "partial", count: 2 });
  });
});

describe("routeMap: WARP", () => {
  it("shows WARP only when a server leaves through it, and the internet only when one leaves directly (or nothing does)", () => {
    const both = map({ verdict: CountryVerdict.OK, inbounds: [inbound("a", "vless", 1), inbound("b", "vless", 2, { egress: "warp" }), inbound("c", "vless", 3, { egress: "warp" })] });
    expect(both.exits.map((e) => [e.id, e.count])).toEqual([["internet", 1], ["warp", 2]]);
    expect(both.exits[1]!.sub).toEqual({ key: "rm.exit.warp", n: 2 });
    const onlyWarp = map({ verdict: CountryVerdict.OK, inbounds: [inbound("b", "vless", 2, { egress: "warp" })] });
    expect(onlyWarp.exits.map((e) => e.id)).toEqual(["warp"]);
    expect(map({ verdict: CountryVerdict.OK }).exits.map((e) => e.id)).toEqual(["internet"]);
  });
});

describe("routeMap: everything off, and an offline node", () => {
  it("with every server off, nothing is carried and every wire is a quiet dotted line", () => {
    const m = map({ inbounds: [inbound("a", "vless", 1, { enabled: false, state: InboundState.DISABLED }), inbound("b", "vless", 2, { enabled: false, state: InboundState.DISABLED })] });
    expect(by(m)["way:direct"]).toMatchObject({ state: "off", count: 0 });
    expect(m.node.chips.every((c) => c.state === "off")).toBe(true);
    expect(m.exits).toEqual([{ id: "internet", count: 0, sub: { key: "rm.exit.internet", params: { node: "be" }, n: 0 } }]);
    expect(m.wires.every((w) => w.style === "muted")).toBe(true);
  });
  it("an offline node: its ways are off, it says so, and no wire flows", () => {
    const m = map({ status: NodeStatus.DOWN, verdict: CountryVerdict.OK, relays: [relay({})] });
    expect(m.node.online).toBe(false);
    expect(by(m)["way:direct"]).toMatchObject({ state: "off", sub: { key: "rm.sub.offline" } });
    expect(m.wires.every((w) => w.style === "muted")).toBe(true);
  });
});

describe("routeLayout", () => {
  const cards = [
    { id: "col:0", w: 120, h: 14 }, { id: "col:1", w: 120, h: 14 }, { id: "col:2", w: 120, h: 14 }, { id: "col:3", w: 120, h: 14 },
    { id: "people", w: 180, h: 80 },
    { id: "way:direct", w: 236, h: 90 }, { id: "way:via:ru", w: 236, h: 130 }, { id: "way:ipv6", w: 236, h: 70 }, { id: "ghost:way", w: 236, h: 46 },
    { id: "node", w: 250, h: 150 },
    { id: "exit:internet", w: 200, h: 70 }, { id: "ghost:exit", w: 200, h: 46 },
  ];
  const pos = routeLayout(cards);
  const rect = (id: string) => ({ ...pos[id]!, ...cards.find((c) => c.id === id)! });
  it("puts the columns left to right, the headers on one line above everything, and the headers over their columns", () => {
    expect(pos.people!.x).toBeLessThan(pos["way:direct"]!.x);
    expect(pos["way:direct"]!.x).toBeLessThan(pos.node!.x);
    expect(pos.node!.x).toBeLessThan(pos["exit:internet"]!.x);
    expect(pos["col:1"]!.x).toBe(pos["way:direct"]!.x);
    expect(pos["col:3"]!.x).toBe(pos["exit:internet"]!.x);
    expect(new Set(["col:0", "col:1", "col:2", "col:3"].map((id) => pos[id]!.y)).size).toBe(1);
    expect(pos["col:0"]!.y).toBeLessThan(Math.min(...Object.entries(pos).filter(([id]) => !id.startsWith("col:")).map(([, p]) => p.y)));
  });
  it("centres the columns on one middle line and never overlaps cards", () => {
    const mid = (id: string) => rect(id).y + rect(id).h / 2;
    expect(mid("people")).toBeCloseTo(mid("node"), 5);
    for (const a of cards.filter((c) => !c.id.startsWith("col:")))
      for (const b of cards.filter((c) => !c.id.startsWith("col:"))) {
        if (a.id >= b.id) continue;
        const p = rect(a.id);
        const q = rect(b.id);
        expect(p.x < q.x + q.w && p.x + p.w > q.x && p.y < q.y + q.h && p.y + p.h > q.y, `${a.id} ${b.id}`).toBe(false);
      }
  });
  it("adds the forwards column on an entry node, below the exits (so the wires to them do not cross)", () => {
    const p = routeLayout([...cards, { id: "col:4", w: 120, h: 14 }, { id: "fwd:be", w: 220, h: 100 }, { id: "fwd:de1", w: 220, h: 120 }]);
    expect(p["fwd:be"]!.x).toBeGreaterThan(p["exit:internet"]!.x);
    expect(p["col:4"]!.x).toBe(p["fwd:be"]!.x);
    expect(p["fwd:de1"]!.y).toBeGreaterThan(p["fwd:be"]!.y);
    expect(p["fwd:be"]!.y).toBeGreaterThan(p["exit:internet"]!.y);
  });
});
