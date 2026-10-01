import { describe, expect, it } from "vitest";
import { foldDays, niceCeil, protocolShort, smoothPath, sortProtocols, tail, toSeries } from "./series";

describe("protocolShort", () => {
  it("writes the tags the owner uses, never a cut word", () => {
    expect(protocolShort("hysteria2")).toBe("HY2");
    expect(protocolShort("awg")).toBe("AWG");
    expect(protocolShort("amneziawg")).toBe("AWG");
    expect(protocolShort("vless")).toBe("VLESS");
  });
});

const hour = (unix: number, ...values: [string, number][]) => ({ startUnix: unix, values: values.map(([protocol, value]) => ({ protocol, value })) });

describe("toSeries", () => {
  it("makes dense arrays per protocol, sums them and puts Hysteria2 first", () => {
    const s = toSeries([hour(0, ["awg", 2], ["hysteria2", 5]), hour(3600), hour(7200, ["hysteria2", 1])]);
    expect(s.protocols).toEqual(["hysteria2", "awg"]);
    expect(s.values.hysteria2).toEqual([5, 0, 1]);
    expect(s.values.awg).toEqual([2, 0, 0]);
    expect(s.total).toEqual([7, 0, 1]);
  });
  it("copes with no points and with a series of empty hours", () => {
    expect(toSeries([]).total).toEqual([]);
    expect(toSeries([hour(0), hour(3600)])).toMatchObject({ protocols: [], total: [0, 0] });
  });
  it("tail keeps the last n buckets", () => {
    const s = tail(toSeries([hour(0, ["hysteria2", 1]), hour(1, ["hysteria2", 2]), hour(2, ["hysteria2", 3])]), 2);
    expect(s.times).toEqual([1, 2]);
    expect(s.total).toEqual([2, 3]);
  });
});

describe("foldDays", () => {
  // local noon on a fixed day, so the test does not depend on the time zone of the machine
  const now = new Date(2026, 8, 30, 12, 0, 0);
  const at = (daysAgo: number, h: number) => Math.floor(new Date(2026, 8, 30 - daysAgo, h).getTime() / 1000);

  it("sums the hours of each local day and keeps seven days, today last", () => {
    const hourly = toSeries([hour(at(8, 10), ["hysteria2", 100]), hour(at(6, 0), ["hysteria2", 1]), hour(at(6, 23), ["hysteria2", 2]), hour(at(0, 9), ["hysteria2", 4]), hour(at(0, 10), ["hysteria2", 8])]);
    const s = foldDays(hourly, "sum", 7, now);
    expect(s.total).toEqual([3, 0, 0, 0, 0, 0, 12]); // the hour 8 days ago is outside the window
    expect(s.times).toHaveLength(7);
  });
  it("takes the day's peak for online people", () => {
    const hourly = toSeries([hour(at(0, 1), ["hysteria2", 3]), hour(at(0, 2), ["hysteria2", 7]), hour(at(0, 3), ["hysteria2", 5])]);
    expect(foldDays(hourly, "max", 7, now).total.at(-1)).toBe(7);
  });
});

describe("niceCeil", () => {
  it.each([
    [0, 1],
    [0.37, 0.5],
    [1, 1],
    [1.2, 2],
    [2.2, 2.5],
    [4, 5],
    [73, 100],
    [0.0031, 0.005],
  ])("%s -> %s", (v, want) => {
    expect(niceCeil(v)).toBeCloseTo(want, 10);
  });
});

describe("smoothPath", () => {
  it("starts at the first point and ends at the last, with one cubic per step", () => {
    const d = smoothPath([0, 10, 20], [5, 1, 3]);
    expect(d.startsWith("M0,5 ")).toBe(true);
    expect(d.endsWith("20,3")).toBe(true);
    expect(d.match(/C/g)).toHaveLength(2);
  });
  it("is empty for no points", () => {
    expect(smoothPath([], [])).toBe("");
  });
});

describe("sortProtocols", () => {
  it("knows Hysteria2 and AmneziaWG, then alphabetical", () => {
    expect(sortProtocols(["zeta", "awg", "alpha", "hysteria2", "awg"])).toEqual(["hysteria2", "awg", "alpha", "zeta"]);
  });
});
