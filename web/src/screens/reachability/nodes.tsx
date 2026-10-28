import { Link } from "@tanstack/react-router";
import { Fragment, useState, type FocusEvent, type KeyboardEvent } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Tip } from "@/components/ui/tooltip";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { useFmt, type Fmt } from "@/lib/format";
import { countryLook, ipv6Why, type CountryCheck } from "@/lib/health";
import { ipv6State, laneNow, midnights, segmentsOf, shownSources, verdictKind, type Lane, type ReachSource, type Segment } from "@/lib/reachability";
import { CountryMark, CountryPill, PartialBreakdown } from "@/screens/health/country";

// The nodes of the Reachability page: a row per node with its verdict now (the pill of the Health column, with its tooltip), then
// a line per source: its word for the node now and its week as a thin lane of verdicts. Globalping, the lists and the probe stay
// in place while off, dimmed, so the rows keep their shape; cheburcheck and bsbord have a line while they are on. Below 1280 px (the sidebar takes its part) a row is a card: the verdict on top, the
// source lines under it, each lane on a line of its own on a phone. While Globalping checks IPv6 as well, the verdict pill says
// "IPv4" and an "IPv6" pill follows it, and Globalping's lane gets its IPv6 twin under it; a node without an IPv6 verdict gets
// one quiet line there instead, with the reason (no IPv6 on the server, broken at the hoster, an old agent, no probes).

export type NodeRow = { id: string; name: string; countryCode: string };

const sourceKey: Record<ReachSource, MessageKey> = {
  globalping: "rc.src.globalping",
  lists: "rc.src.lists",
  tspu: "rc.src.tspu",
  cheburcheck: "rc.src.cheburcheck",
  cheburcheck_own: "rc.src.cheburcheck_own",
  bsbord: "rc.src.bsbord",
};

/**
 * What one source calls a verdict: the lists say "listed" or "not listed", the live sources what they saw. `short`: the narrow
 * column of the word now, where a block on some networks is "some networks" beside its half disc.
 */
function verdictWord(t: T, source: ReachSource, v: CountryVerdict, short = false): string {
  if (source === "lists") return v === CountryVerdict.LISTED ? t("cr.v.listed") : t("rc.st.notListed");
  if (v === CountryVerdict.PARTIAL) return t(short ? "cr.v.partial" : "cr.v.partialLong");
  return v === CountryVerdict.OK ? t("cr.v.ok") : v === CountryVerdict.TCP_BLOCKED ? t("cr.v.tcp") : v === CountryVerdict.BLOCKED ? t("cr.v.blocked") : t("cr.v.unknown");
}

// A lane's colours: OK a soft mint, the blocks their full status colour, a block on some networks split (the warn colour over
// the mint: blocked on some, reachable on the rest), "listed" the warn colour striped (on record, not seen), no data a grey. The
// empty track under them is the time nobody looked.
const segCls: Record<ReturnType<typeof verdictKind>, string> = {
  ok: "tone-ok bg-(--c)/40",
  partial: "tone-warn bg-[linear-gradient(var(--c)_50%,color-mix(in_oklch,var(--accent-mint)_40%,transparent)_50%)]",
  warn: "tone-warn bg-(--c)",
  bad: "tone-bad bg-(--c)",
  listed: "tone-warn bg-(--c)/15 bg-[repeating-linear-gradient(-45deg,color-mix(in_oklch,var(--c)_55%,transparent)_0_2px,transparent_2px_6px)]",
  unknown: "tone-off bg-(--c)/35",
};

const textCls: Record<ReturnType<typeof verdictKind>, string> = {
  ok: "text-muted",
  partial: "tone-warn tone-text",
  warn: "tone-warn tone-text",
  bad: "tone-bad tone-text",
  listed: "tone-warn tone-text",
  unknown: "text-muted",
};

// The columns: node, verdict, then the source lines (name, word, lane). The header uses the same, so the lanes line up with the days.
const rowCols = "xl:grid xl:grid-cols-[minmax(84px,128px)_minmax(120px,168px)_minmax(0,1fr)] xl:gap-x-4";
const lineCols = "grid grid-cols-[88px_minmax(0,1fr)] items-center gap-x-3 gap-y-1 md:grid-cols-[88px_120px_minmax(0,1fr)]";

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
}) {
  const t = useT();
  return (
    <section aria-labelledby="reach-nodes" tabIndex={-1} className="flex scroll-mt-20 flex-col rounded-card-lg border border-line bg-surface px-4 pt-3.5 pb-1.5 outline-none">
      <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1 pb-3">
        <SectionLabel as="h2" icon="server" tone="sky">
          <span id="reach-nodes">{t("rc.nodes.title")}</span>
        </SectionLabel>
        <span className="text-xs text-muted">{t("rc.nodes.sub")}</span>
      </div>
      <div aria-hidden className={cx("hidden pb-1.5 text-[11px] font-bold text-muted", rowCols)}>
        <span>{t("rc.col.node")}</span>
        <span>{t("rc.col.now")}</span>
        <div className={lineCols}>
          <span>{t("rc.col.source")}</span>
          <span />
          <DayAxis since={since} now={now} />
        </div>
      </div>
      <ul aria-label={t("rc.nodes.title")} className="flex flex-col">
        {nodes.map((n) => {
          const c = checks.get(n.id);
          const v6 = ipv6State(c?.ipv6, lanes6.get(n.id));
          return (
            <li key={n.id} data-testid="reach-node" className={cx("flex flex-col gap-2.5 border-t border-line py-3", rowCols)}>
              {/* a card: the name, and the verdicts at the right under each other (IPv4 keeps its place in every row); the wide screen: each in its column */}
              <div className="flex items-start justify-between gap-3 xl:contents">
                <Link to="/nodes/$id" params={{ id: n.id }} className="flex min-w-0 items-center gap-2 pt-1 hover:text-accent-text xl:self-start">
                  <span className="w-5 flex-none font-mono text-[10px] font-bold text-muted">{n.countryCode}</span>
                  <span className="truncate font-mono text-[13px] font-bold">{n.name}</span>
                </Link>
                <div className="flex flex-none flex-col items-end gap-1.5 xl:items-start xl:self-start">
                  <CountryPill check={c} node={n.name} code={country} tag={c?.ipv6 ? "IPv4" : ""} />
                  {v6 === "data" && c?.ipv6 && <CountryPill check={c.ipv6} node={n.name} code={country} tag="IPv6" />}
                </div>
              </div>
              <div className="flex flex-col gap-1.5">
                {shownSources(sources).map((src) => (
                  <Fragment key={src}>
                    <SourceLine source={src} on={sources.includes(src)} lane={lanes.get(n.id)?.get(src)} probe={src === "tspu" && n.id === probeNode} since={since} now={now} />
                    {src === "globalping" && v6 === "data" && <SourceLine source={src} v6 on lane={lanes6.get(n.id)} probe={false} since={since} now={now} />}
                    {src === "globalping" && v6 === "reason" && c?.ipv6 && <Ipv6None check={c.ipv6} />}
                  </Fragment>
                ))}
                <div aria-hidden className={cx(lineCols, "text-[10px] text-faint xl:hidden")}>
                  <span className="max-md:hidden" />
                  <span className="max-md:hidden" />
                  <span className="col-span-2 flex justify-between md:col-span-1">
                    <span>{t("rc.axis.ago")}</span>
                    <span>{t("rc.axis.now")}</span>
                  </span>
                </div>
              </div>
              <RowBreakdown check={c} />
            </li>
          );
        })}
      </ul>
    </section>
  );
}

/** A verdict with networks that do not see the node: every source's networks behind a disclosure under the row, across all the columns. */
function RowBreakdown({ check }: { check: CountryCheck | undefined }) {
  const t = useT();
  const fmt = useFmt();
  const look = check ? countryLook(t, fmt, check) : null;
  if (!look?.breakdown) return null;
  return (
    <details data-testid="reach-breakdown" className="xl:col-span-3">
      <summary className="w-fit cursor-pointer text-xs font-semibold text-muted hover:text-fg">{t("rc.part.more")}</summary>
      <PartialBreakdown breakdown={look.breakdown} className={cx("mt-2 max-w-xl rounded-field tint px-3 py-2.5", look.kind === "bad" ? "tone-bad" : "tone-warn")} />
    </details>
  );
}

/** The days over the lanes: the date in the middle of each day that is wide enough. The lanes end now, as the column "Now" says. */
function DayAxis({ since, now }: { since: number; now: number }) {
  const fmt = useFmt();
  const span = Math.max(1, now - since);
  const edges = [since, ...midnights(since, now), now];
  return (
    <div className="relative h-4 overflow-hidden">
      {edges.slice(0, -1).map((from, i) => {
        const to = edges[i + 1]!;
        const mid = ((from + to) / 2 - since) / span;
        if ((to - from) / span < 0.09) return null; // a sliver of a day has no room for its date
        return (
          <span key={from} className="absolute top-0 -translate-x-1/2 font-medium whitespace-nowrap" style={{ left: `${mid * 100}%` }}>
            {fmt.date(to - 1)}
          </span>
        );
      })}
    </div>
  );
}

/**
 * The name of the IPv6 twin of Globalping's line, under it: "IPv6", indented, the source said to a screen reader. The quiet line
 * of a node without an IPv6 verdict carries it too, so both sit where the lane would.
 */
function Ipv6Label() {
  const t = useT();
  return (
    <span className="truncate pl-3 font-mono text-[11px] font-bold text-muted">
      <span className="sr-only">{t("rc.src.globalping")} · </span>IPv6
    </span>
  );
}

/** A node Globalping does not check over IPv6: why, on one quiet line under Globalping's lane ("The server has no IPv6"). */
function Ipv6None({ check }: { check: CountryCheck }) {
  const t = useT();
  return (
    <div data-testid="line-ipv6-none" className={lineCols.replace("items-center", "items-baseline")}>
      <Ipv6Label />
      <span className="min-w-0 text-xs leading-snug text-pretty text-muted md:col-span-2">{ipv6Why(t, check)}</span>
    </div>
  );
}

/**
 * One source of one node: its name, what it says now, and its week. Off, the line is dimmed and the lane is an empty dashed track.
 * `v6`: Globalping's IPv6 lane, under its IPv4 one.
 */
function SourceLine(p: { source: ReachSource; v6?: boolean; on: boolean; lane: Lane | undefined; probe: boolean; since: number; now: number }) {
  const { source, v6 = false, on, lane, probe, since, now } = p;
  const t = useT();
  const v = on ? laneNow(lane, now) : null;
  const kind = v == null ? "unknown" : verdictKind(v);
  return (
    <div data-testid={`line-${source}${v6 ? "6" : ""}`} data-on={on} className={cx(lineCols, !on && "opacity-55")}>
      {v6 ? <Ipv6Label /> : <span className="truncate text-xs font-semibold">{t(sourceKey[source])}</span>}
      <span className={cx("flex min-w-0 items-center gap-1.5 text-xs font-bold", on ? textCls[kind] : "text-faint")}>
        {!on ? (
          t("rc.st.off")
        ) : v != null ? (
          <>
            <CountryMark kind={kind} />
            <span className="truncate">{verdictWord(t, source, v, true)}</span>
          </>
        ) : probe ? (
          t("rc.st.probeNode")
        ) : (
          <span title={t("cr.v.notYet")}>{t("hl.cell.none")}</span>
        )}
      </span>
      <div className="col-span-2 md:col-span-1">
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
    <div
      role="group"
      aria-label={t("rc.lane", { source: v6 ? `${t(sourceKey[source])} · IPv6` : t(sourceKey[source]) })}
      data-testid={`lane-${source}${v6 ? "6" : ""}`}
      onKeyDown={onKeyDown} onFocus={onFocus} className="relative h-4">
      <div aria-hidden className={cx("absolute inset-x-0 top-1/2 h-2 -translate-y-1/2 overflow-hidden rounded-full", on ? "bg-surface-2" : "border border-dashed border-line")}>
        {segs.map((s, i) => (
          <span key={i} data-verdict={verdictKind(s.verdict)} className={cx("absolute inset-y-0 min-w-[3px]", segCls[verdictKind(s.verdict)])} style={{ left: `${s.left}%`, width: `${s.width}%` }} />
        ))}
        {on && midnights(since, now).map((m) => <span key={m} className="absolute inset-y-0 w-px bg-surface" style={{ left: `${((m - since) / span) * 100}%` }} />)}
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
            <span
              data-seg={i}
              role="img"
              tabIndex={i === at ? 0 : -1}
              aria-label={t("rc.seg", { verdict: word, range })}
              className="absolute inset-y-0 min-w-[3px] rounded-[3px]"
              style={{ left: `${s.left}%`, width: `${s.width}%` }}
            />
          </Tip>
        );
      })}
    </div>
  );
}
