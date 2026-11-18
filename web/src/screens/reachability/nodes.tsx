import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { buttonClass } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { clientProbeQuery } from "@/lib/clientprobe";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { countryLook, type Alert, type CountryCheck, type CountryLook } from "@/lib/health";
import { ipv6State, segmentsOf, shownSources, verdictKind, type Lane, type ReachSource } from "@/lib/reachability";
import type { Ipv6Entry } from "@/lib/ways";
import { CountryPill } from "@/screens/health/country";
import { Gl, P } from "@/screens/health/page-parts";
import { sayOf, sourceKey, verdictWord, type Hold } from "./detail-model";
import { NodeDetail } from "./node-detail";
import { Scope } from "./source-ui";
import "./reach.css";

// The nodes of the Reachability page, as the board draws them (PnReach): the summary card (the count, the states as a filter, the bar of
// their shares) and the table, a row per node with its verdict now, the word of Globalping, of the lists and of the probe, and the week as
// seven cells. A row opens (one at a time) into what the board shows there: the sources, the reports of kl!ck users with their operators
// and regions, the buttons; and what the page has besides: the verdict in full, the week of each source as lanes with their moments, the
// IPv6 side, the hint for the IPv6 entry, the networks of a partial block, the points of the own probes. While Globalping checks IPv6 as
// well, the verdict says "IPv4" and an "IPv6" pill follows it.

export type NodeRow = { id: string; name: string; countryCode: string; ipv6?: Ipv6Entry };

/** The states of the board's summary, in its order; each is the board's class of the chip, the bar, the pill's word and the day's cell. */
export const stateKinds = ["ok", "part", "tcp", "bad", "reg", "nd"] as const;
export type State = (typeof stateKinds)[number];
export const stateOf: Record<CountryLook["kind"], State> = { ok: "ok", partial: "part", warn: "tcp", bad: "bad", listed: "reg", unknown: "nd" };
/** The chip's word: the verdict's own words, the short one for a block on some networks. */
export const stateWord: Record<State, MessageKey> = { ok: "rc.sum.ok", part: "rp.chip.part", tcp: "rc.sum.warn", bad: "rc.sum.bad", reg: "rc.sum.listed", nd: "rc.sum.unknown" };

/** The columns of Globalping, the lists and the probe: what each says of the node now, in the board's three tones (the warn, the quiet, the plain). */
const columns = ["globalping", "lists", "tspu"] as const;

/** A cell of the row: the source's word now; the quiet tone says nothing of the node, the warn one is a block on some networks or of TCP. */
function cellOf(t: T, source: ReachSource, on: boolean, lane: Lane | undefined, probe: boolean, now: number, hold: Hold = ""): { word: string; cls: "" | "part" | "mut" } {
  const say = sayOf(t, source, on, lane, probe, now, hold);
  return { word: say.word, cls: say.tone === "part" ? "part" : say.tone === "mut" ? "mut" : "" };
}

/** One of the seven days of the strip: the worst verdict any source had on it; nothing seen is an empty cell. */
export type Day = { from: number; to: number; verdict: CountryVerdict | null };

const worse = (a: CountryVerdict | null, b: CountryVerdict): CountryVerdict => {
  const rank = (v: CountryVerdict | null) => (v == null ? -1 : ({ ok: 1, partial: 2, warn: 2, bad: 3, listed: 3, unknown: 0 } as const)[verdictKind(v)]);
  return rank(b) > rank(a) ? b : (a ?? b);
};

/** The last seven days (today the last, from midnight to now) with the worst verdict of each across the lanes. */
export function daysOf(lanes: readonly Lane[], now: number): Day[] {
  const day = new Date(now * 1000);
  day.setHours(0, 0, 0, 0);
  const days: Day[] = [];
  for (let i = 6; i >= 0; i--) {
    const from = new Date(day);
    from.setDate(day.getDate() - i);
    const next = new Date(from);
    next.setDate(from.getDate() + 1);
    const a = from.getTime() / 1000;
    const b = i === 0 ? now : next.getTime() / 1000;
    let verdict: CountryVerdict | null = null;
    for (const lane of lanes)
      for (const s of segmentsOf(lane, a, b)) if (s.to > s.from || (s.from >= a && s.from < b)) if (verdictKind(s.verdict) !== "unknown") verdict = worse(verdict, s.verdict);
    days.push({ from: a, to: b, verdict });
  }
  return days;
}

/** The cell's class: the board's three colours. */
const dayClass = (v: CountryVerdict | null) => (v == null ? "" : ({ ok: "ok", partial: "part", warn: "part", bad: "bad", listed: "bad", unknown: "" } as const)[verdictKind(v)]);

function daysLabel(t: T, days: readonly Day[]): string {
  const seen = days.filter((d) => d.verdict != null);
  if (seen.length === 0) return t("rp.days.none");
  const trouble = seen.filter((d) => dayClass(d.verdict) !== "ok");
  if (trouble.length === 0) return t("rp.days.ok", { n: seen.length });
  const last = days[days.length - 1]!;
  return t("rp.days.mixed", { n: trouble.length, today: last.verdict == null ? t("cr.v.unknown") : verdictWord(t, "globalping", last.verdict) });
}

export function NodesTable({
  nodes,
  checks,
  lanes,
  lanes6,
  sources,
  probeNode,
  country,
  since,
  now,
  owner = false,
  filter = "",
  onShowAll,
  alerts = [],
  open: openGiven,
  first = false,
  stale,
}: {
  nodes: readonly NodeRow[];
  checks: ReadonlyMap<string, CountryCheck>;
  lanes: ReadonlyMap<string, ReadonlyMap<string, Lane>>;
  /** Globalping's IPv6 lane of each node */
  lanes6: ReadonlyMap<string, Lane>;
  sources: readonly string[];
  probeNode: string;
  country: string;
  since: number;
  now: number;
  /** The owner adds an IPv6 entry from a row; everybody else reads the hint. */
  owner?: boolean;
  /** Only the nodes in this state (a chip of the summary); "" shows them all. */
  filter?: State | "";
  /** The way out of an empty filter. */
  onShowAll?: () => void;
  /** The open country alerts: the row's "Open alert" leads to its own. */
  alerts?: readonly Alert[];
  /** The node whose row is open from the start (a link, the dev kit). */
  open?: string;
  /** The first round of checks is still running: the sources wait for it. */
  first?: boolean;
  /** The sources in trouble: their last words are old. */
  stale?: ReadonlySet<string>;
}) {
  const t = useT();
  const fmt = useFmt();
  const [chosen, setChosen] = useState<string | null>(openGiven ?? null);
  const holdOf = (s: ReachSource): Hold => (stale?.has(s) ? "stale" : first ? "wait" : "");
  const klick = useQuery({ ...clientProbeQuery, enabled: owner });
  const reports = klick.data?.settings?.enabled ? new Map((klick.data.nodes ?? []).map((n) => [n.nodeId, n])) : null;
  const head = (
    <div className="rc-r rc-h">
      <span>{t("rc.col.node")}</span>
      <span>{t("rc.col.now")}</span>
      {columns.map((c) => (
        <span key={c}>{t(sourceKey[c])}</span>
      ))}
      <span className="rc-dh">
        <i>{t("rc.axis.ago")}</i>
        <i>{t("rc.axis.now")}</i>
      </span>
      <span />
    </div>
  );
  const lookOf = (id: string) => {
    const c = checks.get(id);
    return c ? countryLook(t, fmt, c) : null;
  };
  const shown = nodes.filter((n) => !filter || stateOf[lookOf(n.id)?.kind ?? "unknown"] === filter);
  // until a row is chosen the board has one open: the first that is not all right (else the first), as Health opens its first critical
  const opened = chosen ?? (shown.find((n) => stateOf[lookOf(n.id)?.kind ?? "unknown"] !== "ok") ?? shown[0])?.id ?? "";
  return (
    <Scope>
      <section className="card pp-sec sb-sec rc-main">
        <div className="pp-sh">
          <span className="pp-ic sky" aria-hidden="true">
            <Gl d={P.server} />
          </span>
          <h3>{t("rc.nodes.title")}</h3>
          <span className="sb-cnt2">{t("rc.nodes.sub")}</span>
        </div>
        <div className="rc-t" role="list" aria-label={t("rc.nodes.title")}>
          <div aria-hidden="true">{head}</div>
          {shown.map((n) => {
            const c = checks.get(n.id);
            const look = lookOf(n.id);
            const state = stateOf[look?.kind ?? "unknown"];
            const v6 = ipv6State(c?.ipv6, lanes6.get(n.id));
            const isOpen = opened === n.id;
            const nodeLanes = lanes.get(n.id);
            const days = daysOf(shownSources(sources).flatMap((s) => nodeLanes?.get(s) ?? []), now);
            const label = daysLabel(t, days);
            return (
              <div key={n.id} role="listitem" data-testid="reach-node" data-state={state} className={cx("rc-w", isOpen && "open")}>
                <button type="button" className="rc-r" aria-expanded={isOpen} aria-controls={`rc-d-${n.id}`} onClick={() => setChosen(isOpen ? "" : n.id)}>
                  <span className="hl-cnode">
                    {n.countryCode && <Flag code={n.countryCode} size={12} className="fl" />}
                    <b className="mono">{n.name}</b>
                  </span>
                  <span className="rc-now">
                    <CountryPill check={c} node={n.name} code={country} tag={c?.ipv6 ? "IPv4" : ""} inButton />
                    {v6 === "data" && c?.ipv6 && <CountryPill check={c.ipv6} node={n.name} code={country} tag="IPv6" inButton />}
                  </span>
                  {columns.map((s) => {
                    const cell = cellOf(t, s, sources.includes(s), nodeLanes?.get(s), s === "tspu" && n.id === probeNode, now, holdOf(s));
                    return (
                      <span key={s} className={cx("rc-v", cell.cls)} data-l={t(sourceKey[s])} data-testid={`cell-${s}`}>
                        {cell.word}
                      </span>
                    );
                  })}
                  <span className="rc-days" role="img" aria-label={label}>
                    {days.map((d) => (
                      <i key={d.from} className={dayClass(d.verdict)} title={`${fmt.date(d.from)}: ${d.verdict == null ? t("cr.v.unknown") : verdictWord(t, "globalping", d.verdict)}`} />
                    ))}
                  </span>
                  <Gl d={P.chev} className="sb-chev" />
                </button>
                {isOpen && (
                  <NodeDetail
                    n={n}
                    check={c}
                    look={look}
                    state={state}
                    lanes={nodeLanes}
                    lane6={lanes6.get(n.id)}
                    v6={v6}
                    sources={sources}
                    probe={n.id === probeNode}
                    country={country}
                    since={since}
                    now={now}
                    owner={owner}
                    report={reports?.get(n.id)}
                    reportsOn={!!reports}
                    holdOf={holdOf}
                    alert={alerts.find((a) => a.nodeId === n.id && a.resolvedAtUnix === 0)}
                  />
                )}
              </div>
            );
          })}
          {shown.length === 0 && (
            <div className="pv-nf">
              <p>{t("rp.none")}</p>
              {onShowAll && (
                <button type="button" className={buttonClass("secondary", "md")} onClick={onShowAll}>
                  {t("rp.showAll")}
                </button>
              )}
            </div>
          )}
        </div>
        <p className="sb-vfoot">
          {(["ok", "part", "bad", "nd"] as const).map((k) => (
            <span key={k} className="rc-lg">
              <i className={k === "nd" ? "" : k} />
              {t(({ ok: "rp.lg.ok", part: "rp.lg.part", bad: "rp.lg.bad", nd: "rp.lg.nd" } as const)[k])}
            </span>
          ))}
        </p>
      </section>
    </Scope>
  );
}
