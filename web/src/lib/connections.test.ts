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
  unknownKeys: 0, hellos: 0, handshakes: 0, keysStalled: 0, keysTried: 0, unsupported: [], hours: [], clients: [], ...over,
});

describe("connection counters in words", () => {
  it("names a device's counts, trouble flagged", () => {
    const parts = deviceConnParts(t, fmt, { deviceId: "d", nodeId: "n", nodeName: "de", inboundId: "i", profileName: "HY2", protocol: "hysteria2", connects: 1, rejects: 2, drops: 0, stalls: 3, onlineS: 120, lastHourUnix: 0, platform: "", client: "", kind: "", hours: [] });
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

  it("counts AmneziaWG devices whose handshake never completed, and unknown keys as context", () => {
    expect(inboundConnParts(t, fmt, inbound({ protocol: "awg", connects: 2, keysStalled: 1, keysTried: 3, unknownKeys: 5 }))).toEqual([
      { text: "2 connections" },
      { text: "1 of 3 devices never completed a handshake", warn: true },
      { text: "unknown keys: 5" },
    ]);
  });
});
