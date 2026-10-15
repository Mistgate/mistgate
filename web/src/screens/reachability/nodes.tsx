import { Link } from "@tanstack/react-router";
import { useState, type FocusEvent, type KeyboardEvent } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Tip } from "@/components/ui/tooltip";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { useFmt, type Fmt } from "@/lib/format";
import type { CountryCheck } from "@/lib/health";
import { laneNow, midnights, reachSources, segmentsOf, verdictKind, type Lane, type ReachSource, type Segment } from "@/lib/reachability";
import { CountryMark, CountryPill } from "@/screens/health/country";

// The nodes of the Reachability page: a row per node with its verdict now (the pill of the Health column, with its tooltip), then
// a line per source: its word for the node now and its week as a thin lane of verdicts. A source that is off stays in place,
// dimmed, so the rows keep their shape. Below 1280 px (the sidebar takes its part) a row is a card: the verdict on top, the
// source lines under it, each lane on a line of its own on a phone.

export type NodeRow = { id: string; name: string; countryCode: string };

const sourceKey: Record<ReachSource, MessageKey> = { globalping: "rc.src.globalping", lists: "rc.src.lists", tspu: "rc.src.tspu" };

/** What one source calls a verdict: the lists say "listed" or "not listed", the live sources what they saw. */
function verdictWord(t: T, source: ReachSource, v: CountryVerdict): string {
  if (source === "lists") return v === CountryVerdict.LISTED ? t("cr.v.listed") : t("rc.st.notListed");
  return v === CountryVerdict.OK ? t("cr.v.ok") : v === CountryVerdict.TCP_BLOCKED ? t("cr.v.tcp") : v === CountryVerdict.BLOCKED ? t("cr.v.blocked") : t("cr.v.unknown");
}

// A lane's colours: OK a soft mint, the blocks their full status colour, "listed" the warn colour striped (on record, not seen),
// no data a grey. The empty track under them is the time nobody looked.
const segCls: Record<ReturnType<typeof verdictKind>, string> = {
  ok: "tone-ok bg-(--c)/40",
  warn: "tone-warn bg-(--c)",
  bad: "tone-bad bg-(--c)",
  listed: "tone-warn bg-(--c)/15 bg-[repeating-linear-gradient(-45deg,color-mix(in_oklch,var(--c)_55%,transparent)_0_2px,transparent_2px_6px)]",
  unknown: "tone-off bg-(--c)/35",
};

const textCls: Record<ReturnType<typeof verdictKind>, string> = {
  ok: "text-muted",
  warn: "tone-warn tone-text",
  bad: "tone-bad tone-text",
  listed: "tone-warn tone-text",
  unknown: "text-muted",
};

// The columns: node, verdict, then the source lines (name, word, lane). The header uses the same, so the lanes line up with the days.
const rowCols = "xl:grid xl:grid-cols-[minmax(84px,128px)_minmax(120px,168px)_minmax(0,1fr)] xl:gap-x-4";
const lineCols = "grid grid-cols-[76px_minmax(0,1fr)] items-center gap-x-3 gap-y-1 md:grid-cols-[72px_120px_minmax(0,1fr)]";

export function NodesTable({
  nodes,
  checks,
  lanes,
  sources,
  probeNode,
  country,
  since,
  now,
}: {
  nodes: readonly NodeRow[];
  checks: ReadonlyMap<string, CountryCheck>;
  lanes: ReadonlyMap<string, ReadonlyMap<string, Lane>>;
  sources: readonly string[];
  probeNode: string;
  country: string;
  since: number;
  now: number;
}) {
  const t = useT();
  return (
    <section aria-labelledby="reach-nodes" className="flex flex-col rounded-card-lg border border-line bg-surface px-4 pt-3.5 pb-1.5">
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
        {nodes.map((n) => (
          <li key={n.id} data-testid="reach-node" className={cx("flex flex-col gap-2.5 border-t border-line py-3", rowCols)}>
            {/* a card: name and verdict on one line; the wide screen: each in its column */}
            <div className="flex items-start justify-between gap-3 xl:contents">
              <Link to="/nodes/$id" params={{ id: n.id }} className="flex min-w-0 items-center gap-2 pt-1 hover:text-accent-text xl:self-start">
                <span className="w-5 flex-none font-mono text-[10px] font-bold text-muted">{n.countryCode}</span>
                <span className="truncate font-mono text-[13px] font-bold">{n.name}</span>
              </Link>
              <div className="xl:self-start">
                <CountryPill check={checks.get(n.id)} node={n.name} code={country} />
              </div>
            </div>
            <div className="flex flex-col gap-1.5">
              {reachSources.map((src) => (
                <SourceLine key={src} source={src} on={sources.includes(src)} lane={lanes.get(n.id)?.get(src)} probe={src === "tspu" && n.id === probeNode} since={since} now={now} />
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
          </li>
        ))}
      </ul>
    </section>
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

/** One source of one node: its name, what it says now, and its week. Off, the line is dimmed and the lane is an empty dashed track. */
function SourceLine({ source, on, lane, probe, since, now }: { source: ReachSource; on: boolean; lane: Lane | undefined; probe: boolean; since: number; now: number }) {
  const t = useT();
  const v = on ? laneNow(lane, now) : null;
  const kind = v == null ? "unknown" : verdictKind(v);
  return (
    <div data-testid={`line-${source}`} data-on={on} className={cx(lineCols, !on && "opacity-55")}>
      <span className="truncate text-xs font-semibold">{t(sourceKey[source])}</span>
      <span className={cx("flex min-w-0 items-center gap-1.5 text-xs font-bold", on ? textCls[kind] : "text-faint")}>
        {!on ? (
          t("rc.st.off")
        ) : v != null ? (
          <>
            <CountryMark kind={kind} />
            <span className="truncate">{verdictWord(t, source, v)}</span>
          </>
        ) : probe ? (
          t("rc.st.probeNode")
        ) : (
          <span title={t("cr.v.notYet")}>{t("hl.cell.none")}</span>
        )}
      </span>
      <div className="col-span-2 md:col-span-1">
        <LaneView source={source} on={on} lane={lane} since={since} now={now} />
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
export function LaneView({ source, on, lane, since, now }: { source: ReachSource; on: boolean; lane: Lane | undefined; since: number; now: number }) {
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
    <div role="group" aria-label={t("rc.lane", { source: t(sourceKey[source]) })} data-testid={`lane-${source}`} onKeyDown={onKeyDown} onFocus={onFocus} className="relative h-4">
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
