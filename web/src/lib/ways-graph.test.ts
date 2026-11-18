import { describe, expect, it } from "vitest";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import type { RelayMsg } from "./relay";
import type { Ipv6Entry } from "./ways";
import { ipv6StateOf, networkOf, relayRefusal, wayOf, type GServer } from "./ways-graph";

const relay = (over: Partial<RelayMsg>) =>
  ({ id: "r1", inboundId: "hy2", exitNodeId: "be", exitNodeName: "be", viaNodeId: "msk1", viaNodeName: "msk1", network: "udp", port: 443, enabled: true, state: InboundState.ACTIVE, ...over }) as RelayMsg;

describe("relay rules", () => {
  const server = (over: Partial<GServer> = {}): GServer => ({ inboundId: "hy2", name: "HY2", protocol: "hysteria2", net: "udp", port: 443, egress: "direct", listed: true, cdn: false, on: true, failed: false, way: "works", ...over });
  it("refuses what the relay dialog refuses", () => {
    expect(relayRefusal("be", "be", server(), [])).toBe("same_node");
    expect(relayRefusal("msk1", "be", server({ noRelay: "cdn" }), [])).toBe("cdn");
    expect(relayRefusal("msk1", "be", server(), [relay({})])).toBe("exists");
    expect(relayRefusal("ru2", "be", server(), [relay({})])).toBeUndefined();
  });
  it("networks of the protocols", () => {
    expect([networkOf("hysteria2"), networkOf("awg"), networkOf("vless"), networkOf("mtproto"), networkOf("x")]).toEqual(["udp", "udp", "tcp", "tcp", undefined]);
  });
});

const v6 = (over: Partial<Ipv6Entry> = {}): Ipv6Entry => ({ entry: "", entrySupported: true, unavailableReason: "", addresses: [], src: "", ...over });

describe("the way of a server and the IPv6 row", () => {
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

  it("refusals come with the reason the row shows: cdn, off, net", () => {
    const server = (over: Partial<GServer> = {}): GServer => ({ inboundId: "x", name: "X", protocol: "vless", net: "tcp", port: 1, egress: "direct", listed: true, cdn: false, on: true, failed: false, way: "works", ...over });
    expect(relayRefusal("a", "b", server({ noRelay: "cdn" }), [])).toBe("cdn");
    expect(relayRefusal("a", "b", server({ noRelay: "off" }), [])).toBe("off");
    expect(relayRefusal("a", "b", server({ noRelay: "net" }), [])).toBe("net");
  });
});
