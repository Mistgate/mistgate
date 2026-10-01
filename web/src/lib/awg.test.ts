import { describe, expect, it } from "vitest";
import { changedCount, guessPlatform, leaves, minClients, parseAwgConf, randomKey32, randomPort } from "./awg";
import { codedError } from "./coded-error";
import { errorCode } from "./errors";
import { Code, ConnectError } from "@connectrpc/connect";

// fake key material and documentation addresses only
const SECRET = "cHJpdmF0ZS1rZXktZm9yLXRlc3Rpbmctb25seS0wMDAwMDA=";
const peer = `\n[Peer]\nPublicKey = c2VydmVyLXB1YmxpYy1rZXktZm9yLXRlc3RpbmctMDAwMDA=\nPresharedKey = ${SECRET}\nAllowedIPs = 0.0.0.0/0, ::/0\nEndpoint = vpn.example.com:23456\nPersistentKeepalive = 25\n`;
const iface = (...lines: string[]) => `[Interface]\nAddress = 10.66.0.2/32\nDNS = 1.1.1.1, 8.8.8.8\nPrivateKey = ${SECRET}\n${lines.join("\n")}\n${peer}`;

const conf31 = iface("MTU = 1280", "Jc = 6", "Jmin = 40", "Jmax = 90", "S1 = 24", "S2 = 24", "S3 = 24", "S4 = 24", "H1 = 100-200", "H2 = 300-400", "H3 = 500-600", "H4 = 700-800", "I1 = <b 0x1234><r 16>", "HeaderProtectionKey = aGVhZGVyLXByb3RlY3Rpb24ta2V5LTMyLWJ5dGVzLSEh", "RandomTrailers = on", "RekeyAfterTime = 120-150", "DisableCookies = on");
const conf20 = iface("Jc = 5", "Jmin = 30", "Jmax = 80", "S1 = 20", "S2 = 21", "S3 = 22", "S4 = 23", "H1 = 100-200", "H2 = 300-400", "H3 = 500-600", "H4 = 700-800");
const conf15 = iface("Jc = 4", "Jmin = 20", "Jmax = 60", "S1 = 10", "S2 = 11", "H1 = 1111", "H2 = 2222", "H3 = 3333", "H4 = 4444", "I1 = <b 0x00>");
const conf10 = iface("Jc = 4", "Jmin = 20", "Jmax = 60", "S1 = 10", "S2 = 11", "H1 = 1111", "H2 = 2222", "H3 = 3333", "H4 = 4444");

describe("parseAwgConf", () => {
  it("3.1 by its own keys: fills the block, skips the key material", () => {
    const r = parseAwgConf(conf31);
    expect(r.kind).toBe("3.1");
    expect(r.version).toBe("3.1");
    expect(r.mtu).toBe(1280);
    expect(r.obfuscation).toMatchObject({ jc: 6, jmin: 40, jmax: 90, s4: 24, h1: "100-200", i1: "<b 0x1234><r 16>", i2: "", random_trailers: true, rekey_after_time: "120-150", preset: "custom", persistent_keepalive: "25" });
    expect(r.obfuscation.header_protection_key).toBe("aGVhZGVyLXByb3RlY3Rpb24ta2V5LTMyLWJ5dGVzLSEh");
    expect(r.obfuscation).not.toHaveProperty("disable_cookies"); // the client's switch, not the node's
    expect(r.ignored).toEqual(expect.arrayContaining(["PrivateKey", "PresharedKey", "Address", "DNS", "AllowedIPs", "Endpoint", "PublicKey", "DisableCookies"]));
    expect(r.unknown).toEqual([]);
    expect(r.notes).toEqual(["custom"]);
    expect(JSON.stringify(r)).not.toContain(SECRET); // not even by accident
  });
  it("2.0 by S3/S4 and H ranges", () => {
    const r = parseAwgConf(conf20);
    expect([r.kind, r.version, r.mtu]).toEqual(["2.0", "2.0", undefined]);
    expect(r.obfuscation).toMatchObject({ jc: 5, s3: 22, s4: 23, h4: "700-800" });
    expect(r.obfuscation).not.toHaveProperty("preset");
    expect(r.notes).toEqual([]);
  });
  it("1.5 (an I packet, single H) lands on 2.0 with S3 = S4 = 0 and the packet as the look", () => {
    const r = parseAwgConf(conf15);
    expect([r.kind, r.version]).toEqual(["1.5", "2.0"]);
    expect(r.obfuscation).toMatchObject({ s3: 0, s4: 0, h1: "1111", i1: "<b 0x00>", preset: "custom" });
    expect(r.notes).toEqual(["custom", "legacy"]);
  });
  it("1.0 (junk, S1-S2, single H) also lands on 2.0", () => {
    const r = parseAwgConf(conf10);
    expect([r.kind, r.version]).toEqual(["1.0", "2.0"]);
    expect(r.obfuscation).toMatchObject({ jc: 4, s3: 0, s4: 0 });
    expect(r.notes).toEqual(["legacy"]);
  });
  it("plain WireGuard has nothing to take but the MTU", () => {
    const r = parseAwgConf(iface("MTU = 1380"));
    expect([r.kind, r.version, r.mtu, r.obfuscation]).toEqual(["wg", null, 1380, {}]);
    expect(r.filled).toEqual(["MTU"]);
  });
  it("garbage gives nothing", () => {
    const r = parseAwgConf("hello world\nthis = not a config\n");
    expect([r.kind, r.version, r.mtu, r.obfuscation, r.filled]).toEqual(["none", null, undefined, {}, []]);
    expect(r.unknown).toEqual(["this"]);
    expect(parseAwgConf("")).toMatchObject({ kind: "none", version: null });
  });
  it("names strangers and drops bad values instead of guessing", () => {
    const r = parseAwgConf(iface("Jc = many", "H1 = x", "Jmin = 20", "Banana = 1", "RandomTrailers = maybe"));
    expect(r.obfuscation).toEqual({ jmin: 20, persistent_keepalive: "25" });
    expect(r.ignored).toEqual(expect.arrayContaining(["Jc", "H1", "RandomTrailers"]));
    expect(r.unknown).toEqual(["Banana"]);
  });
  it("reads CRLF, comments and any case of the keys", () => {
    const r = parseAwgConf("# my tunnel\r\n[interface]\r\njc=7\r\nS1 = 9\r\n; note\r\nheaderprotectionkey = abc\r\n");
    expect(r.obfuscation).toMatchObject({ jc: 7, s1: 9, header_protection_key: "abc" });
    expect(r.kind).toBe("3.1");
  });
  it("cuts an inline comment the way wg-quick does", () => {
    const r = parseAwgConf(iface("S1 = 25 # padding", "Jc = 5#x", "HeaderProtectionKey = abc # my key", "I1 = <b 0x12> # tag", "H1 = 100-200 ; not a comment"));
    expect(r.obfuscation).toMatchObject({ s1: 25, jc: 5, header_protection_key: "abc", i1: "<b 0x12>" });
    expect(r.ignored).toEqual(expect.arrayContaining(["H1"])); // ';' is not a comment mark of wg-quick: the value is bad
    expect(r.ignored).not.toContain("S1");
    expect(r.ignored).not.toContain("Jc");
  });
  it("a 3.x file without RandomTrailers means off (our own render leaves the line out); 2.0 and an explicit value are left alone", () => {
    const no = iface("S1 = 24", "HeaderProtectionKey = abc", "H1 = 1-2");
    expect(parseAwgConf(no).obfuscation).toMatchObject({ random_trailers: false });
    expect(parseAwgConf(conf31).obfuscation).toMatchObject({ random_trailers: true });
    expect(parseAwgConf(conf20).obfuscation).not.toHaveProperty("random_trailers");
  });
});
describe("changedCount", () => {
  it("counts the leaves that differ, including added and removed ones", () => {
    expect(changedCount({ jc: 1, s1: 2 }, { jc: 1, s1: 3 })).toBe(1);
    expect(changedCount({ jc: 1 }, { jc: 1, i1: "x" })).toBe(1);
    expect(changedCount({ a: { b: 1 } }, { a: { b: 1 } })).toBe(0);
    expect(changedCount(undefined, { jc: 1, jmin: 2 })).toBe(2);
  });
  it("flattens nested objects with dotted paths", () => {
    expect([...leaves({ a: { b: 1 }, c: 2 }).keys()]).toEqual(["a.b", "c"]);
  });
});

describe("generators", () => {
  it("random port stays in 10000-60000 and skips the well-known ones", () => {
    const seq = [51820 - 10000, 55424 - 10000, 0, 50000];
    let i = 0;
    expect(randomPort(() => seq[i++]!)).toBe(10000);
    for (let n = 0; n < 200; n++) {
      const p = randomPort();
      expect(p).toBeGreaterThanOrEqual(10000);
      expect(p).toBeLessThanOrEqual(60000);
    }
  });
  it("a header protection key is 32 bytes in standard base64", () => {
    const k = randomKey32();
    expect(atob(k)).toHaveLength(32);
    expect(k).toMatch(/^[A-Za-z0-9+/]{43}=$/);
  });
});

describe("tables", () => {
  it("3.1 asks for the newer clients", () => {
    expect(minClients["3.1"].find((c) => c.app === "AmneziaVPN")?.min).toBe("5.0.1.5");
    expect(minClients["2.0"].find((c) => c.app === "AmneziaVPN")?.min).toBe("4.8.12.9");
  });
  it("guesses the platform of the browser", () => {
    expect(guessPlatform("Mozilla/5.0 (iPhone; CPU iPhone OS 17_4)")).toBe("ios");
    expect(guessPlatform("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", 5)).toBe("ios");
    expect(guessPlatform("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", 0)).toBe("macos");
    expect(guessPlatform("curl/8")).toBe("other");
  });
});

describe("coded errors", () => {
  it("takes the words before the colon as the code", () => {
    expect(errorCode("device_limit: 5/5")).toBe("device_limit");
    expect(errorCode("agent too old")).toBe("agent_too_old");
    expect(errorCode("")).toBe("");
  });
  const t = Object.assign((k: string) => k, { n: (k: string) => k, opt: (k: string, v?: Record<string, string | number>) => (k === "awg.err.device_limit" ? `limit ${v?.detail}` : undefined), lang: "en" }) as never;
  it("uses the dictionary text with the detail and falls back to the generic one", () => {
    expect(codedError(new ConnectError("device_limit: 5/5", Code.FailedPrecondition), t, "awg.err")).toBe("limit 5/5");
    expect(codedError(new ConnectError("brand_new_code", Code.FailedPrecondition), t, "awg.err")).toBe("brand_new_code");
    expect(codedError(new ConnectError("x", Code.Internal), t, "awg.err")).toBe("err.generic");
  });
});
