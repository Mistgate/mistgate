import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useMemo, useState, type ReactNode } from "react";
import { Scramble } from "@/components/fx/scramble-react";
import { Button } from "@/components/ui/button";
import { FilterChips } from "@/components/ui/chips";
import type { IconName } from "@/components/ui/icons";
import { Icon } from "@/components/ui/icons";
import { EmptyState } from "@/components/ui/notice";
import { CursorPagination } from "@/components/ui/pagination";
import { Pending, QueryError } from "@/components/ui/query-error";
import type { StatusKind } from "@/components/ui/status";
import { EventSeverity } from "@/gen/mistgate/admin/v1/fleet_pb";
import { useT } from "@/i18n";
import { fleet } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useCursorPaging } from "@/lib/paging";
import { dayLabel, useFmt, type Fmt } from "@/lib/format";
import { plain } from "@/lib/plain";
import { pollMs } from "@/lib/queries";
import { byDay, buildLines, eventFilters, lineRows, weekSummary, type EventFilter, type EventLine, type NodeEvent } from "./event-model";
import { lineText, profileLabel, rowDetail } from "./event-text";
import "./history.css";

/**
 * The glyph that says what kind of thing a line is. The box around it says how it went (red a failure, amber a warning or a blink,
 * grey the rest); the glyph is the family: the node and its updates, profiles and engines, certificates, the host's clock and facts.
 */
function lineMark(l: EventLine): IconName {
  switch (l.kind) {
    case "agent_started":
      return "server";
    case "update":
    case "rollback":
      return "upload";
    case "profile_added":
      return "plus";
    case "profile_removed":
      return "trash";
    case "profiles_restarted":
      return "refresh";
    case "profiles_started":
      return l.head.code === "engine_restarted" ? "refresh" : "check";
  }
  const c = l.head.code;
  if (c.startsWith("update_")) return "upload";
  if (c === "engine_failed" || c === "awg_backend_unavailable" || c === "apply_rejected") return "x";
  if (c === "cert_renewed") return "lock";
  if (c.startsWith("node_") || c === "host_rebooted" || c === "stats_stale" || c === "clock_skew") return "clock";
  return "info";
}

const tone = (s: StatusKind) => (s === "bad" ? "crit" : s === "warn" || s === "blip" ? "warn" : "");
const exact = (fmt: Fmt, unix: number) => new Date(unix * 1000).toLocaleString(fmt.locale, { dateStyle: "short", timeStyle: "medium" });

const pageSizes = [25, 50, 100] as const;

/**
 * Everything the node reported and the panel concluded about it, one line per thing, newest first, one page of the
 * server's log at a time (Newer / Older; where the page starts is in the URL). Above it the week at a glance, read from the
 * node's own events. `lead` is the bar's first part (the view switch), `createdUnix` and `down` say how much of the week the node had.
 */
export function EventsTab({ nodeId, lead, createdUnix = 0, down = false }: { nodeId: string; lead?: ReactNode; createdUnix?: number; down?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const [filter, setFilter] = useState<EventFilter>("all");
  const [open, setOpen] = useState<ReadonlySet<number>>(new Set());
  const paging = useCursorPaging({ sizes: pageSizes, defaultSize: 50 });
  // every filter is the server's, so a page of 50 is 50 of the kind asked for
  const problems = filter === "problems";
  const family = filter === "profiles" || filter === "agent" ? filter : "";
  const q = useQuery({
    queryKey: ["node-events", nodeId, filter, paging.before, paging.size],
    queryFn: async ({ signal }) =>
      plain(
        await fleet.listEvents(
          { nodeId, limit: paging.size, beforeId: BigInt(paging.before), minSeverity: problems ? EventSeverity.WARNING : undefined, family },
          { signal },
        ),
      ),
    // the old page stays on screen while the next one loads
    placeholderData: keepPreviousData,
    refetchInterval: pollMs,
  });
  const events = useMemo(() => q.data?.events ?? [], [q.data]);
  const days = useMemo(() => byDay(buildLines(events, filter)), [events, filter]);
  const older = q.data?.hasMore ? events.at(-1)?.id : undefined;
  const goOlder = () => older !== undefined && paging.older(older);
  const pager = (
    <CursorPagination
      flush
      className="px-4"
      busy={q.isPlaceholderData}
      size={paging.size}
      sizes={pageSizes}
      onSize={paging.setSize}
      cursor={{ before: paging.before, count: events.length, hasOlder: older !== undefined, hasNewer: paging.hasNewer, toNewest: paging.newerToFirst, index: paging.index, onOlder: goOlder, onNewer: paging.newer }}
    />
  );

  if (q.isError && events.length === 0) return <QueryError error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data) return <Pending />;

  const toggle = (id: number) =>
    setOpen((cur) => {
      const next = new Set(cur);
      if (!next.delete(id)) next.add(id);
      return next;
    });

  const bar = (
    <div className="nh-bar">
      {lead}
      <FilterChips
        aria-label={t("node.ev.filter")}
        value={filter}
        onValueChange={(f) => {
          setFilter(f);
          paging.reset();
        }}
        options={eventFilters.map((f) => ({ value: f, label: t(`node.ev.filter.${f}`) }))}
        className="nh-chips flex-nowrap"
      />
    </div>
  );

  if (events.length === 0 && filter === "all" && !paging.hasNewer) {
    return (
      <div className="nh">
        {bar}
        <div className="rounded-card border border-dashed border-line">
          <EmptyState title={t("node.events.none")}>{t("node.events.noneBody")}</EmptyState>
        </div>
      </div>
    );
  }
  return (
    <div className="nh">
      {bar}
      <WeekCard nodeId={nodeId} createdUnix={createdUnix} down={down} />
      <section className="nh-tl rounded-card border border-line bg-surface" aria-label={t("node.tab.events")}>
        {days.length === 0 ? (
          older !== undefined ? (
            // nothing of this kind on this page, but there is more: say so, never "none at all"
            <EmptyState
              title={t("node.ev.noneFilteredMore", { n: events.length })}
              action={
                <Button variant="secondary" size="md" onClick={goOlder}>
                  {t("node.ev.searchMore")}
                </Button>
              }
            />
          ) : (
            <EmptyState title={t("node.ev.noneFiltered")}>{t("node.ev.noneFilteredBody")}</EmptyState>
          )
        ) : (
          days.map((d) => (
            <div key={d.day} role="group" aria-label={dayLabel(t, fmt, d.unix)}>
              <div className="nh-day">
                <b>{dayLabel(t, fmt, d.unix)}</b>
                <span>{t.n("np.hi.events", d.lines.length)}</span>
              </div>
              {d.lines.map((l) => (
                <Line key={l.id} line={l} open={open.has(l.id)} onToggle={() => toggle(l.id)} />
              ))}
            </div>
          ))
        )}
        <div className="nh-foot">
          <span>{t("np.hi.keep")}</span>
        </div>
        {(older !== undefined || paging.hasNewer) && <div className="border-t border-line">{pager}</div>}
      </section>
    </div>
  );
}

/** The week: how many failures, how long the node was away, how much of the time it was on the line, how much happened; and the cells of it. */
function WeekCard({ nodeId, createdUnix, down }: { nodeId: string; createdUnix: number; down: boolean }) {
  const t = useT();
  const fmt = useFmt();
  // the agent's family holds the node's own up and down; the newest 200 cover a week unless the node is very chatty
  const q = useQuery({
    queryKey: ["node-events-week", nodeId],
    queryFn: async ({ signal }) => plain(await fleet.listEvents({ nodeId, limit: 200, family: "agent" }, { signal })),
    refetchInterval: pollMs * 6,
  });
  // the week is read as of the moment the list came in
  const now = Math.floor(q.dataUpdatedAt / 1000);
  const w = useMemo(() => (q.data ? weekSummary(q.data.events, now - (now % 60), createdUnix, down) : null), [q.data, now, createdUnix, down]);
  if (!w) return null;
  const capped = (q.data?.events.length ?? 0) >= 200 && !!q.data?.hasMore;
  const day = (i: number) => {
    const c = w.cells[i * 12]!;
    return dayLabel(t, fmt, c.fromUnix + 3600);
  };
  const cellText = (kind: string, from: number) => `${dayLabel(t, fmt, from + 60)}, ${fmt.clock(from)} · ${t(`np.hi.cell.${kind}` as "np.hi.cell.ok")}`;
  const stats: { v: string; l: string; c: string }[] = [
    { v: String(w.failures), l: t.n("np.hi.stat.failures", w.failures), c: w.failures > 0 ? "warn" : "" },
    { v: w.downS > 0 ? fmt.duration(w.downS) : t("np.hi.stat.noDown"), l: t("np.hi.stat.down"), c: "" },
    { v: w.uptime === null ? "—" : `${fmt.num(Math.floor(w.uptime * 100) / 100, 2)}%`, l: t("np.hi.stat.up"), c: w.uptime !== null && w.uptime >= 99 ? "ok" : w.uptime !== null ? "warn" : "" },
    { v: `${w.events}${capped ? "+" : ""}`, l: t.n("np.hi.stat.events", w.events), c: "" },
  ];
  return (
    <section className="nh-sum relative rounded-card border border-line bg-surface" aria-label={t("np.hi.week")}>
      <div className="nh-s1">
        {stats.map((x) => (
          <div key={x.l}>
            <b className={x.c}>
              <Scramble value={x.v} once />
            </b>
            <span>{x.l}</span>
          </div>
        ))}
      </div>
      <div>
        <div className="nh-up" aria-hidden>
          {w.cells.map((c) => (
            <i key={c.fromUnix} className={c.kind === "ok" ? undefined : c.kind} data-t={cellText(c.kind, c.fromUnix)} />
          ))}
        </div>
        <div className="nh-ux" aria-hidden>
          {Array.from({ length: 7 }, (_, i) => (
            <span key={i}>{day(i)}</span>
          ))}
        </div>
      </div>
    </section>
  );
}

/**
 * One line (the board's .nh-e): the time, the box with the glyph, the title and what it says, who did it, a chevron; opened, the rows behind
 * it with their exact time, the source and every parameter. On a phone the source is dropped and the title wraps.
 */
function Line({ line, open, onToggle }: { line: EventLine; open: boolean; onToggle: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const { title, sub } = lineText(t, line, fmt.stamp);
  const e = line.head;
  const who = t(`node.ev.src.${e.source === "admin" || e.source === "panel" ? e.source : "agent"}`);
  return (
    <div className={cx("nh-e", open && "open")}>
      <button type="button" className="nh-r" aria-expanded={open} aria-label={`${fmt.clock(line.timeUnix)} ${title}`} onClick={onToggle}>
        <span className="t">{fmt.clock(line.timeUnix)}</span>
        <span className={cx("g", tone(line.status))} aria-hidden>
          <Icon name={lineMark(line)} size={16} />
        </span>
        <div className="min-w-0">
          <b className="first-letter:uppercase">{title}</b>
          {sub && <span>{sub}</span>}
        </div>
        <span className="who">{who}</span>
        <Icon name="chevronDown" size={16} aria-hidden className="cv" />
      </button>
      {open && lineRows(line).map((r) => <RowDetail key={r.id} e={r} />)}
    </div>
  );
}

function RowDetail({ e }: { e: NodeEvent }) {
  const t = useT();
  const fmt = useFmt();
  const profile = e.profileName || e.params.profile || e.inboundId ? profileLabel(t, e) : "";
  const params = Object.entries(e.params);
  return (
    <div className="nh-d">
      <dl>
        <div>
          <dt>{t("np.hi.d.event")}</dt>
          <dd className="mono">{e.code}</dd>
        </div>
        <div>
          <dt>{t("np.hi.d.when")}</dt>
          <dd className="mono">{exact(fmt, e.timeUnix)}</dd>
        </div>
        <div>
          <dt>{t("np.hi.d.source")}</dt>
          <dd>{t(`node.ev.src.${e.source === "admin" || e.source === "panel" ? e.source : "agent"}`)}</dd>
        </div>
        {profile && (
          <div>
            <dt>{t("np.hi.d.profile")}</dt>
            <dd>{profile}</dd>
          </div>
        )}
      </dl>
      {params.length > 0 && (
        <div className="kv" title={rowDetail(e)}>
          {params.map(([k, v]) => (
            <span key={k}>
              <i>{k}</i>
              {v}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}
