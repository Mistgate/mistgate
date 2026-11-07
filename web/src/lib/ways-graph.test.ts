import { describe, expect, it } from "vitest";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import type { CountryCheck } from "./health";
import type { RelayMsg } from "./relay";
import { lanesOf } from "./ways";
import type { Ipv6Entry } from "./ways";
import { ipv6StateOf, networkOf, port, relayRefusal, relayWire, waysGraph, waysLayout, wayOf, type GraphInput, type GServer } from "./ways-graph";

const country = (verdict: CountryVerdict) => ({ nodeId: "", verdict, atUnix: 100, probes: [], vantages: [] }) as unknown as CountryCheck;
const inbound = (id: string, protocol: string, portNo: number, over: Partial<GraphInput["inbounds"][number]> = {}) => ({
  id,
  profileName: id.toUpperCase(),
  protocol,
  port: portNo,
  state: InboundState.ACTIVE,
  enabled: true,
  egress: "direct",
  cdn: false,
  directInSubscriptions: true,
  ...over,
});
const relay = (over: Partial<RelayMsg>) =>
  ({ id: "r1", inboundId: "hy2", exitNodeId: "be", exitNodeName: "be", viaNodeId: "msk1", viaNodeName: "msk1", network: "udp", port: 443, enabled: true, state: InboundState.ACTIVE, ...over }) as RelayMsg;

function graph(over: { inbounds?: GraphInput["inbounds"]; relays?: RelayMsg[]; nodeId?: string; warp?: boolean; verdict?: CountryVerdict; extra?: GraphInput["extraEntries"]; ipv6?: GraphInput["ipv6"]; entry?: "on" | "down"; hasIpv6?: boolean; entryVerdict?: CountryVerdict } = {}) {
  const nodeId = over.nodeId ?? "be";
  const inbounds = over.inbounds ?? [inbound("hy2", "hysteria2", 443), inbound("vless", "vless", 8443), inbound("awg", "awg", 14491, { egress: "warp" })];
  const ways = lanesOf({ node: { id: nodeId, status: NodeStatus.ONLINE }, inbounds, relays: over.relays ?? [], country: { ...country(over.verdict ?? CountryVerdict.BLOCKED), ipv6: over.entryVerdict ? country(over.entryVerdict) : undefined } as CountryCheck, countryOf: () => country(over.entryVerdict ?? CountryVerdict.OK), entry: over.entry, hasIpv6: over.hasIpv6 });
  return waysGraph({ node: { id: nodeId, name: nodeId }, inbounds, ways, country: "RU", warp: over.warp ?? false, extraEntries: over.extra, ipv6: over.ipv6 });
}

describe("waysGraph", () => {
  it("lays the cards out in columns: people, entries, the node, the exits", () => {
    const g = graph({ relays: [relay({})] });
    expect(g.cards.map((c) => [c.id, c.column])).toEqual([
      ["people", 0],
      ["entry:msk1", 1],
      ["node", 2],
      ["internet", 4],
      ["warp", 4],
    ]);
    const node = g.cards.find((c) => c.kind === "node")!;
    // servers by network, then port: one in and one out each
    expect(node.kind === "node" && node.servers.map((s) => s.inboundId)).toEqual(["vless", "hy2", "awg"]);
    expect(node.ports.filter((p) => p.side === "in").map((p) => [p.id, p.type])).toEqual([
      [port.serverIn("vless"), "tcp"],
      [port.serverIn("hy2"), "udp"],
      [port.serverIn("awg"), "udp"],
    ]);
  });

  it("draws every way the list has, with the list's states", () => {
    const g = graph({ relays: [relay({}), relay({ id: "r2", inboundId: "vless", network: "tcp", port: 8443, enabled: false, state: InboundState.PENDING, check: { ok: false } as RelayMsg["check"] })] });
    const by = Object.fromEntries(g.wires.map((w) => [w.id, w]));
    // blocked from the country: the direct ways are bad and carry nothing
    expect(by["direct:hy2"]).toMatchObject({ kind: "direct", state: "bad", flowing: false });
    // a working relay flows; one whose check failed is bad and does not
    expect(by["relay:r1"]).toMatchObject({ kind: "relay", state: "ok", flowing: true, from: { card: "entry:msk1", port: port.relay("r1") }, to: { card: "node", port: port.serverIn("hy2") } });
    expect(by["relay:r2"]).toMatchObject({ state: "bad", flowing: false });
    // people reach the entry: its country sees it
    expect(by["reach:msk1"]).toMatchObject({ state: "ok", to: { card: "entry:msk1", port: port.reach } });
    // each server leaves where its profile says
    expect(by["egress:awg"]).toMatchObject({ to: { card: "warp", port: port.exit } });
    expect(by["egress:hy2"]).toMatchObject({ to: { card: "internet", port: port.exit } });
    // every wire ends at ports that exist
    for (const w of g.wires) {
      for (const end of [w.from, w.to]) {
        const card = g.cards.find((c) => c.id === end.card);
        expect(card?.ports.some((p) => p.id === end.port), `${w.id} → ${end.card}/${end.port}`).toBe(true);
      }
    }
  });

  it("a server kept out of subscriptions or switched off has no wire from people; switched off it has no way out either", () => {
    const g = graph({ verdict: CountryVerdict.OK, inbounds: [inbound("hy2", "hysteria2", 443, { directInSubscriptions: false }), inbound("vless", "vless", 8443, { enabled: false, state: InboundState.DISABLED })] });
    const ids = g.wires.map((w) => w.id);
    expect(ids).not.toContain("direct:hy2");
    expect(ids).not.toContain("direct:vless");
    expect(ids).toContain("egress:hy2");
    expect(ids).not.toContain("egress:vless");
    // and with nothing through WARP and no WARP on the node, there is no WARP card
    expect(g.cards.some((c) => c.id === "warp")).toBe(false);
  });

  it("a CDN server's way comes from the CDN lane; it cannot be relayed", () => {
    const g = graph({ verdict: CountryVerdict.BLOCKED, inbounds: [inbound("cdn", "vless", 443, { cdn: true })] });
    expect(g.wires.find((w) => w.id === "cdn:cdn")).toMatchObject({ kind: "cdn", state: "ok" });
    const node = g.cards.find((c) => c.kind === "node");
    expect(node?.kind === "node" && node.servers[0]!.noRelay).toBe("cdn");
  });

  it("on an entry node the exits it forwards to are cards of their own", () => {
    const g = graph({ nodeId: "msk1", inbounds: [], relays: [relay({})] });
    expect(g.cards.map((c) => c.id)).toEqual(["people", "node", "exit:be", "internet"]);
    expect(g.wires.find((w) => w.id === "relay:r1")).toMatchObject({ from: { card: "node", port: port.relay("r1") }, to: { card: "exit:be", port: port.serverIn("hy2") } });
  });

  it("an entry put on the canvas by hand has no relay yet and an output to draw one", () => {
    const g = graph({ extra: [{ id: "fi1", name: "fi1" }, { id: "be", name: "be" }] });
    const entry = g.cards.find((c) => c.id === "entry:fi1");
    expect(entry?.ports.map((p) => p.id)).toEqual([port.reach, port.newRelay]);
    expect(g.wires.find((w) => w.id === "reach:fi1")).toMatchObject({ state: "unknown" });
    // the node itself is never its own entry
    expect(g.cards.some((c) => c.id === "entry:be")).toBe(false);
  });
});

describe("relay rules", () => {
  const server = (over: Partial<GServer> = {}): GServer => ({ inboundId: "hy2", name: "HY2", protocol: "hysteria2", net: "udp", port: 443, egress: "direct", listed: true, cdn: false, on: true, failed: false, way: "works", ...over });
  it("refuses what the relay dialog refuses", () => {
    expect(relayRefusal("be", "be", server(), [])).toBe("same_node");
    expect(relayRefusal("msk1", "be", server({ noRelay: "awg" }), [])).toBe("awg");
    expect(relayRefusal("msk1", "be", server(), [relay({})])).toBe("exists");
    expect(relayRefusal("ru2", "be", server(), [relay({})])).toBeUndefined();
  });
  it("networks and wire states", () => {
    expect([networkOf("hysteria2"), networkOf("awg"), networkOf("vless"), networkOf("mtproto"), networkOf("x")]).toEqual(["udp", "udp", "tcp", "tcp", undefined]);
    expect(relayWire({ enabled: true, state: InboundState.PENDING })).toBe("pending");
    expect(relayWire({ enabled: false, state: InboundState.PENDING })).toBe("off");
  });
});

const v6 = (over: Partial<Ipv6Entry> = {}): Ipv6Entry => ({ entry: "", entrySupported: true, unavailableReason: "", addresses: [], src: "", ...over });

describe("the calm states (ADR 0016 follow-up)", () => {
  it("a way the country cuts is a blocked wire, a blocked port and the server's way is 'blocked'", () => {
    const g = graph();
    const node = g.cards.find((c) => c.kind === "node")!;
    expect(node.kind === "node" && node.servers.map((s) => s.way)).toEqual(["blocked", "blocked", "blocked"]);
    expect(g.wires.find((w) => w.id === "direct:hy2")).toMatchObject({ blocked: true, flowing: false });
    expect(node.ports.find((p) => p.id === port.serverIn("hy2"))?.status).toBe("blocked");
    // the people to entry wire is blocked only when the entry's own country verdict is
    const dark = graph({ relays: [relay({})], entryVerdict: CountryVerdict.BLOCKED });
    expect(dark.wires.find((w) => w.id === "reach:msk1")).toMatchObject({ blocked: true });
    expect(graph({ relays: [relay({})] }).wires.find((w) => w.id === "reach:msk1")?.blocked).toBeUndefined();
  });

  it("works, partial, off, failed and hidden servers each have their own way and port", () => {
    const ok = graph({ verdict: CountryVerdict.OK, inbounds: [inbound("hy2", "hysteria2", 443)] });
    const part = graph({ verdict: CountryVerdict.PARTIAL, inbounds: [inbound("hy2", "hysteria2", 443)] });
    const by = (g: ReturnType<typeof graph>) => (g.cards.find((c) => c.kind === "node") as { servers: GServer[]; ports: { id: string; status?: string }[] });
    expect(by(ok).servers[0]!.way).toBe("works");
    expect(by(ok).ports.find((p) => p.id === port.serverIn("hy2"))?.status).toBe("ok");
    expect(by(part).servers[0]!.way).toBe("partial");
    expect(by(part).ports.find((p) => p.id === port.serverIn("hy2"))?.status).toBe("warn");
    const mixed = graph({
      verdict: CountryVerdict.OK,
      inbounds: [
        inbound("off", "vless", 1, { enabled: false, state: InboundState.DISABLED }),
        inbound("bad", "vless", 2, { state: InboundState.FAILED }),
        inbound("hid", "vless", 3, { directInSubscriptions: false }),
        inbound("fine", "vless", 4),
      ],
    });
    expect(Object.fromEntries(by(mixed).servers.map((s) => [s.inboundId, s.way]))).toEqual({ off: "off", bad: "failed", hid: "hidden", fine: "partial" }); // the lane warns while one of its servers has failed
    for (const id of ["off", "bad", "hid"]) expect(by(mixed).ports.find((p) => p.id === port.serverIn(id))?.status).toBe("none");
  });

  it("wayOf: an offline node's lane is off, a CDN server is never 'blocked'", () => {
    const s = { on: true, failed: false, listed: true, cdn: false };
    const lane = (over: object) => ({ ring: "ok", state: "ok", seen: "all", ...over }) as never;
    expect(wayOf(s, lane({ ring: "off", state: "off" }))).toBe("off");
    expect(wayOf(s, lane({ ring: "bad", state: "bad" }))).toBe("failed");
    expect(wayOf(s, lane({ ring: "bad", state: "ok", seen: "none" }))).toBe("blocked");
    expect(wayOf({ ...s, cdn: true }, lane({ ring: "bad", state: "ok", seen: "none" }))).toBe("partial");
    expect(wayOf(s, undefined)).toBe("unknown");
  });

  it("the IPv6 row: on and working, on but broken, can be turned on, none here, broken here, unavailable", () => {
    expect(ipv6StateOf("ok", v6({ entry: "2001:db8::1" }))).toMatchObject({ state: "on" });
    expect(ipv6StateOf("broken", v6({ entry: "2001:db8::1", unavailableReason: "ipv6_broken" }))).toMatchObject({ state: "on_down" });
    expect(ipv6StateOf("ok", v6())).toMatchObject({ state: "can" });
    expect(ipv6StateOf("none", v6({ entrySupported: false, unavailableReason: "no_ipv6" }))).toMatchObject({ state: "none", reason: "no_ipv6" });
    expect(ipv6StateOf("broken", v6({ entrySupported: false }))).toMatchObject({ state: "broken" });
    expect(ipv6StateOf("unknown", v6({ entrySupported: false, unavailableReason: "agent_old" }))).toMatchObject({ state: "unavailable", reason: "agent_old" });
  });

  it("the IPv6 row shows even for a host with none, but only an entry that is on is a wire from people", () => {
    const none = graph({ ipv6: { fact: "none", v6: v6({ entrySupported: false, unavailableReason: "no_ipv6" }) } });
    const node = none.cards.find((c) => c.kind === "node")!;
    expect(node.kind === "node" && node.v6?.state).toBe("none");
    expect(node.ports.find((p) => p.id === port.ipv6)).toMatchObject({ side: "in", status: "none" });
    expect(none.wires.some((w) => w.kind === "ipv6")).toBe(false);
    const can = graph({ ipv6: { fact: "ok", v6: v6() } });
    expect(can.wires.some((w) => w.kind === "ipv6")).toBe(false);
    const on = graph({ verdict: CountryVerdict.PARTIAL, entry: "on", hasIpv6: true, ipv6: { fact: "ok", v6: v6({ entry: "2001:db8::7" }) }, entryVerdict: CountryVerdict.OK });
    expect(on.wires.find((w) => w.kind === "ipv6")).toMatchObject({ state: "ok", flowing: true });
    expect(on.cards.find((c) => c.kind === "node")!.ports.find((p) => p.id === port.ipv6)?.status).toBe("ok");
    const down = graph({ entry: "down", hasIpv6: true, ipv6: { fact: "broken", v6: v6({ entry: "2001:db8::7", unavailableReason: "ipv6_broken" }) } });
    expect(down.wires.find((w) => w.kind === "ipv6")).toMatchObject({ state: "bad", flowing: false });
    expect(down.cards.find((c) => c.kind === "node")!.ports.find((p) => p.id === port.ipv6)?.status).toBe("warn");
  });

  it("without the IPv6 fact the row follows the lane, and a node with no lane has none", () => {
    expect(graph().cards.find((c) => c.kind === "node")?.kind === "node" && graph().cards.find((c) => c.kind === "node")).toBeTruthy();
    const nodeOf = (g: ReturnType<typeof graph>) => g.cards.find((c) => c.kind === "node") as { v6?: { state: string } };
    expect(nodeOf(graph()).v6).toBeUndefined();
    expect(nodeOf(graph({ hasIpv6: true })).v6?.state).toBe("can");
    expect(nodeOf(graph({ entry: "on" })).v6?.state).toBe("on");
  });

  it("refusals come with the reason the row shows: cdn, awg, off, exists, the same node", () => {
    const server = (over: Partial<GServer> = {}): GServer => ({ inboundId: "x", name: "X", protocol: "vless", net: "tcp", port: 1, egress: "direct", listed: true, cdn: false, on: true, failed: false, way: "works", ...over });
    expect(relayRefusal("a", "b", server({ noRelay: "cdn" }), [])).toBe("cdn");
    expect(relayRefusal("a", "b", server({ noRelay: "off" }), [])).toBe("off");
    expect(relayRefusal("a", "b", server({ noRelay: "net" }), [])).toBe("net");
    const g = graph({ inbounds: [inbound("awg", "awg", 1), inbound("cdn", "vless", 2, { cdn: true }), inbound("hy", "hysteria2", 3)] });
    const servers = (g.cards.find((c) => c.kind === "node") as { servers: GServer[] }).servers;
    expect(Object.fromEntries(servers.map((s) => [s.inboundId, s.noRelay]))).toEqual({ awg: "awg", cdn: "cdn", hy: undefined });
  });
});

describe("waysLayout", () => {
  const cards = [
    { id: "people", w: 214, h: 80 },
    { id: "entry:a", w: 214, h: 110 },
    { id: "entry:b", w: 214, h: 110 },
    { id: "entry:c", w: 214, h: 90 },
    { id: "node", w: 300, h: 280 },
    { id: "internet", w: 206, h: 70 },
    { id: "warp", w: 206, h: 70 },
  ];
  const pos = waysLayout(cards);
  const rect = (id: string) => ({ ...pos[id]!, w: cards.find((c) => c.id === id)!.w, h: cards.find((c) => c.id === id)!.h });

  it("puts the columns in order and people on the node's axis", () => {
    expect(pos.people!.x).toBeLessThan(pos["entry:a"]!.x);
    expect(pos["entry:a"]!.x).toBeLessThan(pos.node!.x);
    expect(pos.node!.x).toBeLessThan(pos.internet!.x);
    expect(pos.internet!.x).toBe(pos.warp!.x);
    expect(pos.people!.y + 40).toBe(pos.node!.y + 140);
  });

  it("keeps the entries out of the band of the node, above and below, and no two cards overlap", () => {
    for (const id of ["entry:a", "entry:b", "entry:c"]) {
      const r = rect(id);
      expect(r.y + r.h <= pos.node!.y - 28 || r.y >= pos.node!.y + 280 + 28, id).toBe(true);
    }
    expect(pos["entry:a"]!.y).toBeLessThan(pos.node!.y);
    expect(pos["entry:b"]!.y).toBeGreaterThan(pos.node!.y);
    for (const a of cards)
      for (const b of cards) {
        if (a.id >= b.id) continue;
        const p = rect(a.id);
        const q = rect(b.id);
        expect(p.x < q.x + q.w && p.x + p.w > q.x && p.y < q.y + q.h && p.y + p.h > q.y, `${a.id} ${b.id}`).toBe(false);
      }
  });

  it("collapses a column nobody is in, and on an entry node puts the exit cards before the internet", () => {
    const small = waysLayout([{ id: "people", w: 200, h: 60 }, { id: "node", w: 300, h: 200 }, { id: "internet", w: 200, h: 60 }]);
    expect(small.node!.x).toBe(200 + 144);
    const entry = waysLayout([{ id: "people", w: 200, h: 60 }, { id: "node", w: 300, h: 200 }, { id: "exit:be", w: 220, h: 120 }, { id: "internet", w: 200, h: 60 }]);
    expect(entry["exit:be"]!.x).toBeLessThan(entry.internet!.x);
  });
});
