import { describe, expect, it } from "vitest";
import { dnsLine, dnsRecord, heldBack, sharedPortViews, shareOffer, validShareName, type PortInbound } from "./tcpshare";

const inb = (id: string, over: Partial<PortInbound> = {}): PortInbound => ({ id, profileId: `p_${id}`, profileName: `P ${id}`, protocol: "vless", port: 443, tlsServerName: `${id}.example.com`, on: true, lastError: "", ...over });
const tcp = (p: string) => p !== "hysteria2";

describe("the offer of a held name", () => {
  it("is made only for sni_taken that says share=1", () => {
    expect(shareOffer("sni_taken", { share: "1", suggest: "cdn.de1.example.com", port: "443", profile: "Main", name: "de1.example.com", free: "8443" })).toEqual({ port: "443", profile: "Main", held: "de1.example.com", suggest: "cdn.de1.example.com", free: "8443" });
    expect(shareOffer("sni_taken", { port: "443" })).toBeNull();
    expect(shareOffer("share_needs_site", { share: "1" })).toBeNull();
    expect(shareOffer("sni_taken", { share: "1", free: "0" })?.free).toBe("");
  });
});

describe("the name a profile answers to", () => {
  it("accepts a host name with a dot and nothing else", () => {
    for (const ok of ["cdn.de1.example.com", "CDN.example.com.", "a-b.example.org"]) expect(validShareName(ok), ok).toBe(true);
    for (const bad of ["", "cdn", "203.0.113.7", "[2001:db8::1]", "*.example.com", "a b.example.com", "-a.example.com", "ünï.example.com"]) expect(validShareName(bad), bad).toBe(false);
  });
});

describe("the DNS record of a name", () => {
  it("is A for IPv4, AAAA for IPv6 and a CNAME to a host name", () => {
    expect(dnsRecord("cdn.example.com", "203.0.113.7")).toEqual({ type: "A", name: "cdn.example.com", value: "203.0.113.7" });
    expect(dnsRecord("cdn.example.com", "[2001:db8::7]")).toEqual({ type: "AAAA", name: "cdn.example.com", value: "2001:db8::7" });
    expect(dnsRecord("cdn.de1.example.com", "de1.example.com.")).toEqual({ type: "CNAME", name: "cdn.de1.example.com", value: "de1.example.com" });
    expect(dnsRecord("", "de1.example.com")).toBeNull();
    expect(dnsLine({ type: "A", name: "cdn.example.com", value: "203.0.113.7" })).toBe("cdn.example.com A 203.0.113.7");
  });
});

describe("the shared ports of a node", () => {
  const snap = { port: 443, defaultInboundId: "a", accepted: 10, routes: [{ inboundId: "a", delivered: 6 }, { inboundId: "b", delivered: 4 }], defaultNoSni: 0, defaultUnknownName: 0, defaultNotTls: 0, defaultBadHello: 0, defaultHelloTooLarge: 0, defaultRouteDown: 0, timeouts: 0, evicted: 0, refused: 0, noRoute: 0, unknownNames: [], bindError: "" };
  it("lists a TCP port that two switched-on services stand on, with the counters and the default", () => {
    const v = sharedPortViews([inb("a"), inb("b"), inb("c", { port: 8443 }), inb("d", { protocol: "hysteria2" }), inb("e", { protocol: "hysteria2" })], [snap], tcp);
    expect(v).toHaveLength(1);
    expect(v[0]!.rows.map((r) => [r.inbound.id, r.isDefault, r.delivered])).toEqual([["a", true, 6], ["b", false, 4]]);
    expect(v[0]!.old).toBe(false);
    expect(v[0]!.noReport).toBe(false);
  });
  it("waits for the first report of a port that is set up and not yet reported", () => {
    const v = sharedPortViews([inb("a"), inb("b")], [], tcp);
    expect(v[0]!.noReport).toBe(true);
    expect(v[0]!.rows.every((r) => r.delivered === null && !r.isDefault)).toBe(true);
  });
  it("does not count a switched-off service, and shows nothing for a port that one service holds", () => {
    expect(sharedPortViews([inb("a"), inb("b", { on: false })], [], tcp)).toEqual([]);
  });
  it("says an agent that cannot share holds the others back, and which one runs", () => {
    const held = "share_agent_old: this node's agent cannot share a TCP port";
    expect(heldBack(held)).toBe(true);
    expect(heldBack("listen tcp :443: bind: address already in use")).toBe(false);
    const v = sharedPortViews([inb("a"), inb("b", { lastError: held }), inb("c", { lastError: held })], [], tcp);
    expect(v[0]!.old).toBe(true);
    expect(v[0]!.running).toBe("a");
    expect(v[0]!.waiting).toEqual(["P b", "P c"]);
    expect(v[0]!.noReport).toBe(false);
    expect(v[0]!.rows.find((r) => r.isDefault)?.inbound.id).toBe("a");
  });
});
