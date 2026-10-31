import { describe, expect, it } from "vitest";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CheckStatus, CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import type { CountryCheck } from "./health";
import type { RelayMsg } from "./relay";
import { entryOf, entryOffered, entryState, hintOf, ipv6FactOf, isWayId, lanesOf, relaysState, type Ipv6Entry, type WaysInput } from "./ways";

const country = (verdict: CountryVerdict, dark: string[] = [], over: Partial<CountryCheck> = {}) =>
  ({ nodeId: "", verdict, atUnix: 100, probes: [], vantages: dark.map((network) => ({ network, key: "", state: "dark" })), ...over }) as unknown as CountryCheck;
const relay = (over: Partial<RelayMsg> = {}) =>
  ({ id: "r1", exitNodeId: "de1", exitNodeName: "de1", viaNodeId: "msk1", viaNodeName: "msk1", enabled: true, state: InboundState.ACTIVE, ...over }) as RelayMsg;
const on = relay();
const down = relay({ id: "r2", enabled: false, state: InboundState.PENDING, check: { ok: false } as RelayMsg["check"] });
const off = relay({ id: "r3", enabled: false, state: InboundState.PENDING });
const applying = relay({ id: "r4", state: InboundState.PENDING });

const base = (over: Partial<WaysInput> = {}): WaysInput => ({
  node: { id: "de1", status: NodeStatus.ONLINE },
  inbounds: [{ state: InboundState.ACTIVE }, { state: InboundState.ACTIVE }],
  ...over,
});

describe("lanesOf", () => {
  it("a node dark on part of the country: direct works, half seen, opened first, no badge", () => {
    const w = lanesOf(base({ country: country(CountryVerdict.PARTIAL, ["Tele2", "MTS"]) }));
    expect(w.lanes.map((l) => l.id)).toEqual(["direct"]);
    expect(w.lanes[0]).toMatchObject({ state: "ok", seen: "part", ring: "half", fault: false, dark: ["MTS", "Tele2"] });
    expect(w.badge).toBe(0);
    expect(w.first).toBe("direct");
  });

  it("a healthy node has only direct, green", () => {
    const w = lanesOf(base({ country: country(CountryVerdict.OK), relays: [] }));
    expect(w.lanes).toHaveLength(1);
    expect(w.lanes[0]).toMatchObject({ ring: "ok", seen: "all" });
    expect(w.out).toEqual([]);
  });

  it("through an entry: seen where the exit is dark, and the entry's own dark networks among them", () => {
    const de1 = country(CountryVerdict.PARTIAL, ["MTS", "Tele2"]);
    const seenAll = lanesOf(base({ country: de1, relays: [on], countryOf: () => country(CountryVerdict.PARTIAL, ["Beeline"]) }));
    expect(seenAll.lanes[1]).toMatchObject({ id: "via:msk1", kind: "via", state: "ok", seen: "all", ring: "ok", peer: { id: "msk1", name: "msk1" } });
    const darkToo = lanesOf(base({ country: de1, relays: [on], countryOf: () => country(CountryVerdict.PARTIAL, ["Tele2"]) }));
    expect(darkToo.lanes[1]).toMatchObject({ seen: "part", ring: "half", dark: ["Tele2"] });
    // nothing known of the entry
    expect(lanesOf(base({ country: de1, relays: [on] })).lanes[1]!.seen).toBe("unknown");
  });

  it("a relay lane's state: down, partly down, off, turning on", () => {
    expect(relaysState([down])).toBe("bad");
    expect(relaysState([on, down])).toBe("warn");
    expect(relaysState([off])).toBe("off");
    expect(relaysState([on, off])).toBe("ok");
    expect(relaysState([on, applying])).toBe("busy");
    const w = lanesOf(base({ relays: [on, down] }));
    expect(w.lanes[1]).toMatchObject({ state: "warn", ring: "warn", fault: true });
    expect(w.badge).toBe(1);
    expect(w.first).toBe("via:msk1");
  });

  it("an entry node lists the exits reached through it", () => {
    const toDe = relay({ viaNodeId: "msk1", exitNodeId: "de1", exitNodeName: "de1" });
    const toNl = relay({ id: "r9", viaNodeId: "msk1", exitNodeId: "nl1", exitNodeName: "nl1", enabled: false, state: InboundState.PENDING, check: { ok: false } as RelayMsg["check"] });
    const w = lanesOf(base({ node: { id: "msk1", status: NodeStatus.ONLINE }, relays: [toDe, toNl, toDe] }));
    expect(w.lanes.map((l) => l.id)).toEqual(["direct"]);
    expect(w.out.map((l) => [l.id, l.state, l.relays.length])).toEqual([
      ["out:de1", "ok", 2],
      ["out:nl1", "bad", 1],
    ]);
    expect(w.badge).toBe(1);
    expect(w.first).toBe("out:nl1");
  });

  it("a helper gets no relay lanes", () => {
    const w = lanesOf(base({ relays: undefined, country: country(CountryVerdict.BLOCKED, [], { probes: [{ valid: true, network: "MTS" }] } as never) }));
    expect(w.lanes.map((l) => l.id)).toEqual(["direct"]);
    expect(w.lanes[0]).toMatchObject({ seen: "none", ring: "bad", dark: ["MTS"] });
  });

  it("no profiles, failed profiles, broken ports, failing client checks, an offline node", () => {
    expect(lanesOf(base({ inbounds: [] })).lanes[0]!.state).toBe("off");
    expect(lanesOf(base({ inbounds: [{ state: InboundState.DISABLED }] })).lanes[0]!.state).toBe("off");
    expect(lanesOf(base({ inbounds: [{ state: InboundState.FAILED }, { state: InboundState.ACTIVE }] })).lanes[0]!.state).toBe("warn");
    expect(lanesOf(base({ inbounds: [{ state: InboundState.FAILED }] })).lanes[0]!.state).toBe("bad");
    const broken = lanesOf(base({ portChecks: [{ verdict: "broken" }] }));
    expect(broken.lanes[0]).toMatchObject({ state: "bad", fault: true });
    expect(broken.badge).toBe(1);
    expect(lanesOf(base({ tcpChecks: [{ verdict: "busy" }] })).lanes[0]!.state).toBe("warn");
    const cells = [{ deployed: true, last: { status: CheckStatus.FAILED } }, { deployed: true, last: { status: CheckStatus.OK } }];
    expect(lanesOf(base({ cells })).lanes[0]!.state).toBe("warn");
    const gone = lanesOf(base({ node: { id: "de1", status: NodeStatus.DOWN }, portChecks: [{ verdict: "broken" }] }));
    expect(gone.offline).toBe(true);
    expect(gone.lanes[0]).toMatchObject({ state: "off", fault: false });
  });

  it("the IPv6 lane: shown, off, for a server with IPv6, seen by the IPv6 rounds", () => {
    expect(lanesOf(base({ ipv6: "none" })).lanes.map((l) => l.id)).toEqual(["direct"]);
    const w = lanesOf(base({ ipv6: "ok", country: country(CountryVerdict.PARTIAL, ["MTS"], { ipv6: country(CountryVerdict.OK) }) }));
    expect(w.lanes.map((l) => l.id)).toEqual(["direct", "ipv6"]);
    expect(w.lanes[1]).toMatchObject({ state: "off", ring: "off", seen: "all" });
    expect(lanesOf(base({ ipv6: "broken" })).lanes[1]!.id).toBe("ipv6");
  });

  it("the IPv6 lane with the entry on works, and is down (a fault) while the server's IPv6 does not work", () => {
    const lit = lanesOf(base({ ipv6: "ok", entry: "on", country: country(CountryVerdict.PARTIAL, ["MTS"], { ipv6: country(CountryVerdict.OK) }) }));
    expect(lit.lanes[1]).toMatchObject({ id: "ipv6", state: "ok", ring: "ok", fault: false });
    // the doctor may stop reporting, the entry still has its lane
    const dark = lanesOf(base({ ipv6: "none", entry: "down" }));
    expect(dark.lanes[1]).toMatchObject({ id: "ipv6", state: "bad", ring: "bad", fault: true });
    expect(dark.badge).toBe(1);
  });
});

describe("the IPv6 entry", () => {
  const v6 = (over: Partial<Ipv6Entry>): Ipv6Entry => ({ entry: "", entrySupported: true, unavailableReason: "", addresses: [], src: "", ...over });

  it("entryState: on, down while the uplink has no working IPv6, else off", () => {
    expect(entryState(v6({}))).toBeUndefined();
    expect(entryState(v6({ entry: "2001:db8::5" }))).toBe("on");
    expect(entryState(v6({ entry: "2001:db8::5", entrySupported: false, unavailableReason: "ipv6_broken" }))).toBe("down");
    expect(entryState(v6({ entry: "2001:db8::5", entrySupported: false, unavailableReason: "no_ipv6" }))).toBe("down");
    // an old agent names no address, but the entry that is on still works
    expect(entryState(v6({ entry: "2001:db8::5", entrySupported: false, unavailableReason: "agent_old" }))).toBe("on");
  });

  it("entryOffered only when the node takes one and it is off", () => {
    expect(entryOffered(v6({}))).toBe(true);
    expect(entryOffered(v6({ entry: "2001:db8::5" }))).toBe(false);
    expect(entryOffered(v6({ entrySupported: false, unavailableReason: "main_is_ipv6" }))).toBe(false);
  });

  it("entryOf keeps the server's view, and words the doctor's fact when the node message has none", () => {
    const sent = v6({ entry: "2001:db8::5" });
    expect(entryOf(sent, "none")).toBe(sent);
    expect(entryOf(undefined, "broken")).toMatchObject({ entrySupported: false, unavailableReason: "ipv6_broken" });
    expect(entryOf(undefined, "unknown").unavailableReason).toBe("not_checked");
  });
});

describe("helpers", () => {
  it("hintOf says what the relay dialog says", () => {
    expect(hintOf(country(CountryVerdict.UNKNOWN))).toBeUndefined();
    expect(hintOf(country(CountryVerdict.TCP_BLOCKED, ["MTS"], { probes: [{ valid: true, network: "Beeline" }, { valid: false, network: "X" }] } as never))).toMatchObject({ verdict: "blocked", dark: ["Beeline", "MTS"] });
    expect(hintOf(country(CountryVerdict.LISTED))!.verdict).toBe("listed");
  });

  it("ipv6FactOf reads the doctor's ipv6 check", () => {
    expect(ipv6FactOf(undefined)).toBe("unknown");
    expect(ipv6FactOf([{ id: "ipv6", params: { ipv6: "no" } }])).toBe("none");
    expect(ipv6FactOf([{ id: "ipv6", params: { ipv6: "yes", connect: "ok" } }])).toBe("ok");
    expect(ipv6FactOf([{ id: "ipv6", params: { ipv6: "yes", connect: "failed" } }])).toBe("broken");
  });

  it("isWayId keeps the known shapes", () => {
    expect(["direct", "ipv6", "via:nod_msk1", "out:nod_de1"].every(isWayId)).toBe(true);
    expect(["", "via:", "bogus", "via:a b", 3].some(isWayId)).toBe(false);
  });
});
