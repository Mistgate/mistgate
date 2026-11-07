import { describe, expect, it } from "vitest";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import type { CountryCheck } from "./health";
import type { RelayMsg } from "./relay";
import { lanesOf } from "./ways";
import { networkOf, port, relayRefusal, relayWire, waysGraph, type GraphInput, type GServer } from "./ways-graph";

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

function graph(over: { inbounds?: GraphInput["inbounds"]; relays?: RelayMsg[]; nodeId?: string; warp?: boolean; verdict?: CountryVerdict; extra?: GraphInput["extraEntries"] } = {}) {
  const nodeId = over.nodeId ?? "be";
  const inbounds = over.inbounds ?? [inbound("hy2", "hysteria2", 443), inbound("vless", "vless", 8443), inbound("awg", "awg", 14491, { egress: "warp" })];
  const ways = lanesOf({ node: { id: nodeId, status: NodeStatus.ONLINE }, inbounds, relays: over.relays ?? [], country: country(over.verdict ?? CountryVerdict.BLOCKED), countryOf: () => country(CountryVerdict.OK) });
  return waysGraph({ node: { id: nodeId, name: nodeId }, inbounds, ways, country: "RU", warp: over.warp ?? false, extraEntries: over.extra });
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
  const server = (over: Partial<GServer> = {}): GServer => ({ inboundId: "hy2", name: "HY2", protocol: "hysteria2", net: "udp", port: 443, egress: "direct", listed: true, cdn: false, on: true, failed: false, ...over });
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
