import { describe, expect, it } from "vitest";
import { InboundState, NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { findNodes, fleetMap, relayPairRefusal, type FleetNodeIn, type FmVerdict } from "./fleet-map";
import type { RelayMsg } from "./relay";

const node = (name: string, over: Partial<FleetNodeIn> = {}): FleetNodeIn => ({
  id: `n_${name}`, name, countryCode: "DE", location: "", status: NodeStatus.ONLINE, protocols: ["hysteria2", "vless"], online: 3, cpu: 20, warp: WarpState.NOT_CONFIGURED, ...over,
});
const relay = (id: string, via: string, exit: string, over: Partial<RelayMsg> = {}) =>
  ({ id, inboundId: `i_${id}`, viaNodeId: `n_${via}`, viaNodeName: via, exitNodeId: `n_${exit}`, exitNodeName: exit, network: "udp", port: 443, enabled: true, state: InboundState.ACTIVE, ...over }) as RelayMsg;
const verdicts = (o: Record<string, FmVerdict> = {}) => new Map(Object.entries(o).map(([k, v]) => [`n_${k}`, v]));

// the owner's shape: ru and ru2 are entries (no servers of their own) relaying to be and tr
const fleet = [
  node("de"), node("nl"), node("be"), node("tr", { countryCode: "TR" }),
  node("ru", { countryCode: "RU", protocols: [], online: 0 }),
  node("ru2", { countryCode: "RU", protocols: [] }),
  node("fresh", { status: NodeStatus.PENDING, protocols: [], online: 0, cpu: null }),
  node("old", { status: NodeStatus.RETIRED }),
];
const relays = [relay("r1", "ru", "be"), relay("r2", "ru", "be", { inboundId: "i_x" }), relay("r3", "ru2", "tr"), relay("r4", "ru2", "be", { state: InboundState.FAILED })];

describe("fleetMap: roles and columns", () => {
  const m = fleetMap({ nodes: fleet, relays, verdicts: verdicts({ de: "ok", nl: "partial", be: "blocked", tr: "blocked", ru: "ok", ru2: "ok" }), country: "RU" });
  const by = Object.fromEntries(m.nodes.map((n) => [n.nodeId, n]));

  it("leaves retired nodes out and gives every other a card", () => {
    expect(m.nodes.map((n) => n.name).sort()).toEqual(["be", "de", "fresh", "nl", "ru", "ru2", "tr"]);
    expect(m.people).toEqual({ country: "RU" });
  });
  it("tells an entry-only node from one that serves and from a fresh one", () => {
    expect(by.n_ru).toMatchObject({ role: "entry", layer: 1, relaysOut: 2 });
    expect(by.n_ru2).toMatchObject({ role: "entry", layer: 1, relaysOut: 2 });
    expect(by.n_be).toMatchObject({ role: "serve", layer: 3, relaysIn: 3 });
    expect(by.n_de).toMatchObject({ role: "serve", relaysOut: 0, relaysIn: 0 });
    expect(by.n_fresh).toMatchObject({ role: "idle", status: "pending" });
  });
  it("a node that both serves and relays is in the middle column", () => {
    const x = fleetMap({ nodes: [node("a"), node("b")], relays: [relay("r", "a", "b")], verdicts: verdicts(), country: "RU" });
    expect(x.nodes.find((n) => n.name === "a")).toMatchObject({ role: "mixed", layer: 2 });
  });
  it("carries the status, the load and the people online for the card", () => {
    expect(by.n_de).toMatchObject({ status: "online", load: 20, online: 3, verdict: "ok" });
    expect(by.n_fresh!.load).toBeNull();
    expect(fleetMap({ nodes: [node("x", { status: NodeStatus.BLIP }), node("y", { status: NodeStatus.DOWN })], relays: [], verdicts: verdicts(), country: "RU" }).nodes.map((n) => n.status)).toEqual(["blip", "offline"]);
  });
});

describe("fleetMap: wires", () => {
  const m = fleetMap({ nodes: fleet, relays, verdicts: verdicts({ de: "ok", nl: "partial", be: "blocked", tr: "blocked", ru: "ok", ru2: "ok" }), country: "RU" });
  const wire = (id: string) => m.wires.find((w) => w.id === id)!;

  it("draws people to each node by the country's verdict: flowing, amber, or faint", () => {
    expect(wire("people:n_de")).toMatchObject({ kind: "people", style: "flow", from: "people", to: "node:n_de" });
    expect(wire("people:n_nl").style).toBe("partial");
    expect(wire("people:n_be").style).toBe("muted");
    expect(m.wires.some((w) => w.id === "people:n_fresh")).toBe(false);
  });
  it("draws one wire per pair a relay joins, with the number of relays and the state", () => {
    expect(wire("relay:n_ru:n_be")).toMatchObject({ kind: "relay", count: 2, style: "flow", label: { key: "fm.wire.relays", n: 2 }, from: "node:n_ru", to: "node:n_be" });
    expect(wire("relay:n_ru2:n_tr")).toMatchObject({ count: 1, style: "flow" });
    // a failing relay is the one rose wire
    expect(wire("relay:n_ru2:n_be")).toMatchObject({ style: "fail", state: "bad" });
    expect(m.wires.filter((w) => w.kind === "relay")).toHaveLength(3);
  });
  it("a pair with some relays down is amber, with all switched off faint", () => {
    const x = fleetMap({ nodes: [node("a", { protocols: [] }), node("b")], relays: [relay("1", "a", "b"), relay("2", "a", "b", { state: InboundState.FAILED })], verdicts: verdicts(), country: "RU" });
    expect(x.wires.find((w) => w.kind === "relay")).toMatchObject({ style: "partial", state: "partial" });
    const off = fleetMap({ nodes: [node("a", { protocols: [] }), node("b")], relays: [relay("1", "a", "b", { enabled: false, state: InboundState.PENDING })], verdicts: verdicts(), country: "RU" });
    expect(off.wires.find((w) => w.kind === "relay")!.style).toBe("muted");
  });
  it("sends the serving nodes to the internet, and to WARP where they have it; entry-only nodes have no exit", () => {
    expect(m.exits).toEqual(["internet"]);
    expect(wire("exit:n_be:internet")).toMatchObject({ from: "node:n_be", to: "exit:internet", style: "plain" });
    expect(m.wires.some((w) => w.id === "exit:n_ru:internet")).toBe(false);
    const w = fleetMap({ nodes: [node("a", { warp: WarpState.UP }), node("b")], relays: [], verdicts: verdicts(), country: "RU" });
    expect(w.exits).toEqual(["internet", "warp"]);
    expect(w.wires.filter((x) => x.to === "exit:warp").map((x) => x.from)).toEqual(["node:n_a"]);
  });
  it("an offline node has no flowing wire", () => {
    const x = fleetMap({ nodes: [node("a", { status: NodeStatus.DOWN })], relays: [], verdicts: verdicts({ a: "ok" }), country: "RU" });
    expect(x.wires.every((w) => w.style === "muted")).toBe(true);
  });
  it("ignores a relay to or from a retired node", () => {
    const x = fleetMap({ nodes: fleet, relays: [relay("z", "ru", "old")], verdicts: verdicts(), country: "RU" });
    expect(x.wires.some((w) => w.kind === "relay")).toBe(false);
  });
});

describe("relay refusals and search", () => {
  it("refuses the same node, a retired one, and an exit with nothing deployed", () => {
    expect(relayPairRefusal("n_de", "n_de", fleet)).toBe("same_node");
    expect(relayPairRefusal("n_ru", "n_old", fleet)).toBe("retired");
    expect(relayPairRefusal("n_ru", "n_fresh", fleet)).toBe("no_servers");
    expect(relayPairRefusal("n_ru", "n_be", fleet)).toBeUndefined();
    expect(relayPairRefusal("n_ru", "n_missing", fleet)).toBe("retired");
  });
  it("finds nodes by the start of their name first, then by what they contain", () => {
    const ns = [{ id: "1", name: "mass" }, { id: "2", name: "de2" }, { id: "3", name: "de" }, { id: "4", name: "nde" }];
    expect(findNodes(ns, "de")).toEqual(["2", "3", "4"]);
    expect(findNodes(ns, " MA ")).toEqual(["1"]);
    expect(findNodes(ns, "")).toEqual([]);
    expect(findNodes(ns, "zz")).toEqual([]);
  });
});
