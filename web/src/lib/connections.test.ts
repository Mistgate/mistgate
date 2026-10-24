import { describe, expect, it } from "vitest";
import { fill, pickForm, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { deviceConnParts, inboundConnParts, type InboundConn } from "./connections";
import { makeFmt } from "./format";

const t = Object.assign((key: string, vars?: Record<string, string | number>) => fill((en as Record<string, string>)[key]!, vars), {
  n: (key: string, count: number, vars?: Record<string, string | number>) => fill(pickForm((en as Record<string, string>)[key]!, count, "en"), { n: count, ...vars }),
}) as unknown as T;
const fmt = makeFmt("en", t);
const inbound = (over: Partial<InboundConn>): InboundConn => ({
  inboundId: "inb_1", profileId: "prf_1", profileName: "HY2", protocol: "hysteria2", connects: 0, rejects: 0, drops: 0, stalls: 0, onlineS: 0,
  unknownKeys: 0, hellos: 0, handshakes: 0, keysStalled: 0, keysTried: 0, hours: [], clients: [], clientCloses: 0, freezes: 0, quiet: 0,
  accepted: 0, hellosForeign: 0, rttMs: 0, lossPermille: 0, rttSamples: 0, pktsSent: 0,
  unsupported: ["client_closes", "freezes", "quiet", "accepted", "hellos_foreign", "rtt_ms", "loss_permille"], // an agent before links/2
  ...over,
});

describe("connection counters in words", () => {
  it("names a device's counts, trouble flagged", () => {
    const parts = deviceConnParts(t, fmt, { deviceId: "d", nodeId: "n", nodeName: "de", inboundId: "i", profileName: "HY2", protocol: "hysteria2", connects: 1, rejects: 2, drops: 0, stalls: 3, onlineS: 120, lastHourUnix: 0, platform: "", client: "", kind: "", hours: [], clientCloses: 0, freezes: 0, quiet: 0, rttMs: 0, lossPermille: 0, rttSamples: 0, pktsSent: 0 });
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
      // 30 hellos, 4 for another name, 20 completed: 6 never completed
      { text: "20% of 30 handshakes did not complete", warn: false },
    ]);
    expect(deviceConnParts(t, fmt, { deviceId: "d", nodeId: "n", nodeName: "de", inboundId: "i", profileName: "HY2", protocol: "hysteria2",
      connects: 5, rejects: 0, drops: 4, stalls: 0, onlineS: 0, lastHourUnix: 0, platform: "", client: "", kind: "", hours: [],
      clientCloses: 0, freezes: 0, quiet: 4, rttMs: 40, lossPermille: 3, rttSamples: 2, pktsSent: 300 })).toEqual([
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
