import { describe, expect, it } from "vitest";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import type { CountryCheck } from "./health";
import { darkEverywhere, laneNow, midnights, segmentsOf, shownSources, tallyOf, verdictKind, type Lane } from "./reachability";

const H = 3600;
const now = 1_760_500_000;
const since = now - 7 * 24 * H;
const lane = (points: [number, CountryVerdict][], until = now): Lane => ({ source: "globalping", points: points.map(([atUnix, verdict]) => ({ atUnix, verdict })), untilUnix: until });

describe("the lanes of the week", () => {
  it("draw each point until the next one and the last until the lane ends, in percent of the week", () => {
    // the server's compacted lane: ok, cut on day 4 for 16 hours, ok again
    const cut = since + 3 * 24 * H + 10 * H;
    const segs = segmentsOf(lane([[since + 300, CountryVerdict.OK], [cut, CountryVerdict.TCP_BLOCKED], [cut + 16 * H, CountryVerdict.OK]]), since, now);
    expect(segs.map((s) => s.verdict)).toEqual([CountryVerdict.OK, CountryVerdict.TCP_BLOCKED, CountryVerdict.OK]);
    expect(segs[0]).toMatchObject({ from: since + 300, to: cut });
    expect(segs[1]).toMatchObject({ from: cut, to: cut + 16 * H });
    expect(segs[2]!.to).toBe(now);
    expect(segs[1]!.left).toBeCloseTo(((cut - since) / (7 * 24 * H)) * 100, 6);
    expect(segs[1]!.width).toBeCloseTo((16 / 168) * 100, 6);
    // the five minutes before the first round stay empty: nothing was seen there
    expect(segs[0]!.left).toBeGreaterThan(0);
    expect(segs.reduce((sum, s) => sum + s.width, 0)).toBeCloseTo(100 - segs[0]!.left, 6);
  });

  it("end where the lane ends: a probe run that stopped counting leaves the rest of the week empty", () => {
    const segs = segmentsOf(lane([[since + H, CountryVerdict.OK]], now - 30 * H), since, now);
    expect(segs).toHaveLength(1);
    expect(segs[0]!.to).toBe(now - 30 * H);
    expect(laneNow(lane([[since + H, CountryVerdict.OK]], now - 30 * H), now)).toBeNull();
  });

  it("cut a point older than the window at its start, and never draw backwards", () => {
    const segs = segmentsOf(lane([[since - 5 * H, CountryVerdict.LISTED]]), since, now);
    expect(segs[0]).toMatchObject({ from: since, to: now, left: 0 });
    expect(segs[0]!.width).toBeCloseTo(100, 6);
    expect(segmentsOf(lane([[now - 60, CountryVerdict.OK]], now - 600), since, now)[0]!.width).toBe(0);
  });

  it("are empty for a source with nothing on the node", () => {
    expect(segmentsOf(lane([]), since, now)).toEqual([]);
    expect(laneNow(undefined, now)).toBeNull();
    expect(laneNow(lane([]), now)).toBeNull();
  });

  it("say what the source says now: the last verdict while the lane reaches now", () => {
    expect(laneNow(lane([[since, CountryVerdict.OK], [now - H, CountryVerdict.TCP_BLOCKED]]), now)).toBe(CountryVerdict.TCP_BLOCKED);
    expect(laneNow(lane([[since, CountryVerdict.LISTED]], now - 30), now)).toBe(CountryVerdict.LISTED);
    expect(laneNow(lane([[since, CountryVerdict.OK], [now - H, CountryVerdict.PARTIAL]]), now)).toBe(CountryVerdict.PARTIAL);
  });

  it("mark every local midnight inside the window", () => {
    const m = midnights(since, now);
    expect(m.length === 7 || m.length === 8).toBe(true);
    for (const x of m) {
      expect(x).toBeGreaterThan(since);
      expect(x).toBeLessThan(now);
      const d = new Date(x * 1000);
      expect([d.getHours(), d.getMinutes()]).toEqual([0, 0]);
    }
  });
});

describe("the summary", () => {
  it("counts nodes by the look of their verdict; a node without a row has no data", () => {
    const verdicts: Record<string, CountryVerdict> = { de1: CountryVerdict.OK, fi1: CountryVerdict.TCP_BLOCKED, nl1: CountryVerdict.OK, se1: CountryVerdict.LISTED, x1: CountryVerdict.BLOCKED };
    expect(tallyOf(["de1", "fi1", "nl1", "se1", "x1", "new1"], (id) => verdicts[id])).toEqual({ ok: 2, partial: 0, warn: 1, bad: 1, listed: 1, unknown: 1 });
    expect(tallyOf([], () => undefined)).toEqual({ ok: 0, partial: 0, warn: 0, bad: 0, listed: 0, unknown: 0 });
    expect([CountryVerdict.UNSPECIFIED, CountryVerdict.UNKNOWN].map(verdictKind)).toEqual(["unknown", "unknown"]);
  });

  it("counts a block on some networks apart from TCP blocked and from reachable", () => {
    expect(verdictKind(CountryVerdict.PARTIAL)).toBe("partial");
    const verdicts: Record<string, CountryVerdict> = { de1: CountryVerdict.PARTIAL, fi1: CountryVerdict.TCP_BLOCKED, nl1: CountryVerdict.OK, se1: CountryVerdict.PARTIAL };
    expect(tallyOf(["de1", "fi1", "nl1", "se1"], (id) => verdicts[id])).toEqual({ ok: 1, partial: 2, warn: 1, bad: 0, listed: 0, unknown: 0 });
  });
});

describe("the source lines", () => {
  it("keep Globalping, the lists and the probe in place while off, and add cheburcheck and bsbord only while they are on", () => {
    expect(shownSources([])).toEqual(["globalping", "lists", "tspu"]);
    expect(shownSources(["globalping", "bsbord"])).toEqual(["globalping", "lists", "tspu", "bsbord"]);
    expect(shownSources(["cheburcheck_own", "tspu", "cheburcheck", "bsbord"])).toEqual(["globalping", "lists", "tspu", "cheburcheck", "cheburcheck_own", "bsbord"]);
  });
});

describe("a network that reaches none of the nodes", () => {
  type Vantage = CountryCheck["vantages"][number];
  const v = (source: string, key: string, state: string, over: Partial<Vantage> = {}): Vantage => ({
    source, key, network: key, region: "", asn: "", kind: "", state, atUnix: now, dpiHop: 0, icmpOk: false, tcpOk: false, ...over,
  });
  const crimea = (state: string) => v("bsbord", "mts|krym|off", state, { network: "MTS", region: "КРЫМ" });
  const node = (...vantages: Vantage[]) => ({ vantages }) as CountryCheck;

  it("is named when one vantage is dark for every node its source looked at, while something reaches them", () => {
    const fleet = [
      node(crimea("dark"), v("bsbord", "volna|krym|off", "reach", { network: "Volna", region: "КРЫМ" }), v("globalping", "AS8359", "reach")),
      node(crimea("dark"), v("bsbord", "volna|krym|off", "reach", { network: "Volna", region: "КРЫМ" })),
      node(crimea("dark"), v("cheburcheck", "probe:7", "dark", { network: "Rostelecom", asn: "12389", region: "Moscow" })),
    ];
    expect(darkEverywhere(fleet)).toEqual([{ source: "bsbord", name: "MTS (КРЫМ)" }]);
    // the same network twice in one row counts once
    expect(darkEverywhere([node(crimea("dark"), crimea("dark"), v("globalping", "AS1", "reach")), node(crimea("dark"))])).toEqual([{ source: "bsbord", name: "MTS (КРЫМ)" }]);
  });

  it("says nothing when the vantage reaches one node, when one node is all there is, or when nothing reaches any node", () => {
    expect(darkEverywhere([node(crimea("dark"), v("globalping", "AS1", "reach")), node(crimea("reach"))])).toEqual([]);
    expect(darkEverywhere([node(crimea("dark"), v("globalping", "AS1", "reach"))])).toEqual([]);
    // a full block of the whole fleet: the full alerts cover it, a list of every network would only repeat it
    expect(darkEverywhere([node(crimea("dark"), v("globalping", "AS1", "dark")), node(crimea("dark"), v("globalping", "AS1", "dark"))])).toEqual([]);
    // a white list cuts every node alike by design: it is never "dark"
    expect(darkEverywhere([node(crimea("whitelist"), v("globalping", "AS1", "reach")), node(crimea("whitelist"))])).toEqual([]);
    expect(darkEverywhere([])).toEqual([]);
  });

  it("orders by source, then by name, and counts a node without the source's vantages out (bsbord skips a node with no TCP port)", () => {
    const gp = (state: string) => v("globalping", "AS8359", state, { network: "MTS", asn: "8359", region: "Omsk" });
    const fleet = [node(crimea("dark"), gp("dark"), v("tspu", "nod_probe1", "reach")), node(crimea("dark"), gp("dark")), node(v("tspu", "nod_probe1", "reach"), gp("dark"))];
    expect(darkEverywhere(fleet)).toEqual([
      { source: "globalping", name: "MTS (AS8359, Omsk)" },
      { source: "bsbord", name: "MTS (КРЫМ)" },
    ]);
  });
});
