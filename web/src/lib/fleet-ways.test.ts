import { describe, expect, it } from "vitest";
import { InboundState, NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { directOf, filterExits, fleetWays, sortExits, type FleetNodeIn, type WaysIn } from "./fleet-map";
import type { CountryCheck } from "./health";
import type { RelayMsg } from "./relay";

// The ways out of the country, node by node: what the list's "From Russia" column and the Paths view say is decided here.

const NOW = 1_800_000_000;
const id = (n: string) => `n_${n}`;
const node = (name: string, over: Partial<FleetNodeIn> = {}): FleetNodeIn => ({
  id: id(name), name, countryCode: "DE", location: "", status: NodeStatus.ONLINE, protocols: ["hysteria2", "vless"], online: 1, cpu: 10, warp: WarpState.NOT_CONFIGURED, ...over,
});
// an entry has no servers of its own
const entryNode = (name: string, over: Partial<FleetNodeIn> = {}) => node(name, { countryCode: "RU", protocols: [], ...over });
const check = (name: string, verdict: CountryVerdict, dark: string[] = [], at = NOW - 300) =>
  ({ nodeId: id(name), country: "RU", verdict, atUnix: at, probes: [], vantages: [...dark.map((d) => ({ state: "dark", network: d, key: d })), { state: "reach", network: "Operator B", key: "Operator B" }] }) as unknown as CountryCheck;
const closed = (name: string) => check(name, CountryVerdict.BLOCKED, ["Operator A", "Operator B"]);
const open = (name: string) => check(name, CountryVerdict.OK);
const part = (name: string) => check(name, CountryVerdict.PARTIAL, ["Operator A"]);
const relay = (rid: string, via: string, exit: string, over: Record<string, unknown> = {}) =>
  ({ id: rid, inboundId: `i_${rid}`, viaNodeId: id(via), viaNodeName: via, exitNodeId: id(exit), exitNodeName: exit, network: "tcp", port: 443, enabled: true, state: InboundState.ACTIVE, agentSupported: true, ...over }) as unknown as RelayMsg;
// a relay whose leg failed and nothing carries: it is down
const down = (rid: string, via: string, exit: string, over: Record<string, unknown> = {}) => relay(rid, via, exit, { check: { ok: false, stage: "leg", leg: { status: "fail", atUnix: NOW }, atUnix: NOW }, ...over });
const bridged = (rid: string, via: string, exit: string) => relay(rid, via, exit, { mode: "auto", activeMode: "reverse", check: { ok: true, stage: "", atUnix: NOW } });

const ways = (nodes: FleetNodeIn[], checks: ReturnType<typeof check>[], relays: RelayMsg[] | undefined, over: Partial<WaysIn> = {}) =>
  fleetWays({ nodes, relays, checks: new Map(checks.map((c) => [c.nodeId, c])), country: "RU", nowS: NOW, ...over });

describe("directOf: what the country sees of a node", () => {
  it("says works, partly, closed, or nothing is known, with the networks that do not see it", () => {
    expect(directOf(open("a"))).toMatchObject({ tone: "ok", dark: [] });
    expect(directOf(part("a"))).toMatchObject({ tone: "part", dark: ["Operator A"] });
    expect(directOf(closed("a"))).toMatchObject({ tone: "bad" });
    expect(directOf(undefined)).toMatchObject({ tone: "none", dark: [] });
    expect(directOf(check("a", CountryVerdict.LISTED))).toMatchObject({ tone: "none" });
  });
});

describe("fleetWays: a node that people use", () => {
  const fleet = [node("de"), node("nl"), node("fi"), entryNode("msk")];
  const checks = [closed("de"), part("nl"), open("fi"), open("msk")];

  it("opens directly: the word is direct, with the entries it also has", () => {
    const w = ways(fleet, checks, [relay("1", "msk", "fi")]);
    expect(w.byNode.get(id("fi"))).toMatchObject({ tone: "ok", kind: "direct", noWay: false });
    expect(w.byNode.get(id("fi"))!.pairs.map((p) => p.entryName)).toEqual(["msk"]);
  });
  it("is closed and an entry carries it: bypassed, through that entry", () => {
    const w = ways(fleet, checks, [relay("1", "msk", "de"), relay("2", "msk", "de")]);
    expect(w.byNode.get(id("de"))).toMatchObject({ tone: "ok", kind: "bypass", noWay: false });
    expect(w.byNode.get(id("de"))!.pairs[0]).toMatchObject({ entryName: "msk", n: 2, ok: 2, state: "ok", bridge: 0 });
  });
  it("a relay that stands in the bridge is counted as one", () => {
    const w = ways(fleet, checks, [bridged("1", "msk", "de")]);
    expect(w.byNode.get(id("de"))!.pairs[0]!.bridge).toBe(1);
    expect(w.bridgeN).toBe(1);
  });
  it("is closed and no relay passes it: no way, and Get around is offered", () => {
    const w = ways(fleet, checks, []);
    expect(w.byNode.get(id("de"))).toMatchObject({ tone: "bad", kind: "noway", noWay: true });
    expect(w.noWayN).toBe(1);
  });
  it("some relays of the pair fail: partly, the working ones counted", () => {
    const w = ways(fleet, checks, [relay("1", "msk", "de"), down("2", "msk", "de", { network: "udp" })]);
    const de = w.byNode.get(id("de"))!;
    expect(de).toMatchObject({ tone: "part", kind: "partly", noWay: false });
    expect(de.pairs[0]).toMatchObject({ ok: 1, n: 2, down: 1, state: "warn" });
    // nl is closed on Operator A only and no relay carries it: partly as well
    expect(w.partN).toBe(2);
    expect(w.downN).toBe(1);
  });
  it("every relay of the pair fails: no way, and Get around is still offered (nothing works)", () => {
    const w = ways(fleet, checks, [down("1", "msk", "de")]);
    expect(w.byNode.get(id("de"))).toMatchObject({ tone: "bad", kind: "noway", noWay: true });
  });
  it("a relay that is turning on is no way yet, and nothing offers another while it turns on", () => {
    const w = ways(fleet, checks, [relay("1", "msk", "de", { state: InboundState.PENDING })]);
    expect(w.byNode.get(id("de"))).toMatchObject({ tone: "bad", busy: true, noWay: false });
  });
  it("opens directly on some networks only, and nothing carries it: partly, directly", () => {
    const w = ways(fleet, checks, []);
    expect(w.byNode.get(id("nl"))).toMatchObject({ tone: "part", kind: "directPart" });
  });
  it("opens on some networks and an entry carries the rest: bypassed", () => {
    const w = ways(fleet, checks, [relay("1", "msk", "nl")]);
    expect(w.byNode.get(id("nl"))).toMatchObject({ tone: "ok", kind: "bypass" });
  });
  it("an entry dark on the networks where the exit is dark does not carry them: partly, with those networks named", () => {
    const f = [node("de"), entryNode("spb")];
    const w = ways(f, [closed("de"), part("spb")], [relay("1", "spb", "de")]);
    const p = w.byNode.get(id("de"))!.pairs[0]!;
    expect(p).toMatchObject({ seen: "part", dark: ["Operator A"] });
    expect(w.byNode.get(id("de"))).toMatchObject({ tone: "part", kind: "partly" });
  });
  it("is not measured: no data, and a pair that works is a way all the same", () => {
    const w = ways(fleet, [], [relay("1", "msk", "de")]);
    expect(w.unmeasured).toBe(true);
    expect(w.byNode.get(id("fi"))).toMatchObject({ tone: "none", kind: "nodata" });
    expect(w.byNode.get(id("de"))).toMatchObject({ tone: "ok", kind: "bypass" });
  });
  it("has a node with no check and no relay at no data, not at all well", () => {
    const w = ways([node("x"), node("fi")], [open("fi")], []);
    expect(w.byNode.get(id("x"))).toMatchObject({ tone: "none", kind: "nodata" });
  });
  it("knows how old its measurement is when over a day", () => {
    const w = ways([node("de")], [check("de", CountryVerdict.BLOCKED, [], NOW - 2 * 86400 - 60)], []);
    expect(w.byNode.get(id("de"))!.staleDays).toBe(2);
    expect(ways([node("de")], [closed("de")], []).byNode.get(id("de"))!.staleDays).toBe(0);
  });
});

describe("fleetWays: an entry that does not work", () => {
  const exits = [node("de"), node("fi")];
  it("an entry offline: its exits have no way, said once at the entry, and no Get around for them", () => {
    const f = [...exits, entryNode("msk", { status: NodeStatus.DOWN })];
    const w = ways(f, [closed("de"), open("fi"), open("msk")], [relay("1", "msk", "de")]);
    expect(w.byNode.get(id("msk"))).toMatchObject({ tone: "bad", kind: "entry", entry: { offline: true, hidden: false, passes: 1 } });
    expect(w.byNode.get(id("de"))).toMatchObject({ tone: "bad", kind: "deadEntry", noWay: false, deadBy: { entry: "msk", why: "offline" } });
    expect(w.byNode.get(id("de"))!.pairs[0]!.dead).toBe("offline");
  });
  it("an entry the country does not see: hidden", () => {
    const f = [...exits, entryNode("msk")];
    const w = ways(f, [closed("de"), open("fi"), closed("msk")], [relay("1", "msk", "de")]);
    expect(w.byNode.get(id("msk"))).toMatchObject({ tone: "bad", entry: { hidden: true, offline: false } });
    expect(w.byNode.get(id("de"))).toMatchObject({ kind: "deadEntry", deadBy: { why: "hidden" } });
  });
  it("a dead entry does not help an exit that has another that works", () => {
    const f = [...exits, entryNode("msk", { status: NodeStatus.DOWN }), entryNode("spb")];
    const w = ways(f, [closed("de"), open("msk"), open("spb")], [relay("1", "msk", "de"), relay("2", "spb", "de")]);
    expect(w.byNode.get(id("de"))).toMatchObject({ tone: "ok", kind: "bypass" });
    expect(w.byNode.get(id("de"))!.pairs.map((p) => p.dead)).toEqual(expect.arrayContaining(["offline", ""]));
  });
  it("an entry whose agent is old, and the exits that go through it", () => {
    const f = [...exits, entryNode("msk")];
    const w = ways(f, [closed("de"), open("msk")], [relay("1", "msk", "de", { agentSupported: false })]);
    expect(w.byNode.get(id("msk"))).toMatchObject({ tone: "part", entry: { oldAgent: true } });
  });
});

describe("fleetWays: an entry", () => {
  it("counts the exits that pass through it and the ones with a relay down", () => {
    const f = [node("de"), node("nl"), node("pl"), entryNode("msk")];
    const w = ways(f, [closed("de"), closed("nl"), closed("pl"), open("msk")], [relay("1", "msk", "de"), relay("2", "msk", "nl"), down("3", "msk", "nl"), relay("4", "msk", "pl")]);
    expect(w.byNode.get(id("msk"))).toMatchObject({ role: "entry", tone: "ok", kind: "entry", entry: { passes: 3, broken: 1, offline: false, hidden: false, oldAgent: false } });
    expect(w.entries.map((e) => e.name)).toEqual(["msk"]);
    expect(w.exits.map((e) => e.name)).toEqual(["de", "nl", "pl"]);
  });
  it("an entry that is seen only on some networks says so, and one with nothing to pass says so", () => {
    const w = ways([node("de"), entryNode("spb")], [closed("de"), part("spb")], []);
    expect(w.byNode.get(id("spb"))).toMatchObject({ tone: "part", entry: { passes: 0 }, direct: { dark: ["Operator A"] } });
  });
  it("a node with nothing deployed inside the country can take relays; elsewhere it is nothing", () => {
    const w = ways([node("de"), entryNode("msk"), node("xx", { protocols: [], countryCode: "FI" })], [closed("de"), open("msk")], []);
    expect(w.byNode.get(id("msk"))).toMatchObject({ role: "idle", kind: "entry" });
    expect(w.byNode.get(id("xx"))).toMatchObject({ kind: "idle" });
    expect(w.entries.map((e) => e.name)).toEqual(["msk"]);
  });
  it("a node that serves and passes relays on is both an entry's words and an exit's row", () => {
    const w = ways([node("a", { countryCode: "RU" }), node("b")], [open("a"), closed("b")], [relay("1", "a", "b")]);
    expect(w.byNode.get(id("a"))).toMatchObject({ role: "mixed", kind: "direct" });
    expect(w.exits.map((e) => e.name)).toEqual(["a", "b"]);
    // and its column: the pairs through it have a place in the table
    expect(w.entries.map((e) => e.name)).toEqual(["a"]);
    expect(w.byNode.get(id("a"))!.entry).toMatchObject({ passes: 1, offline: false });
  });
  it("leaves out a retired node and its relays", () => {
    const w = ways([node("de"), entryNode("msk"), node("old", { status: NodeStatus.RETIRED })], [closed("de"), open("msk")], [relay("1", "msk", "old")]);
    expect(w.byNode.has(id("old"))).toBe(false);
    expect(w.relayN).toBe(0);
  });
});

describe("fleetWays: a helper", () => {
  it("has no relays to read: what the country sees of each node, no entries, no rows", () => {
    const w = ways([node("de"), node("fi"), entryNode("msk")], [closed("de"), open("fi")], undefined);
    expect(w.byNode.get(id("de"))).toMatchObject({ tone: "bad", kind: "direct", pairs: [], noWay: false });
    expect(w.byNode.get(id("fi"))).toMatchObject({ tone: "ok" });
    expect(w.byNode.get(id("msk"))).toMatchObject({ tone: "none", kind: "nodata" });
    expect(w.entries).toEqual([]);
    expect(w.exits).toEqual([]);
  });
});

describe("fleetWays: the totals and the order of the rows", () => {
  const fleet = [node("de"), node("nl"), node("fi"), node("bb"), entryNode("msk")];
  const w = ways(fleet, [closed("de"), part("nl"), open("fi"), open("msk")], [relay("1", "msk", "bb"), bridged("2", "msk", "nl"), down("3", "msk", "nl", { network: "udp" })]);
  it("counts the exits that open, the ones with no way, partly, over the bridge, and the relays", () => {
    // fi opens; bb has no check (no data), but a relay that works; de has no way; nl is carried by one relay of two
    expect(w).toMatchObject({ reachN: 2, noWayN: 1, partN: 1, bridgeN: 1, relayN: 3, downN: 1 });
  });
  it("puts no way first, then partly, then no data, then the rest, by name inside", () => {
    expect(sortExits(w.exits).map((e) => e.name)).toEqual(["de", "nl", "bb", "fi"]);
  });
  it("filters by the view's chips", () => {
    const all = sortExits(w.exits);
    expect(filterExits(all, "all")).toHaveLength(4);
    expect(filterExits(all, "noway").map((e) => e.name)).toEqual(["de"]);
    expect(filterExits(all, "part").map((e) => e.name)).toEqual(["nl"]);
    expect(filterExits(all, "bridge").map((e) => e.name)).toEqual(["nl"]);
  });
});
