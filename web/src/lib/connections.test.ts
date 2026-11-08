import { describe, expect, it } from "vitest";
import { fill, pickForm, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { deviceConnParts, inboundConnParts, type DeviceConn, type InboundConn } from "./connections";
import { makeFmt } from "./format";

const t = Object.assign((key: string, vars?: Record<string, string | number>) => fill((en as Record<string, string>)[key]!, vars), {
  n: (key: string, count: number, vars?: Record<string, string | number>) => fill(pickForm((en as Record<string, string>)[key]!, count, "en"), { n: count, ...vars }),
}) as unknown as T;
const fmt = makeFmt("en", t);
const inbound = (over: Partial<InboundConn>): InboundConn => ({
  inboundId: "inb_1", profileId: "prf_1", profileName: "HY2", protocol: "hysteria2", connects: 0, rejects: 0, drops: 0, stalls: 0, onlineS: 0,
  unknownKeys: 0, hellos: 0, handshakes: 0, keysStalled: 0, keysTried: 0, hours: [], clients: [], clientCloses: 0, freezes: 0, quiet: 0,
  accepted: 0, hellosForeign: 0, rttMs: 0, lossPermille: 0, rttSamples: 0, pktsSent: 0, hsUnfinished: 0, tlsAlertsSent: 0, tlsAlertsRecv: 0,
  tlsCertRefused: 0, realityFallback: 0, authFailed: 0, tcpStalls: 0, tcpStalls16k: 0, initiations: 0, responses: 0, responsesUnmatched: 0,
  unsupported: ["client_closes", "freezes", "quiet", "accepted", "hellos_foreign", "rtt_ms", "loss_permille", "rtt_samples", "pkts_sent"], // an agent before links/2
  ...over,
});

// a device row of an agent before links/3: none of the per-key reasons is known
const noReasons = { tcpStalls: 0, tcpStalls16k: 0, initiations: 0, responses: 0, responsesUnmatched: 0,
  unsupported: ["tcp_stalls", "tcp_stalls_16k", "initiations", "responses", "responses_unmatched"] };
const device = (over: Partial<DeviceConn>): DeviceConn => ({
  deviceId: "d", nodeId: "n", nodeName: "de", inboundId: "i", profileName: "P", protocol: "awg", connects: 0, rejects: 0, drops: 0, stalls: 0,
  onlineS: 0, lastHourUnix: 0, platform: "", client: "", kind: "", hours: [], clientCloses: 0, freezes: 0, quiet: 0, rttMs: 0, lossPermille: 0,
  rttSamples: 0, pktsSent: 0, ...noReasons, ...over,
});

describe("why handshakes fail (links/3)", () => {
  it("says a device's answers were lost, and that the node did not answer, with a hint", () => {
    const lost = deviceConnParts(t, fmt, device({ stalls: 2, initiations: 9, responses: 9, responsesUnmatched: 8, unsupported: ["tcp_stalls", "tcp_stalls_16k"] }));
    expect(lost.map((p) => [p.text, p.warn])).toEqual([
      ["0 connections", undefined],
      ["handshake did not complete: 2", true],
      ["answers lost: 8 of 9", true],
    ]);
    expect(lost[2]!.hint).toContain("way back");
    const unanswered = deviceConnParts(t, fmt, device({ initiations: 12, unsupported: ["tcp_stalls", "tcp_stalls_16k"] }));
    expect(unanswered.map((p) => [p.text, p.warn])).toEqual([["0 connections", undefined], ["not answered by the node: 12", true]]);
    // the kernel backend (or an older agent) cannot tell: nothing is said of it
    expect(deviceConnParts(t, fmt, device({ initiations: 12, responsesUnmatched: 5 })).map((p) => p.text)).toEqual(["0 connections"]);
  });

  it("names VLESS freezes after the first 16 KB apart from later stalls", () => {
    const parts = deviceConnParts(t, fmt, device({ protocol: "vless", connects: 6, tcpStalls: 5, tcpStalls16k: 4,
      unsupported: ["initiations", "responses", "responses_unmatched"] }));
    expect(parts.map((p) => [p.text, p.warn])).toEqual([
      ["6 connections", undefined],
      ["froze after 16 KB: 4", true],
      ["stalled: 1", undefined],
    ]);
  });

  it("says on the profile line why handshakes fail: no reply, a refused certificate, REALITY's refusals", () => {
    const hy = inbound({ unsupported: ["reality_fallback", "auth_failed", "tcp_stalls", "tcp_stalls_16k", "initiations", "responses", "responses_unmatched"],
      connects: 2, hellos: 40, accepted: 8, handshakes: 6, hsUnfinished: 30, tlsCertRefused: 3, tlsAlertsRecv: 3, tlsAlertsSent: 2 });
    expect(inboundConnParts(t, fmt, hy).map((p) => [p.text, p.warn])).toEqual([
      ["2 connections", undefined],
      ["answered, no reply: 30", true],
      ["certificate refused: 3", true],
      ["refused by the node: 2", undefined],
      ["80% of 40 handshakes did not complete", true],
    ]);
    const vless = inbound({ protocol: "vless", unsupported: ["tls_alerts_sent", "tls_alerts_recv", "tls_cert_refused", "initiations", "responses", "responses_unmatched"],
      connects: 0, hellos: 20, hellosForeign: 3, realityFallback: 9, authFailed: 6 });
    expect(inboundConnParts(t, fmt, vless).map((p) => [p.text, p.warn])).toEqual([
      ["wrong REALITY key: 6", true],
      ["sent to the decoy: 3", undefined],
    ]);
    // an agent before links/3: the same zeros say nothing, and VLESS hellos are not read as failures
    expect(inboundConnParts(t, fmt, inbound({ protocol: "vless", hellos: 0, onlineS: 60 })).map((p) => p.text)).toEqual(["online 1 min"]);
  });
});

describe("connection counters in words", () => {
  it("names a device's counts, trouble flagged", () => {
    const parts = deviceConnParts(t, fmt, { deviceId: "d", nodeId: "n", nodeName: "de", inboundId: "i", profileName: "HY2", protocol: "hysteria2", connects: 1, rejects: 2, drops: 0, stalls: 3, onlineS: 120, lastHourUnix: 0, platform: "", client: "", kind: "", hours: [], clientCloses: 0, freezes: 0, quiet: 0, rttMs: 0, lossPermille: 0, rttSamples: 0, pktsSent: 0, ...noReasons });
    expect(parts).toEqual([
      { text: "1 connection" },
      { text: "key refused: 2", warn: true },
      { text: "handshake did not complete: 3", warn: true },
      { text: "online 2 min" },
    ]);
  });

  it("gives the share of Hysteria2 handshakes that never authenticated, trouble from half of ten", () => {
    expect(inboundConnParts(t, fmt, inbound({ hellos: 40, handshakes: 10, connects: 9 }))).toEqual([
      { text: "9 connections" },
      { text: "75% of 40 handshakes did not complete", warn: true },
    ]);
    // few handshakes, or most complete: said, but not trouble
    expect(inboundConnParts(t, fmt, inbound({ hellos: 6, handshakes: 1 }))[0]).toEqual({ text: "83% of 6 handshakes did not complete", warn: false });
    expect(inboundConnParts(t, fmt, inbound({ hellos: 20, handshakes: 20 }))).toEqual([]);
  });

  it("with a links/2 agent: frozen paths are the trouble, early drops are not, and RTT and loss are said", () => {
    const x = inbound({ unsupported: [], connects: 20, drops: 12, freezes: 3, rttMs: 85, rttSamples: 40, lossPermille: 62, pktsSent: 5000,
      hellos: 30, hellosForeign: 4, accepted: 20, handshakes: 20 });
    expect(inboundConnParts(t, fmt, x)).toEqual([
      { text: "20 connections" },
      { text: "dropped at once: 12", warn: false },
      { text: "froze: 3", warn: true },
      { text: "RTT 85 ms" },
      { text: "loss 6.2%", warn: true },
      // 30 hellos, 20 completed: 10 never completed
      { text: "33% of 30 handshakes did not complete", warn: false },
    ]);
    expect(deviceConnParts(t, fmt, { deviceId: "d", nodeId: "n", nodeName: "de", inboundId: "i", profileName: "HY2", protocol: "hysteria2",
      connects: 5, rejects: 0, drops: 4, stalls: 0, onlineS: 0, lastHourUnix: 0, platform: "", client: "", kind: "", hours: [],
      clientCloses: 0, freezes: 0, quiet: 4, rttMs: 40, lossPermille: 3, rttSamples: 2, pktsSent: 300, ...noReasons })).toEqual([
      { text: "5 connections" },
      { text: "dropped at once: 4", warn: false },
      { text: "RTT 40 ms" },
    ]);
  });

  it("counts AmneziaWG devices whose handshake never completed, and unknown keys as context", () => {
    expect(inboundConnParts(t, fmt, inbound({ protocol: "awg", connects: 2, keysStalled: 1, keysTried: 3, unknownKeys: 5 }))).toEqual([
      { text: "2 connections" },
      { text: "1 of 3 devices never completed a handshake", warn: true },
      { text: "unknown keys: 5" },
    ]);
  });
});
