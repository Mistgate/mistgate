import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import type { T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import type { Fmt } from "@/lib/format";
import { countryIn, hasKey, listOf, probeReason, sourceName, sourceRank, type CountryCheck, type CountryLook, type Vantage } from "@/lib/health";
import { laneNow, reachSources, segmentsOf, verdictKind, type Lane, type ReachSource } from "@/lib/reachability";
import type { State } from "./nodes";

// What the open row of a node says (the board's `.rc-d.r3`): the verdict in words, the facts beside it, the week of each source as a bar,
// the networks that see the node and those that do not. Pure: the words come from i18n, the numbers from the check and the lanes.

/** The tone of a word about a source: the plain one, "part" (some networks, TCP), "bad" (unreachable, in the registry), "mut" (nothing to say). */
export type Tone = "" | "part" | "bad" | "mut";
/** The board's glyph before it: a full dot, a half one, a cross, a ring. */
export type Glyph = "ok" | "part" | "bad" | "none";

export const toneOf = (kind: CountryLook["kind"]): Tone => (kind === "ok" ? "" : kind === "partial" || kind === "warn" ? "part" : kind === "unknown" ? "mut" : "bad");
export const glyphOf = (tone: Tone): Glyph => (tone === "" ? "ok" : tone === "mut" ? "none" : tone);

export const sourceKey: Record<ReachSource, MessageKey> = {
  globalping: "rc.src.globalping",
  lists: "rc.src.lists",
  tspu: "rc.src.tspu",
  cheburcheck: "rc.src.cheburcheck",
  cheburcheck_own: "rc.src.cheburcheck_own",
  bsbord: "rc.src.bsbord",
  probes: "rc.src.probes",
};

/**
 * What one source calls a verdict: the lists say "listed" or "not listed", the live sources what they saw. `short`: the narrow
 * column of the word now, where a block on some networks is "some networks".
 */
export function verdictWord(t: T, source: ReachSource, v: CountryVerdict, short = false): string {
  if (source === "lists") return v === CountryVerdict.LISTED ? t("cr.v.listed") : t("rc.st.notListed");
  if (v === CountryVerdict.PARTIAL) return t(short ? "cr.v.partial" : "cr.v.partialLong");
  return v === CountryVerdict.OK ? t("cr.v.ok") : v === CountryVerdict.TCP_BLOCKED ? t("cr.v.tcp") : v === CountryVerdict.BLOCKED ? t("cr.v.blocked") : v === CountryVerdict.LISTED ? t("cr.v.listed") : t("cr.v.unknown");
}

/**
 * What holds a source's word back, in the board's order: a source in trouble ("no fresh data"), then the first round still running
 * ("waits for a round"). Neither says anything of the node, so both are the quiet tone.
 */
export type Hold = "stale" | "wait" | "";

/** What one source says of one node now, in its word and tone; off, held back or silent it is quiet. `none`: the word for a source that has not said anything yet. */
export function sayOf(t: T, source: ReachSource, on: boolean, lane: Lane | undefined, probe: boolean, now: number, hold: Hold = "", none = t("hl.cell.none")): { word: string; tone: Tone } {
  if (!on) return { word: t("rc.st.off"), tone: "mut" };
  if (hold) return { word: t(hold === "stale" ? "rp.st.stale" : "rp.st.wait"), tone: "mut" };
  const v = laneNow(lane, now);
  if (v == null) return { word: probe ? t("rc.st.probeNode") : none, tone: "mut" };
  return { word: verdictWord(t, source, v, true), tone: toneOf(verdictKind(v)) };
}

// ---- the week as a bar ---------------------------------------------------------------------------------------------------------------

/** One stretch of a source's week: what it said (`none`: nothing seen), how long it lasted, and what a hover says of it. */
export type Stretch = { kind: "ok" | "part" | "bad" | "reg" | "none"; sec: number; title: string };

const stretchKind = (v: CountryVerdict): Stretch["kind"] => {
  const k = verdictKind(v);
  return k === "ok" ? "ok" : k === "partial" || k === "warn" ? "part" : k === "bad" ? "bad" : k === "listed" ? "reg" : "none";
};

/**
 * A lane over [since, now] as stretches that fill it: what the source said, and the empty ones before, between and after (nothing was
 * seen). The same word twice in a row is one stretch. A lane that is off or has no points is one empty stretch.
 */
export function weekBar(t: T, fmt: Fmt, source: ReachSource, lane: Lane | undefined, on: boolean, since: number, now: number): Stretch[] {
  type Run = { kind: Stretch["kind"]; verdict: CountryVerdict | null; from: number; to: number };
  const runs: Run[] = [];
  const push = (kind: Stretch["kind"], verdict: CountryVerdict | null, from: number, to: number) => {
    if (to <= from) return;
    const last = runs.at(-1);
    if (last && last.kind === kind && last.verdict === verdict) last.to = to;
    else runs.push({ kind, verdict, from, to });
  };
  let at = since;
  for (const s of on && lane ? segmentsOf(lane, since, now) : []) {
    if (s.to <= s.from) continue;
    push("none", null, at, s.from);
    // a verdict that says nothing is an empty stretch like the gaps
    const kind = stretchKind(s.verdict);
    push(kind, kind === "none" ? null : s.verdict, s.from, s.to);
    at = Math.max(at, s.to);
  }
  push("none", null, at, now);
  if (!runs.length) return [{ kind: "none", sec: Math.max(1, now - since), title: "" }];
  return runs.map((r) => {
    const range = r.to >= now - 60 ? t("rc.seg.toNow", { from: fmt.dateTime(r.from) }) : t("rc.seg.range", { from: fmt.dateTime(r.from), to: fmt.dateTime(r.to) });
    return { kind: r.kind, sec: r.to - r.from, title: r.verdict == null ? "" : `${range}: ${verdictWord(t, source, r.verdict)}` };
  });
}

/**
 * When the node's present trouble began: the start of the last run of non-reachable verdicts (any source) that reaches now, the sources
 * overlapping or following each other. 0 when nothing is wrong now.
 */
export function startedAt(lanes: readonly Lane[], now: number): number {
  const spans: [number, number][] = [];
  for (const lane of lanes)
    lane.points.forEach((p, i) => {
      const kind = verdictKind(p.verdict);
      if (kind === "ok" || kind === "unknown") return;
      spans.push([p.atUnix, Math.min(lane.points[i + 1]?.atUnix ?? lane.untilUnix, now)]);
    });
  spans.sort((a, b) => a[0] - b[0]);
  const runs: [number, number][] = [];
  for (const s of spans) {
    const last = runs.at(-1);
    if (last && s[0] <= last[1] + 60) last[1] = Math.max(last[1], s[1]);
    else runs.push([s[0], s[1]]);
  }
  const live = runs.filter((r) => r[1] >= now - 120).at(-1);
  return live ? live[0] : 0;
}

// ---- the networks --------------------------------------------------------------------------------------------------------------------

/** A network, a unit or an own probe that saw (or did not see) the node: its name, where and what it is, and the source that said so. */
export type NetItem = { name: string; meta: string; source: string };
export type NetGroup = { kind: "bad" | "ok" | "none"; n: number; items: NetItem[] };

const rank: Record<string, number> = { dark: 3, whitelist: 2, uncertain: 1, reach: 0 };

/**
 * The networks of a check: those that do not see the node (dark, or under a white list), those that do, those without a clear answer.
 * The same network of one source is one line (the worse state wins). The probe node of the TSPU source is one more entry when the
 * check has its verdict and no vantage of its own.
 */
export function networksOf(t: T, fmt: Fmt, c: CountryCheck | undefined): { groups: NetGroup[]; seen: number; total: number; skip: string } {
  const lines = new Map<string, { v: Vantage; state: string; item: NetItem }>();
  for (const v of c?.vantages ?? []) {
    const kind = v.kind && hasKey(`cr.kind.${v.kind}`) ? t(`cr.kind.${v.kind}` as MessageKey) : "";
    const own = v.source === "probes";
    const name = v.network || v.key || "?";
    const why = v.state === "dark" || v.state === "uncertain" ? (own ? probeReason(t, v) : "") : "";
    const meta = [v.region, kind, v.state === "whitelist" ? t("bb.units.white") : "", own && v.probeName ? t("r3.probe", { name: v.probeName }) : "", why].filter(Boolean).join(" · ");
    const key = `${v.source}|${name}|${v.region}|${own ? v.probeName : ""}`;
    const seen = lines.get(key);
    if (!seen || (rank[v.state] ?? 0) > (rank[seen.state] ?? 0)) lines.set(key, { v, state: v.state, item: { name, meta, source: sourceName(t, v.source) } });
  }
  const tspu = c && c.tspuAtUnix > 0 && !(c.vantages ?? []).some((v) => v.source === "tspu");
  if (c && tspu && (c.tspuVerdict === CountryVerdict.OK || c.tspuVerdict === CountryVerdict.TCP_BLOCKED || c.tspuVerdict === CountryVerdict.BLOCKED)) {
    const ok = c.tspuVerdict === CountryVerdict.OK;
    const name = c.tspuVantageName || c.tspuVantageId || "?";
    lines.set(`tspu|${name}`, {
      v: { source: "tspu" } as Vantage,
      state: ok ? "reach" : "dark",
      item: { name, meta: [t("cr.kind.dc"), c.tspuVerdict === CountryVerdict.TCP_BLOCKED ? verdictWord(t, "tspu", c.tspuVerdict) : ""].filter(Boolean).join(" · "), source: sourceName(t, "tspu") },
    });
  }
  const of = (pick: (s: string) => boolean) =>
    [...lines.values()]
      .filter((l) => pick(l.state))
      .sort((a, b) => sourceRank(a.v.source) - sourceRank(b.v.source) || a.v.region?.localeCompare(b.v.region ?? "", fmt.locale) || a.item.name.localeCompare(b.item.name, fmt.locale))
      .map((l) => l.item);
  const bad = of((s) => s === "dark" || s === "whitelist");
  const ok = of((s) => s === "reach");
  const none = of((s) => s === "uncertain");
  const groups: NetGroup[] = [
    { kind: "bad" as const, n: bad.length, items: bad },
    { kind: "ok" as const, n: ok.length, items: ok },
    { kind: "none" as const, n: none.length, items: none },
  ].filter((g) => g.n > 0);
  const skip = c?.noProbe.length ? t("r3.net.skip", { list: c.noProbe.join(t("cr.tip.sep")) }) : "";
  return { groups, seen: ok.length, total: ok.length + bad.length, skip };
}

// ---- the verdict in words ------------------------------------------------------------------------------------------------------------

/** The lower-case name of a source inside a sentence: "Globalping, the lists and the probe agree". */
const inSentence = (t: T, s: ReachSource) => (hasKey(`r3.sl.${s}`) ? t(`r3.sl.${s}` as MessageKey) : sourceName(t, s));

/** The sources that said something of the node now (a lane that reaches now and is picked), in the order of the page; the check's own list where the week is not there. */
function sayingSources(c: CountryCheck | undefined, lanes: ReadonlyMap<string, Lane> | undefined, now: number, pick: (v: CountryVerdict) => boolean): ReachSource[] {
  const said = reachSources.filter((s) => {
    const v = laneNow(lanes?.get(s), now);
    return v != null && pick(v);
  });
  return said.length || !c ? said : reachSources.filter((s) => c.sources.includes(s));
}

/** The verdict as the board's head says it, and the line under it. The sources and networks in it are the ones the data names. */
export function verdictText(t: T, fmt: Fmt, state: State, c: CountryCheck | undefined, look: CountryLook | null, lanes: ReadonlyMap<string, Lane> | undefined, now: number, code: string): { title: string; sub: string } {
  const country = countryIn(t, code);
  switch (state) {
    case "ok": {
      const names = sayingSources(c, lanes, now, (v) => v === CountryVerdict.OK).map((s) => inSentence(t, s));
      const sources = listOf(fmt.locale, names);
      return { title: t("r3.v.ok", { country }), sub: names.length > 1 ? t("r3.vs.ok", { sources }) : names.length === 1 ? t("r3.vs.ok1", { sources }) : "" };
    }
    case "part": {
      // the operators with their city: "Operator B (Novosibirsk)"
      const nets = [...new Set((c?.vantages ?? []).filter((v) => v.state === "dark").map((v) => (v.region ? t("r3.net.at", { network: v.network || v.key, region: v.region }) : v.network || v.key)))];
      return { title: t("r3.v.part", { country }), sub: nets.length ? t("ways.seen.part", { networks: listOf(fmt.locale, nets) }) : "" };
    }
    case "tcp":
      return { title: t("r3.v.tcp"), sub: look?.conflict ? `${t("cr.tip.conflict")} · ${look.conflict}` : t("r3.vs.tcp") };
    case "bad": {
      const names = sayingSources(c, lanes, now, (v) => v === CountryVerdict.BLOCKED || v === CountryVerdict.TCP_BLOCKED).map((s) => inSentence(t, s));
      return { title: t("r3.v.bad", { country }), sub: names.length ? t.n("r3.vs.bad", names.length, { sources: listOf(fmt.locale, names) }) : "" };
    }
    case "reg":
      return { title: t("r3.v.reg"), sub: t("r3.vs.reg") };
    default:
      return { title: t("r3.v.nd"), sub: t("r3.vs.nd") };
  }
}
