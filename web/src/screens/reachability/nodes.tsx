import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Fragment, useState, type FocusEvent, type KeyboardEvent } from "react";
import { buttonClass } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Tip } from "@/components/ui/tooltip";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { clientProbeQuery, groupText, troubleText, type ClientProbeNode } from "@/lib/clientprobe";
import { cx } from "@/lib/cx";
import { useFmt, type Fmt } from "@/lib/format";
import { countryIn, countryLook, ipv6Why, type Alert, type CountryCheck, type CountryLook } from "@/lib/health";
import { ipv6Helps, ipv6State, laneNow, midnights, segmentsOf, shownSources, verdictKind, type Lane, type ReachSource, type Segment } from "@/lib/reachability";
import { entryOffered, type Ipv6Entry } from "@/lib/ways";
import { CountryMark, CountryPill, OwnPoints, PartialBreakdown } from "@/screens/health/country";
import { Gl, P } from "@/screens/health/page-parts";
import { Scope } from "./source-ui";

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

const sourceKey: Record<ReachSource, MessageKey> = {
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
function verdictWord(t: T, source: ReachSource, v: CountryVerdict, short = false): string {
  if (source === "lists") return v === CountryVerdict.LISTED ? t("cr.v.listed") : t("rc.st.notListed");
  if (v === CountryVerdict.PARTIAL) return t(short ? "cr.v.partial" : "cr.v.partialLong");
  return v === CountryVerdict.OK ? t("cr.v.ok") : v === CountryVerdict.TCP_BLOCKED ? t("cr.v.tcp") : v === CountryVerdict.BLOCKED ? t("cr.v.blocked") : v === CountryVerdict.LISTED ? t("cr.v.listed") : t("cr.v.unknown");
}

/** The columns of Globalping, the lists and the probe: what each says of the node now, in the board's three tones (the warn, the quiet, the plain). */
const columns = ["globalping", "lists", "tspu"] as const;

function cellOf(t: T, source: ReachSource, on: boolean, lane: Lane | undefined, probe: boolean, now: number): { word: string; cls: "" | "part" | "mut" } {
  if (!on) return { word: t("rc.st.off"), cls: "mut" };
  const v = laneNow(lane, now);
  if (v == null) return { word: probe ? t("rc.st.probeNode") : t("hl.cell.none"), cls: "mut" };
  const kind = verdictKind(v);
  return { word: verdictWord(t, source, v, true), cls: kind === "partial" || kind === "warn" ? "part" : kind === "unknown" ? "mut" : "" };
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
}) {
  const t = useT();
  const fmt = useFmt();
  const [opened, setOpened] = useState(openGiven ?? "");
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
                <button type="button" className="rc-r" aria-expanded={isOpen} aria-controls={`rc-d-${n.id}`} onClick={() => setOpened(isOpen ? "" : n.id)}>
                  <span className="hl-cnode">
                    {n.countryCode && <Flag code={n.countryCode} size={12} className="fl" />}
                    <b className="mono">{n.name}</b>
                  </span>
                  <span className="rc-now">
                    <CountryPill check={c} node={n.name} code={country} tag={c?.ipv6 ? "IPv4" : ""} inButton />
                    {v6 === "data" && c?.ipv6 && <CountryPill check={c.ipv6} node={n.name} code={country} tag="IPv6" inButton />}
                  </span>
                  {columns.map((s) => {
                    const cell = cellOf(t, s, sources.includes(s), nodeLanes?.get(s), s === "tspu" && n.id === probeNode, now);
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

/**
 * The open row (the board's `.rc-d`): the sources and their words (a narrow window has no columns for them), the verdict in full, what the
 * reports of kl!ck users say (their trouble, the operators, the regions), the week of each source, the IPv6 hint, the networks, the buttons.
 */
function NodeDetail(p: {
  n: NodeRow;
  check: CountryCheck | undefined;
  look: CountryLook | null;
  state: State;
  lanes: ReadonlyMap<string, Lane> | undefined;
  lane6: Lane | undefined;
  v6: ReturnType<typeof ipv6State>;
  sources: readonly string[];
  probe: boolean;
  country: string;
  since: number;
  now: number;
  owner: boolean;
  report: ClientProbeNode | undefined;
  reportsOn: boolean;
  alert: Alert | undefined;
}) {
  const t = useT();
  const fmt = useFmt();
  const { n, check: c, look, v6 } = p;
  const x = p.report?.tally;
  const trouble = x ? troubleText(t, x) : "";
  const groups = (g: ClientProbeNode["operators"]) => g.map((o) => ({ key: o.name, text: groupText(t, o.name, o.tally!), bad: !!troubleText(t, o.tally!) }));
  const trouble6 = p.state !== "ok" && p.state !== "nd";
  return (
    <div id={`rc-d-${n.id}`} className="rc-d">
      <div className="rc-dk rc-dsrc">
        <span className="rc-dt">{t("rc.col.source")}</span>
        <div className="rc-ch">
          {columns.map((s) => {
            const cell = cellOf(t, s, p.sources.includes(s), p.lanes?.get(s), s === "tspu" && p.probe, p.now);
            return (
              <i key={s} className={cell.cls}>
                {t(sourceKey[s])}: {cell.word}
              </i>
            );
          })}
        </div>
      </div>
      {look && (
        <div className="rc-dk" data-testid="reach-verdict">
          <span className="rc-dt">{t("rp.verdict")}</span>
          <b>{look.head}</b>
          {(look.note || look.line) && <em>{look.conflict ? `${t("cr.tip.conflict")} · ${look.conflict}` : [look.note, look.line].filter(Boolean).join(" · ")}</em>}
        </div>
      )}
      {p.reportsOn && (
        <>
          <div className="rc-dk">
            <span className="rc-dt">{t("rp.klick")}</span>
            <b>{x ? trouble || t("kc.ok") : t("rp.klick.none")}</b>
            {x && <em>{[t.n("kc.reports", x.reports), fmt.ago(x.lastUnix)].join(" · ")}</em>}
          </div>
          {p.report && p.report.operators.length > 0 && (
            <div className="rc-dk">
              <span className="rc-dt">{t("kc.operators")}</span>
              <div className="rc-ch">
                {groups(p.report.operators).map((g) => (
                  <i key={g.key} className={g.bad ? "bad" : ""}>
                    {g.text}
                  </i>
                ))}
              </div>
            </div>
          )}
          {p.report && p.report.regions.length > 0 && (
            <div className="rc-dk">
              <span className="rc-dt">{t("kc.regions")}</span>
              <div className="rc-ch">
                {groups(p.report.regions).map((g) => (
                  <i key={g.key} className={g.bad ? "bad" : ""}>
                    {g.text}
                  </i>
                ))}
              </div>
            </div>
          )}
        </>
      )}
      <div className="rc-dk rc-wkh">
        <span className="rc-dt">{t("rp.week")}</span>
        <div className="rc-wk">
          {shownSources(p.sources).map((src) => (
            <Fragment key={src}>
              <SourceLine source={src} on={p.sources.includes(src)} lane={p.lanes?.get(src)} probe={src === "tspu" && p.probe} since={p.since} now={p.now} />
              {src === "globalping" && v6 === "data" && <SourceLine source={src} v6 on lane={p.lane6} probe={false} since={p.since} now={p.now} />}
              {src === "globalping" && v6 === "reason" && c?.ipv6 && <Ipv6None check={c.ipv6} />}
            </Fragment>
          ))}
          <div aria-hidden className="rc-ln rc-axis">
            <span />
            <span />
            <span className="rc-ax">
              <span>{t("rc.axis.ago")}</span>
              <span>{t("rc.axis.now")}</span>
            </span>
          </div>
        </div>
      </div>
      {ipv6Helps(c) && n.ipv6 && !n.ipv6.entry && <EntryHint node={n} country={p.country} offer={p.owner && entryOffered(n.ipv6)} />}
      <Breakdown look={look} />
      <div className="hl-act">
        <Link to="/nodes/$id" params={{ id: n.id }} className={buttonClass("secondary", "md")}>
          {t("rp.openNode")}
        </Link>
        {trouble6 && (
          <Link to="/health" hash={p.alert ? `alert-${p.alert.id}` : undefined} className={buttonClass("secondary", "md")}>
            {t("rp.openAlert")}
          </Link>
        )}
      </div>
    </div>
  );
}

/**
 * "IPv4 is blocked from Russia, IPv6 gets through." under a node whose IPv6 entry is off (ipv6 plan §2.6, §3.8); `offer`: with
 * "Add IPv6 entry…", which opens the entry's dialog on the node's Ways in.
 */
function EntryHint({ node, country, offer }: { node: NodeRow; country: string; offer: boolean }) {
  const t = useT();
  return (
    <div data-testid="reach-v6-hint" className="rc-hint">
      <span>
        <Gl d={P.check} />
        {t("ways.ipv6.helps", { country: countryIn(t, country) })}
      </span>
      {offer && (
        <Link to="/nodes/$id" params={{ id: node.id }} search={{ tab: "ways", way: "ipv6", add: "ipv6" }} className={buttonClass("secondary", "md")}>
          {t("ways.ipv6.add")}
        </Link>
      )}
    </div>
  );
}

/** A verdict with networks that do not see the node: every source's networks, in full under the row; the points of an own probe where there is no breakdown. */
function Breakdown({ look }: { look: CountryLook | null }) {
  if (look && !look.breakdown && look.ownPoints.length > 0) return <OwnPoints points={look.ownPoints} className="rc-bk" />;
  if (!look?.breakdown) return null;
  return <PartialBreakdown breakdown={look.breakdown} className={cx("rc-bk", look.kind === "bad" ? "bad" : "part")} />;
}

/** The name of the IPv6 twin of Globalping's line, under it: "IPv6", the source said to a screen reader. */
function Ipv6Label() {
  const t = useT();
  return (
    <span className="rc-nm rc-v6 mono">
      <span className="sr-only">{t("rc.src.globalping")} · </span>IPv6
    </span>
  );
}

/** A node Globalping does not check over IPv6: why, on one quiet line under Globalping's lane ("The server has no IPv6"). */
function Ipv6None({ check }: { check: CountryCheck }) {
  return (
    <div data-testid="line-ipv6-none" className="rc-ln none">
      <Ipv6Label />
      <span className="rc-why">{ipv6Why(useT(), check)}</span>
    </div>
  );
}

/** One source of one node: its name, what it says now, and its week. Off, the line is dimmed and the lane is an empty dashed track. `v6`: Globalping's IPv6 lane, under its IPv4 one. */
function SourceLine(p: { source: ReachSource; v6?: boolean; on: boolean; lane: Lane | undefined; probe: boolean; since: number; now: number }) {
  const { source, v6 = false, on, lane, probe, since, now } = p;
  const t = useT();
  const v = on ? laneNow(lane, now) : null;
  const kind = v == null ? "unknown" : verdictKind(v);
  return (
    <div data-testid={`line-${source}${v6 ? "6" : ""}`} data-on={on} className={cx("rc-ln", !on && "off")}>
      {v6 ? <Ipv6Label /> : <span className="rc-nm">{t(sourceKey[source])}</span>}
      <span className={cx("rc-wd", on ? kind : "off")}>
        {!on ? (
          t("rc.st.off")
        ) : v != null ? (
          <>
            <CountryMark kind={kind} />
            <span>{verdictWord(t, source, v, true)}</span>
          </>
        ) : probe ? (
          t("rc.st.probeNode")
        ) : (
          <span title={t("cr.v.notYet")}>{t("hl.cell.none")}</span>
        )}
      </span>
      <div className="rc-lw">
        <LaneView source={source} v6={v6} on={on} lane={lane} since={since} now={now} />
      </div>
    </div>
  );
}

/** The time of a segment: "14 Oct, 09:15 – 15 Oct, 08:00", or "– now" when it lasts. */
function rangeOf(t: T, fmt: Fmt, s: Segment, now: number) {
  return s.to >= now - 60 ? t("rc.seg.toNow", { from: fmt.dateTime(s.from) }) : t("rc.seg.range", { from: fmt.dateTime(s.from), to: fmt.dateTime(s.to) });
}

/**
 * A source's week of one node: the segments in their colours over an empty track, a hair of the background at every midnight.
 * Each segment is focusable and says its verdict and time in a tooltip; the arrows walk the lane, Tab leaves it (one stop, on
 * the newest segment).
 */
export function LaneView({ source, v6 = false, on, lane, since, now }: { source: ReachSource; v6?: boolean; on: boolean; lane: Lane | undefined; since: number; now: number }) {
  const t = useT();
  const fmt = useFmt();
  const segs = on && lane ? segmentsOf(lane, since, now) : [];
  const [current, setCurrent] = useState(-1);
  const at = current >= 0 && current < segs.length ? current : segs.length - 1;
  const span = Math.max(1, now - since);

  // the segment that has focus is the lane's one Tab stop; the arrows, Home and End move it
  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    const next = { ArrowLeft: at - 1, ArrowRight: at + 1, Home: 0, End: segs.length - 1 }[e.key];
    if (next === undefined || next < 0 || next >= segs.length) return;
    e.preventDefault();
    e.currentTarget.querySelector<HTMLElement>(`[data-seg="${next}"]`)?.focus();
  }
  const onFocus = (e: FocusEvent<HTMLDivElement>) => {
    const i = Number((e.target as HTMLElement).dataset.seg);
    if (!Number.isNaN(i)) setCurrent(i);
  };

  return (
    <div role="group" aria-label={t("rc.lane", { source: v6 ? `${t(sourceKey[source])} · IPv6` : t(sourceKey[source]) })} data-testid={`lane-${source}${v6 ? "6" : ""}`} onKeyDown={onKeyDown} onFocus={onFocus} className="rc-lane">
      <div aria-hidden className={cx("rc-track", on ? "on" : "off")}>
        {segs.map((s, i) => (
          <span key={i} data-verdict={verdictKind(s.verdict)} className="rc-seg" style={{ insetInlineStart: `${s.left}%`, width: `${s.width}%` }} />
        ))}
        {on && midnights(since, now).map((m) => <span key={m} className="rc-mid" style={{ insetInlineStart: `${((m - since) / span) * 100}%` }} />)}
      </div>
      {segs.map((s, i) => {
        const word = verdictWord(t, source, s.verdict);
        const range = rangeOf(t, fmt, s, now);
        const kind = verdictKind(s.verdict);
        return (
          <Tip
            key={i}
            label={
              <span className="flex flex-col gap-0.5">
                <span className="flex items-center gap-1.5">
                  <CountryMark kind={kind} />
                  {word}
                </span>
                <span className="font-medium text-muted">{range}</span>
                <span className="font-medium text-muted">{fmt.duration(s.to - s.from)}</span>
              </span>
            }
          >
            <span data-seg={i} role="img" tabIndex={i === at ? 0 : -1} aria-label={t("rc.seg", { verdict: word, range })} className="rc-hit" style={{ insetInlineStart: `${s.left}%`, width: `${s.width}%` }} />
          </Tip>
        );
      })}
    </div>
  );
}
