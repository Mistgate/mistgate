import { queryOptions } from "@tanstack/react-query";
import type { GetReachabilityHistoryResponse } from "@/gen/mistgate/admin/v1/country_reachability_pb";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { countryReach } from "./api";
import { sourceRank, vantageName, type CountryCheck, type CountryLook } from "./health";
import { plain, type Plain } from "./plain";

// The Reachability page (screens/reachability): the week of each source per node (CountryReachabilityService.
// GetReachabilityHistory) and the small pure helpers that turn it into lanes and counts. The verdicts of now come from
// GetChecks, as on Health.

export type ReachHistory = Plain<GetReachabilityHistoryResponse>;
export type Lane = ReachHistory["nodes"][number]["lanes"][number];

/** The sources in the order of the page (and of the server's `sources`). */
export const reachSources = ["globalping", "lists", "tspu", "cheburcheck", "cheburcheck_own", "bsbord"] as const;
export type ReachSource = (typeof reachSources)[number];

/** The lines of a node: the first three always (dimmed while off, so the rows keep their shape), an external source while it is on. */
export const shownSources = (on: readonly string[]): ReachSource[] => reachSources.filter((s, i) => i < 3 || on.includes(s));

/**
 * Seven days back. Keyed under ["health", "checks"]: whatever refreshes the checks (a source switched or tested, the country
 * changed) refreshes their week as well. A round comes every 10 minutes at the most, so a minute is often enough.
 */
export const historyQuery = queryOptions({
  queryKey: ["health", "checks", "history"],
  queryFn: async ({ signal }) => plain(await countryReach.getReachabilityHistory({ days: 7 }, { signal })),
  refetchInterval: 60_000,
});

/** The look of a verdict, as the Health column draws it: quiet tick, the half disc, "!", "×", the list mark, a grey dot. */
export function verdictKind(v: CountryVerdict): CountryLook["kind"] {
  switch (v) {
    case CountryVerdict.OK:
      return "ok";
    case CountryVerdict.PARTIAL:
      return "partial";
    case CountryVerdict.TCP_BLOCKED:
      return "warn";
    case CountryVerdict.BLOCKED:
      return "bad";
    case CountryVerdict.LISTED:
      return "listed";
    default:
      return "unknown";
  }
}

export type Segment = {
  verdict: CountryVerdict;
  /** unix seconds, cut to the window */
  from: number;
  to: number;
  /** percent of the window */
  left: number;
  width: number;
};

/**
 * A lane as segments of the window [since, now]: a point lasts until the next one, the last until the lane ends (now, or the
 * moment the probe node's run stops counting). Before the first point and after the end the lane is empty: nothing was seen.
 */
export function segmentsOf(lane: Lane, since: number, now: number): Segment[] {
  const span = Math.max(1, now - since);
  return lane.points.map((p, i) => {
    const from = Math.max(p.atUnix, since);
    const to = Math.max(from, Math.min(lane.points[i + 1]?.atUnix ?? lane.untilUnix, now));
    return { verdict: p.verdict, from, to, left: ((from - since) / span) * 100, width: ((to - from) / span) * 100 };
  });
}

/** What a source says of a node now: its last verdict while the lane reaches now (a minute of slack), else nothing. */
export function laneNow(lane: Lane | undefined, now: number): CountryVerdict | null {
  const last = lane?.points.at(-1);
  return lane && last && lane.untilUnix >= now - 60 ? last.verdict : null;
}

/** Midnights inside the window (local time), for the day marks over the lanes. */
export function midnights(since: number, now: number): number[] {
  const d = new Date(since * 1000);
  d.setHours(24, 0, 0, 0);
  const out: number[] = [];
  for (; d.getTime() / 1000 < now; d.setDate(d.getDate() + 1)) out.push(d.getTime() / 1000);
  return out;
}

export type Tally = Record<CountryLook["kind"], number>;

/** Nodes per combined verdict; a node without a row has no data. */
export function tallyOf(nodeIds: readonly string[], verdictOf: (id: string) => CountryVerdict | undefined): Tally {
  const out: Tally = { ok: 0, partial: 0, warn: 0, bad: 0, listed: 0, unknown: 0 };
  for (const id of nodeIds) out[verdictKind(verdictOf(id) ?? CountryVerdict.UNKNOWN)]++;
  return out;
}

export type DarkEverywhere = { source: string; name: string };

/**
 * The vantages that are dark for every node their source looked at, two nodes or more, while at least one of those nodes is
 * reached from somewhere: an operator that cuts the whole fleet alike. No alert covers it (the partial alert needs the network to
 * reach another node, the full one needs every network dark), so the page says it. By source, then name.
 */
export function darkEverywhere(checks: readonly CountryCheck[]): DarkEverywhere[] {
  const nodes = new Map<string, number>(); // source -> nodes it has vantages on
  const reached = new Map<string, number>(); // source -> of those, nodes some vantage reaches
  const dark = new Map<string, DarkEverywhere & { n: number }>(); // source|key -> nodes it is dark on
  for (const c of checks) {
    const reach = c.vantages.some((v) => v.state === "reach");
    for (const s of new Set(c.vantages.map((v) => v.source))) {
      nodes.set(s, (nodes.get(s) ?? 0) + 1);
      if (reach) reached.set(s, (reached.get(s) ?? 0) + 1);
    }
    const once = new Map(c.vantages.filter((v) => v.state === "dark").map((v) => [`${v.source}|${v.key}`, v]));
    for (const [k, v] of once) {
      const d = dark.get(k) ?? { source: v.source, name: vantageName(v), n: 0 };
      d.n++;
      dark.set(k, d);
    }
  }
  return [...dark.values()]
    .filter((d) => d.n >= 2 && d.n === nodes.get(d.source) && (reached.get(d.source) ?? 0) > 0)
    .sort((a, b) => sourceRank(a.source) - sourceRank(b.source) || a.name.localeCompare(b.name))
    .map(({ source, name }) => ({ source, name }));
}
