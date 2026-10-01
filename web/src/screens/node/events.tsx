import { useInfiniteQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { IconChip, type IconName, type Tone } from "@/components/ui/icons";
import { EmptyState } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Segmented } from "@/components/ui/segmented";
import { StatusPill } from "@/components/ui/status";
import { EventSeverity } from "@/gen/mistgate/admin/v1/fleet_pb";
import { useT } from "@/i18n";
import { fleet } from "@/lib/api";
import { useFmt, type Fmt } from "@/lib/format";
import { plain } from "@/lib/plain";
import { pollMs } from "@/lib/queries";
import { byDay, buildLines, eventFilters, lineRows, type EventFilter, type EventLine, type NodeEvent } from "./event-model";
import { lineText, profileLabel, rowDetail } from "./event-text";

/**
 * The glyph that says what kind of thing a line is, and the tone of its family: the node and its updates sky,
 * profiles and engines sage, certificates lavender, the host's clock and facts sand. The status pill next to it says
 * how it went; the tone never does.
 */
function lineMark(l: EventLine): { icon: IconName; tone: Tone } {
  switch (l.kind) {
    case "agent_started":
      return { icon: "server", tone: "sky" };
    case "update":
    case "rollback":
      return { icon: "upload", tone: "sky" };
    case "profile_added":
      return { icon: "plus", tone: "sage" };
    case "profile_removed":
      return { icon: "trash", tone: "sage" };
    case "profiles_restarted":
      return { icon: "refresh", tone: "sage" };
    case "profiles_started":
      return { icon: l.head.code === "engine_restarted" ? "refresh" : "check", tone: "sage" };
  }
  const c = l.head.code;
  if (c.startsWith("update_")) return { icon: "upload", tone: "sky" };
  if (c === "engine_failed" || c === "awg_backend_unavailable" || c === "apply_rejected") return { icon: "x", tone: "sage" };
  if (c === "cert_renewed") return { icon: "lock", tone: "lavender" };
  if (c.startsWith("node_") || c === "host_rebooted" || c === "stats_stale" || c === "clock_skew") return { icon: "clock", tone: "sand" };
  return { icon: "info", tone: "sand" };
}

/** "Today", "Yesterday", else the date; the year only when it is not this one. */
function dayLabel(t: ReturnType<typeof useT>, fmt: Fmt, unix: number, now = new Date()): string {
  const d = new Date(unix * 1000);
  const same = (a: Date, b: Date) => a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
  if (same(d, now)) return t("node.ev.today");
  if (same(d, new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1))) return t("node.ev.yesterday");
  return d.toLocaleDateString(fmt.lang, { day: "numeric", month: "long", ...(d.getFullYear() === now.getFullYear() ? {} : { year: "numeric" }) });
}

const exact = (fmt: Fmt, unix: number) => new Date(unix * 1000).toLocaleString(fmt.lang, { dateStyle: "short", timeStyle: "medium" });

/** Everything the node reported and the panel concluded about it, one line per thing, newest first, 50 rows at a time. */
export function EventsTab({ nodeId }: { nodeId: string }) {
  const t = useT();
  const fmt = useFmt();
  const [filter, setFilter] = useState<EventFilter>("all");
  const [open, setOpen] = useState<ReadonlySet<number>>(new Set());
  // every filter is the server's, so a page of 50 is 50 of the kind asked for
  const problems = filter === "problems";
  const family = filter === "profiles" || filter === "agent" ? filter : "";
  const q = useInfiniteQuery({
    queryKey: ["node-events", nodeId, filter],
    queryFn: async ({ pageParam, signal }) =>
      plain(
        await fleet.listEvents(
          { nodeId, limit: pageSize, beforeId: BigInt(pageParam), minSeverity: problems ? EventSeverity.WARNING : undefined, family },
          { signal },
        ),
      ),
    initialPageParam: 0,
    getNextPageParam: (last) => (last.hasMore ? last.events.at(-1)?.id : undefined),
    refetchInterval: pollMs,
  });
  const events = useMemo(() => q.data?.pages.flatMap((p) => p.events) ?? [], [q.data]);
  const days = useMemo(() => byDay(buildLines(events, filter)), [events, filter]);

  if (q.isError && events.length === 0) return <QueryError error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data) return <Pending />;

  const toggle = (id: number) =>
    setOpen((cur) => {
      const next = new Set(cur);
      if (!next.delete(id)) next.add(id);
      return next;
    });

  const filterBar = (
    <Segmented
      aria-label={t("node.ev.filter")}
      value={filter}
      onValueChange={setFilter}
      variant="flat"
      options={eventFilters.map((f) => ({ value: f, label: t(`node.ev.filter.${f}`) }))}
      className="w-fit max-w-full flex-wrap"
    />
  );

  if (events.length === 0 && filter === "all") {
    return (
      <div className="rounded-card-lg border border-dashed border-line">
        <EmptyState title={t("node.events.none")}>{t("node.events.noneBody")}</EmptyState>
      </div>
    );
  }
  return (
    <div className="flex flex-col gap-3">
      {filterBar}
      {days.length === 0 ? (
        <div className="rounded-card-lg border border-dashed border-line">
          {q.hasNextPage ? (
            // nothing of this kind among what is loaded, but there is more: say so, never "none at all"
            <EmptyState
              title={t("node.ev.noneFilteredMore", { n: events.length })}
              action={
                <Button variant="secondary" size="md" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
                  {t("node.ev.searchMore")}
                </Button>
              }
            />
          ) : (
            <EmptyState title={t("node.ev.noneFiltered")}>{t("node.ev.noneFilteredBody")}</EmptyState>
          )}
        </div>
      ) : (
        days.map((d) => (
          <section key={d.day} aria-label={dayLabel(t, fmt, d.unix)} className="flex flex-col gap-1.5">
            <SectionLabel as="h3" icon="calendar" tone="sand" className="px-1">
              {dayLabel(t, fmt, d.unix)}
            </SectionLabel>
            <div className="rounded-card-lg border border-line bg-surface px-4 py-1.5">
              {d.lines.map((l, i) => (
                <Line key={l.id} line={l} first={i === 0} open={open.has(l.id)} onToggle={() => toggle(l.id)} />
              ))}
            </div>
          </section>
        ))
      )}
      {q.hasNextPage && days.length > 0 && (
        <div className="flex justify-center">
          <Button variant="secondary" size="md" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
            {t("common.showMore")}
          </Button>
        </div>
      )}
    </div>
  );
}

const pageSize = 50;

/**
 * One line. On a phone the time and the glyph go on a line of their own above the title and the family chip is left
 * out, so the title has the whole width and wraps between words; "Details" becomes a text link under it.
 */
function Line({ line, first, open, onToggle }: { line: EventLine; first: boolean; open: boolean; onToggle: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const { title, sub } = lineText(t, line, fmt.stamp);
  const rows = lineRows(line);
  const mark = lineMark(line);
  const toggle = (cls: string) => (
    <button type="button" onClick={onToggle} aria-expanded={open} className={cls}>
      {open ? t("node.ev.hide") : t("node.ev.details")}
    </button>
  );
  return (
    <div className={`flex flex-col gap-2 py-3 ${first ? "" : "border-t border-line"}`}>
      <div className="flex items-start gap-3">
        <span className="w-11 flex-none pt-[3px] font-mono text-[11px] text-muted max-sm:hidden">{fmt.clock(line.timeUnix)}</span>
        <span className="flex-none max-sm:hidden">
          <StatusPill kind={line.status} glyphOnly />
        </span>
        {/* the chip's own display rule outranks the utilities, so the wrapper hides it */}
        <span className="flex flex-none max-sm:hidden">
          <IconChip icon={mark.icon} tone={mark.tone} size={22} />
        </span>
        <div className="flex min-w-0 flex-1 flex-col gap-[3px]">
          <span className="flex items-center gap-2 font-mono text-[11px] text-muted sm:hidden">
            {fmt.clock(line.timeUnix)}
            <StatusPill kind={line.status} glyphOnly />
          </span>
          <span className="text-[13px] font-bold text-pretty first-letter:uppercase">{title}</span>
          {/* an agent's error can be one long word: only that may break anywhere */}
          {sub && <span className="text-xs break-words text-pretty text-muted">{sub}</span>}
          {toggle("w-fit pt-1 text-[11px] font-bold text-accent-text sm:hidden")}
        </div>
        {toggle("flex-none rounded-ctl px-2 py-0.5 text-[11px] font-bold text-muted hover:text-fg max-sm:hidden")}
      </div>
      {open && (
        <ul className="flex flex-col gap-1.5 rounded-field bg-surface-2 px-3 py-2.5 sm:ml-14">
          {rows.map((e) => (
            <RowDetail key={e.id} e={e} />
          ))}
        </ul>
      )}
    </div>
  );
}

function RowDetail({ e }: { e: NodeEvent }) {
  const t = useT();
  const fmt = useFmt();
  const profile = e.profileName || e.params.profile || e.inboundId ? profileLabel(t, e) : "";
  const params = rowDetail(e);
  return (
    <li className="flex flex-col gap-0.5 font-mono text-[11px] break-words">
      <span>
        <b>{e.code}</b> <span className="text-muted">· {exact(fmt, e.timeUnix)} · {t(`node.ev.src.${e.source === "admin" || e.source === "panel" ? e.source : "agent"}`)}</span>
      </span>
      {profile && <span className="text-muted">{profile}</span>}
      {params && <span className="text-muted">{params}</span>}
    </li>
  );
}
