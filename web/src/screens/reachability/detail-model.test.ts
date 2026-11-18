import { describe, expect, it } from "vitest";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { dictionaries, fill, pickForm, type Lang, type T } from "@/i18n";
import { makeFmt } from "@/lib/format";
import { glyphOf, networksOf, startedAt, toneOf, weekBar } from "./detail-model";

// The pure parts of the open row of a node: the bar of a week, when the trouble began, the networks that do and do not see the node.

const NOW = 1_800_000_000;
const DAY = 86400;
const SINCE = NOW - 7 * DAY;
const en = (l: Lang = "en") => {
  const d = dictionaries[l] as Record<string, string>;
  const t = Object.assign((key: string, vars?: Record<string, string | number>) => fill(d[key]!, vars), {
    n: (key: string, count: number, vars?: Record<string, string | number>) => fill(pickForm(d[key]!, count, l), { n: count, ...vars }),
  }) as unknown as T;
  return { t, fmt: makeFmt(l, t) };
};
const lane = (source: string, points: [number, CountryVerdict][], untilUnix = NOW) => ({ source, points: points.map(([atUnix, verdict]) => ({ atUnix, verdict })), untilUnix });
const OK = CountryVerdict.OK;

describe("the week as a bar", () => {
  it("fills the window: empty before the first point and after the end, one stretch for the same word twice", () => {
    const { t, fmt } = en();
    const bar = weekBar(t, fmt, "globalping", lane("globalping", [[SINCE + DAY, OK], [SINCE + 2 * DAY, OK], [NOW - DAY, CountryVerdict.PARTIAL]]), true, SINCE, NOW);
    expect(bar.map((s) => s.kind)).toEqual(["none", "ok", "part"]);
    expect(bar.reduce((n, s) => n + s.sec, 0)).toBe(7 * DAY);
    expect(bar[1]?.title).toMatch(/: reachable$/);
    expect(bar[2]?.title).toMatch(/ – now: blocked on some networks$/);
    expect(bar[0]?.title).toBe("");
  });

  it("is one empty stretch for a source that is off or has no lane, and says the lists in their own words", () => {
    const { t, fmt } = en();
    expect(weekBar(t, fmt, "globalping", undefined, true, SINCE, NOW)).toEqual([{ kind: "none", sec: 7 * DAY, title: "" }]);
    expect(weekBar(t, fmt, "globalping", lane("globalping", [[SINCE, OK]]), false, SINCE, NOW).map((s) => s.kind)).toEqual(["none"]);
    const lists = weekBar(t, fmt, "lists", lane("lists", [[SINCE, OK], [NOW - DAY, CountryVerdict.LISTED]]), true, SINCE, NOW);
    expect(lists.map((s) => s.kind)).toEqual(["ok", "reg"]);
    expect(lists[0]?.title).toMatch(/: not listed$/);
  });

  it("marks a lane that stopped counting: the gap after it is empty", () => {
    const { t, fmt } = en();
    const bar = weekBar(t, fmt, "tspu", lane("tspu", [[SINCE, OK]], NOW - 2 * DAY), true, SINCE, NOW);
    expect(bar.map((s) => s.kind)).toEqual(["ok", "none"]);
    expect(bar[1]?.sec).toBe(2 * DAY);
  });
});

describe("when the trouble began", () => {
  it("is the start of the run of non-reachable verdicts that reaches now, across sources", () => {
    const gp = lane("globalping", [[SINCE, OK], [NOW - 5 * 3600, CountryVerdict.PARTIAL]]);
    const tspu = lane("tspu", [[SINCE, OK], [NOW - 6 * 3600, CountryVerdict.TCP_BLOCKED]]);
    expect(startedAt([gp, tspu], NOW)).toBe(NOW - 6 * 3600);
    // an older trouble that was over does not count
    const old = lane("lists", [[SINCE, CountryVerdict.LISTED], [SINCE + DAY, OK]]);
    expect(startedAt([gp, old], NOW)).toBe(NOW - 5 * 3600);
    expect(startedAt([old], NOW)).toBe(0);
    expect(startedAt([], NOW)).toBe(0);
  });
});

describe("the networks of a check", () => {
  const v = (over: object) => ({ source: "globalping", key: "k", network: "", region: "", asn: "", kind: "home", state: "reach", atUnix: NOW, dpiHop: 0, icmpOk: true, tcpOk: true, why: "", probeName: "", port: 0, ...over });
  const check = (over: object) => ({ nodeId: "n", country: "RU", verdict: CountryVerdict.PARTIAL, atUnix: NOW, noProbe: [], vantages: [], tspuAtUnix: 0, tspuVerdict: CountryVerdict.UNSPECIFIED, tspuVantageName: "", tspuVantageId: "", ...over }) as never;

  it("counts who sees the node and who does not, the same network of one source once, the worse state winning", () => {
    const { t, fmt } = en();
    const n = networksOf(
      t,
      fmt,
      check({
        vantages: [v({ key: "a", network: "Operator A", region: "Moscow" }), v({ key: "b1", network: "Operator B", state: "reach" }), v({ key: "b2", network: "Operator B", state: "dark" }), v({ key: "c", network: "Operator C", source: "bsbord", state: "whitelist", kind: "mobile" }), v({ key: "d", network: "Operator D", state: "uncertain" })],
        noProbe: ["Operator E", "Operator F"],
      }),
    );
    expect(n.groups.map((g) => [g.kind, g.items.map((i) => i.name)])).toEqual([
      ["bad", ["Operator B", "Operator C"]],
      ["ok", ["Operator A"]],
      ["none", ["Operator D"]],
    ]);
    expect(n.groups[0]?.items[1]).toEqual({ name: "Operator C", meta: "mobile · white list", source: "bsbord" });
    expect([n.seen, n.total]).toEqual([1, 3]);
    expect(n.skip).toBe("Not checked this round: Operator E, Operator F");
  });

  it("adds the probe node of the TSPU source when the check has its verdict and no vantage of its own", () => {
    const { t, fmt } = en();
    const cut = networksOf(t, fmt, check({ tspuAtUnix: NOW, tspuVerdict: CountryVerdict.TCP_BLOCKED, tspuVantageName: "msk1" }));
    expect(cut.groups).toEqual([{ kind: "bad", n: 1, items: [{ name: "msk1", meta: "data center · TCP blocked", source: "TSPU probe" }] }]);
    const open = networksOf(t, fmt, check({ tspuAtUnix: NOW, tspuVerdict: CountryVerdict.OK, tspuVantageName: "msk1" }));
    expect([open.groups[0]?.kind, open.seen, open.total]).toEqual(["ok", 1, 1]);
    // a probe node that decided nothing is not a network
    expect(networksOf(t, fmt, check({ tspuAtUnix: NOW, tspuVerdict: CountryVerdict.UNKNOWN, tspuVantageName: "msk1" })).groups).toEqual([]);
    expect(networksOf(t, fmt, undefined)).toEqual({ groups: [], seen: 0, total: 0, skip: "" });
  });

  it("names an own probe by its network, its name and, where it is not reached, why", () => {
    const { t, fmt } = en();
    const n = networksOf(t, fmt, check({ vantages: [v({ source: "probes", key: "p", network: "ExampleNet", probeName: "kitchen", state: "dark", why: "tls_reset", port: 443 })] }));
    expect(n.groups[0]?.items[0]).toEqual({ name: "ExampleNet", meta: "home · probe “kitchen” · TLS is reset (443)", source: "Own probes" });
  });
});

describe("the tones", () => {
  it("map a verdict to the word's tone and the glyph before it", () => {
    expect(["ok", "partial", "warn", "bad", "listed", "unknown"].map((k) => toneOf(k as never))).toEqual(["", "part", "part", "bad", "bad", "mut"]);
    expect((["", "part", "bad", "mut"] as const).map(glyphOf)).toEqual(["ok", "part", "bad", "none"]);
  });
});
