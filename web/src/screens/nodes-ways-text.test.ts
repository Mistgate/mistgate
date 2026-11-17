import { describe, expect, it } from "vitest";
import { InboundState, NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { en as dict, type MessageKey } from "@/i18n/en";
import { fill, pickForm, type T } from "@/i18n";
import { fleetWays, type FleetNodeIn, type WayPair } from "@/lib/fleet-map";
import type { CountryCheck } from "@/lib/health";
import type { RelayMsg } from "@/lib/relay";
import { pairCell, ruCell, verdictWord, viaWord, waysSummary } from "./nodes-ways-text";

// The words of the ways: the cell of a node, a pair's cell, the summary line. The verdicts are fleetWays's (lib/fleet-ways.test.ts).

const t = Object.assign((k: MessageKey, v?: Record<string, string | number>) => fill(dict[k], v), {
  n: (k: MessageKey, n: number, v?: Record<string, string | number>) => fill(pickForm(dict[k], n, "en"), { n, ...v }),
}) as T;
const NOW = 1_800_000_000;
const stamp = (u: number) => `@${u}`;
const node = (name: string, over: Partial<FleetNodeIn> = {}): FleetNodeIn => ({ id: `n_${name}`, name, countryCode: "DE", location: "", status: NodeStatus.ONLINE, protocols: ["vless"], online: 0, cpu: null, warp: WarpState.NOT_CONFIGURED, ...over });
const check = (name: string, verdict: CountryVerdict, dark: string[] = []) =>
  ({ nodeId: `n_${name}`, country: "RU", verdict, atUnix: NOW - 60, probes: [], vantages: dark.map((d) => ({ state: "dark", network: d, key: d })) }) as unknown as CountryCheck;
const relay = (rid: string, via: string, exit: string, over: Record<string, unknown> = {}) =>
  ({ id: rid, inboundId: rid, viaNodeId: `n_${via}`, viaNodeName: via, exitNodeId: `n_${exit}`, exitNodeName: exit, network: "tcp", port: 443, enabled: true, state: InboundState.ACTIVE, agentSupported: true, ...over }) as unknown as RelayMsg;
const pairOf = (relays: RelayMsg[]): WayPair => {
  const w = fleetWays({
    nodes: [node("de"), node("msk", { protocols: [], countryCode: "RU" })],
    relays,
    checks: new Map([check("de", CountryVerdict.BLOCKED, ["Operator A"]), check("msk", CountryVerdict.OK)].map((c) => [c.nodeId, c])),
    country: "RU",
    nowS: NOW,
  });
  return w.byNode.get("n_de")!.pairs[0]!;
};
const bridgeRelay = (rid: string, bridge: Record<string, unknown>, over: Record<string, unknown> = {}) =>
  relay(rid, "msk", "de", { mode: "auto", activeMode: "reverse", modeChangedUnix: NOW - 3600, bridge: { connected: true, conns: 2, pool: 2, clients: 10, capacity: 230, nearFull: false, error: "", ...bridge }, check: { ok: true, atUnix: NOW }, ...over });
const failing = { ok: false, stage: "leg", leg: { status: "fail" } };

describe("a pair's cell", () => {
  it("forwarding that works: how many relays", () => {
    expect(pairCell(t, pairOf([relay("1", "msk", "de"), relay("2", "msk", "de")]), "de", { stamp })).toEqual({ tone: "ok", word: "forwarding", sub: "2 relays" });
    expect(pairCell(t, pairOf([relay("1", "msk", "de")]), "de", { stamp }).sub).toBe("1 relay");
  });
  it("a bridge says since when, nearly full, or at its limit", () => {
    const at = (b: Record<string, unknown>) => pairCell(t, pairOf([bridgeRelay("1", b)]), "de", { stamp });
    expect(at({})).toEqual({ tone: "ok", word: "bridge", sub: `1 relay · since @${NOW - 3600}` });
    expect(at({ clients: 190, nearFull: true }).sub).toBe("1 relay · almost full");
    expect(at({ clients: 230 })).toMatchObject({ tone: "part", sub: "1 relay · bridge at its limit" });
    expect(at({ error: "stream_unavailable", clients: 100 })).toMatchObject({ tone: "part" });
  });
  it("a bridge for some servers and forwarding for the rest says TCP", () => {
    const p = pairOf([bridgeRelay("1", {}), relay("2", "msk", "de")]);
    expect(pairCell(t, p, "de", { stamp })).toEqual({ tone: "ok", word: "bridge", sub: "2 relays · TCP" });
  });
  it("some relays down: how many run and why the first one does not", () => {
    const p = pairOf([relay("1", "msk", "de"), relay("2", "msk", "de", { network: "udp", check: failing })]);
    expect(pairCell(t, p, "de", { stamp })).toEqual({ tone: "part", word: "1 of 2 relays", sub: "msk → de fails (UDP)" });
  });
  it("all down: not working, and the stage that failed; a bridge that is not held says so", () => {
    expect(pairCell(t, pairOf([relay("1", "msk", "de", { check: { ok: false, stage: "path", path: { status: "fail" } } })]), "de", { stamp })).toEqual({ tone: "bad", word: "not working", sub: "from outside through msk fails" });
    const dropped = bridgeRelay("1", { connected: false, error: "connect" }, { check: { ok: false, stage: "bridge", bridge: { status: "fail" } } });
    expect(pairCell(t, pairOf([dropped]), "de", { stamp })).toMatchObject({ tone: "bad", sub: "the bridge is down" });
  });
  it("turning on and switched off", () => {
    expect(pairCell(t, pairOf([relay("1", "msk", "de", { state: InboundState.PENDING })]), "de", { stamp })).toMatchObject({ tone: "busy", word: "turning on…" });
    expect(pairCell(t, pairOf([relay("1", "msk", "de", { enabled: false })]), "de", { stamp })).toMatchObject({ tone: "off", word: "off", sub: "1 relay" });
  });
});

describe("the words of a node", () => {
  const model = (relays: RelayMsg[] | undefined) =>
    fleetWays({
      nodes: [node("de"), node("nl"), node("msk", { protocols: [], countryCode: "RU" }), node("spb", { protocols: [], countryCode: "RU" })],
      relays,
      checks: new Map([check("de", CountryVerdict.BLOCKED, ["Operator A"]), check("nl", CountryVerdict.PARTIAL, ["Operator A"]), check("msk", CountryVerdict.OK), check("spb", CountryVerdict.OK)].map((c) => [c.nodeId, c])),
      country: "RU",
      nowS: NOW,
    });
  const cell = (m: ReturnType<typeof model>, n: string, o: Partial<Parameters<typeof ruCell>[2]> = {}) => ruCell(t, m.byNode.get(`n_${n}`), { country: "Russia", unmeasured: m.unmeasured, ...o });

  it("closed with a relay through two entries: the first and the second, and the way direct is closed", () => {
    const m = model([relay("1", "msk", "de"), relay("2", "spb", "de")]);
    expect(cell(m, "de")).toMatchObject({ tone: "ok", word: "via msk and spb", sub: "direct is blocked", noWay: false });
    expect(viaWord(t, m.byNode.get("n_de")!.pairs)).toBe("via msk and spb");
  });
  it("more than two entries: the first and how many more", () => {
    expect(viaWord(t, [{ bridge: 0, entryName: "a" }, { bridge: 0, entryName: "b" }, { bridge: 0, entryName: "c" }] as WayPair[])).toBe("via a +2");
  });
  it("no relay: no path, Bypass offered; open on some networks: direct, not on them", () => {
    const m = model([]);
    expect(cell(m, "de")).toMatchObject({ tone: "bad", word: "no path", sub: "direct is blocked", noWay: true });
    expect(cell(m, "nl")).toMatchObject({ tone: "part", word: "direct, except Operator A", sub: "" });
  });
  it("an entry: seen, how many it passes and how many have a break", () => {
    const m = model([relay("1", "msk", "de"), relay("2", "msk", "nl", { check: failing })]);
    expect(cell(m, "msk")).toMatchObject({ tone: "ok", word: "visible", sub: "entry for 2 · 1 with a break" });
    expect(cell(m, "spb")).toMatchObject({ word: "visible", sub: "carries no one yet" });
  });
  it("the tooltip joins the word and the line; a node with nothing deployed has no cell", () => {
    const m = model([relay("1", "msk", "de")]);
    expect(cell(m, "de")!.tip).toBe("via msk · direct is blocked");
    expect(ruCell(t, undefined, { country: "Russia", unmeasured: false })).toBeNull();
  });
  it("while the relays load or fail, the cell says that, and offers nothing", () => {
    const m = model([]);
    expect(cell(m, "de", { state: "load" })).toMatchObject({ word: "loading…", noWay: false });
    expect(cell(m, "de", { state: "error" })).toMatchObject({ word: "no relay data", noWay: false });
  });
  it("a helper's cell is what the country sees, with no second line", () => {
    const m = model(undefined);
    expect(cell(m, "de", { helper: true })).toMatchObject({ tone: "bad", word: "direct is blocked", sub: "" });
    expect(cell(m, "nl", { helper: true })).toMatchObject({ tone: "part", word: "direct, except Operator A" });
  });
  it("the verdict of the Paths view's first column", () => {
    expect((["ok", "part", "bad", "none"] as const).map((x) => verdictWord(t, x))).toEqual(["has a path", "partly", "no path", "no data"]);
  });
});

describe("the summary line of the Paths view", () => {
  const exits = fleetWays({
    nodes: [node("de"), node("nl"), node("msk", { protocols: [], countryCode: "RU" })],
    relays: [relay("1", "msk", "nl"), relay("2", "msk", "nl", { check: failing })],
    checks: new Map([check("de", CountryVerdict.BLOCKED), check("nl", CountryVerdict.BLOCKED), check("msk", CountryVerdict.OK)].map((c) => [c.nodeId, c])),
    country: "RU",
    nowS: NOW,
  });
  it("says how many are reached, how many have no way, how many relays and how many fail", () => {
    expect(waysSummary(t, exits, { country: "Russia" })).toBe("From Russia: 0 of 2 reachable · no path 1 · 2 relays, 1 not working");
  });
  it("says an entry that does not work once, in place of the counts", () => {
    expect(waysSummary(t, exits, { country: "Russia", deadEntry: { name: "msk", why: "offline" } })).toBe("From Russia: entry msk offline · 2 relays at risk");
    expect(waysSummary(t, exits, { country: "Russia", deadEntry: { name: "msk", why: "hidden" } })).toBe("From Russia: entry msk not visible · 2 relays at risk");
  });
});
