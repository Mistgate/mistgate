import { queryOptions, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Fragment, useState, type ReactNode } from "react";
import { Icon } from "@/components/ui/icons";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Segmented } from "@/components/ui/segmented";
import { StatusDot, StatusPill } from "@/components/ui/status";
import type { GetFleetLoadResponse, LoadRecommendation, NodeLoad } from "@/gen/mistgate/ops/v1/ops_pb";
import { useT, type T } from "@/i18n";
import { ops } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useFmt, type Fmt } from "@/lib/format";
import { isUnavailable } from "@/lib/ops";
import { plain, type Plain } from "@/lib/plain";
import { bottleneckKey, fitKey, isLoadStatus, loadStatusKind, recKey, severityKind } from "@/lib/resources";

// Nodes -> Load: one row per node from the capacity read (OpsService.GetFleetLoad) over a window of its hourly host load:
// people at peak, the busy hour's CPU, what runs out first, how many more people it can take, and a status. A row opens in
// place to what to do about it and the numbers behind. The VPS edition only: the Nodes page does not offer the view on the
// Cloudflare edition.

type Load = Plain<GetFleetLoadResponse>;
type Row = Plain<NodeLoad>;
type Rec = Plain<LoadRecommendation>;

export const loadWindows = [24, 168, 720] as const;
export type LoadWindow = (typeof loadWindows)[number];

export const fleetLoadQuery = (hours: number) =>
  queryOptions({
    queryKey: ["ops", "fleet-load", hours],
    queryFn: async ({ signal }) => plain(await ops.getFleetLoad({ hours }, { signal })),
    refetchInterval: 5 * 60_000,
    retry: false,
  });

const cols = "md:grid-cols-[minmax(160px,1.6fr)_88px_minmax(130px,1fr)_minmax(120px,1fr)_minmax(110px,0.9fr)_112px_28px]";

export function FleetLoad() {
  const t = useT();
  const fmt = useFmt();
  const [hours, setHours] = useState<LoadWindow>(168);
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const q = useQuery(fleetLoadQuery(hours));
  const toggle = (id: string) => setOpen((s) => (s.has(id) ? new Set([...s].filter((x) => x !== id)) : new Set([...s, id])));

  const range = (
    <Segmented
      variant="flat"
      aria-label={t("load.range")}
      value={String(hours)}
      onValueChange={(v) => setHours(Number(v) as LoadWindow)}
      options={loadWindows.map((h) => ({ value: String(h), label: t(`load.range.${h}`) }))}
    />
  );
  if (!q.data) {
    if (q.isError) return isUnavailable(q.error) ? <Notice tone="quiet">{t("load.unavailable")}</Notice> : <QueryError error={q.error} onRetry={() => void q.refetch()} />;
    return <Pending />;
  }
  const data = q.data;
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-3">
        <Summary data={data} />
        <span className="flex-1" />
        {range}
      </div>
      {data.nodes.length === 0 ? (
        <p className="p-8 text-center text-[13px] text-muted">{t("load.empty")}</p>
      ) : (
        <div className="overflow-hidden rounded-card-lg border border-line bg-surface">
          <div className={cx("hidden h-10 items-center gap-4 border-b border-line px-5 text-xs font-semibold text-muted md:grid", cols)}>
            <span>{t("load.col.node")}</span>
            <span className="text-end">{t("load.col.people")}</span>
            <span>{t("load.col.cpu")}</span>
            <span>{t("load.col.bottleneck")}</span>
            <span className="text-end">{t("load.col.headroom")}</span>
            <span>{t("load.col.status")}</span>
            <span />
          </div>
          {data.nodes.map((n) => (
            <Fragment key={n.nodeId}>
              <NodeRow n={n} fmt={fmt} expanded={open.has(n.nodeId)} onToggle={() => toggle(n.nodeId)} />
              {open.has(n.nodeId) && <Details n={n} hours={data.hours} fmt={fmt} />}
            </Fragment>
          ))}
        </div>
      )}
    </div>
  );
}

/** Nodes by status, the room left across the fleet and where it is. */
function Summary({ data }: { data: Load }) {
  const t = useT();
  const s = data.summary;
  if (!s) return null;
  const counts = [
    { kind: "bad" as const, n: s.overloaded, label: t("load.status.overloaded") },
    { kind: "warn" as const, n: s.busy, label: t("load.status.busy") },
    { kind: "ok" as const, n: s.ok, label: t("load.status.ok") },
    { kind: "off" as const, n: s.unknown, label: t("load.status.unknown") },
  ].filter((c) => c.n > 0);
  const free = s.mostFree[0];
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1.5 text-[13px]">
      {counts.map((c) => (
        <span key={c.kind} className="flex items-center gap-1.5">
          <StatusDot kind={c.kind} />
          {c.label}
          <b className="font-mono">{c.n}</b>
        </span>
      ))}
      {s.headroomNodes > 0 && <span className="text-muted">{t.n("load.sum.room", s.headroomPeople)}</span>}
      {free && free.headroomPeople !== undefined && (
        <span className="text-muted">
          {t("load.sum.freest", { name: free.nodeName, n: String(free.headroomPeople) })}
        </span>
      )}
    </div>
  );
}

/** The headroom cell: people the node can take, "full" at none, or why it is not known. */
function headroomText(t: T, n: Row): { text: string; title?: string } {
  if (n.status === "unknown") return { text: "—" };
  if (n.headroomPeople === undefined) {
    const k = fitKey(n.fit);
    return { text: "—", title: k ? t(k) : undefined };
  }
  if (n.headroomPeople === 0) return { text: t("load.headroom.none") };
  return { text: t.n("load.headroom.n", n.headroomPeople) };
}

function NodeRow({ n, fmt, expanded, onToggle }: { n: Row; fmt: Fmt; expanded: boolean; onToggle: () => void }) {
  const t = useT();
  const status = isLoadStatus(n.status) ? n.status : "unknown";
  const kind = loadStatusKind(status);
  const known = status !== "unknown";
  const bk = bottleneckKey(n.bottleneck);
  const room = headroomText(t, n);
  const cpu = Math.min(100, Math.max(0, n.busyHourCpuPct));
  const label = (s: string, v: ReactNode) => (
    <span className="flex items-baseline gap-2 md:contents">
      <span className="w-32 flex-none text-xs text-muted md:hidden">{s}</span>
      {v}
    </span>
  );
  return (
    <div className={cx("relative grid items-center gap-x-4 gap-y-1.5 border-b border-line px-4 py-2.5 text-[13px] last:border-b-0 md:min-h-[52px] md:px-5", cols)}>
      <div className="flex min-w-0 items-center gap-2.5">
        <StatusDot kind={kind} />
        <Link to="/nodes/$id" params={{ id: n.nodeId }} className="min-w-0 truncate font-bold hover:text-accent-text">
          {n.nodeName}
        </Link>
        {!n.connected && known && <span className="text-xs text-muted">{t("load.offline")}</span>}
      </div>
      {label(t("load.col.people"), <span className="font-mono md:text-end">{known ? fmt.num(n.peoplePeak, 0) : "—"}</span>)}
      {label(
        t("load.col.cpu"),
        known ? (
          <span className="flex min-w-0 flex-1 items-center gap-2">
            <span className="h-1 w-16 flex-none overflow-hidden rounded-[2px] bg-surface-2">
              <span className={cx("block h-full", cpu >= 80 ? "bg-danger" : cpu >= 60 ? "bg-warn" : "bg-accent")} style={{ width: `${cpu}%` }} />
            </span>
            <span className="font-mono text-xs">{fmt.num(n.busyHourCpuPct, 0)}%</span>
          </span>
        ) : (
          <span className="text-muted">—</span>
        ),
      )}
      {label(t("load.col.bottleneck"), <span className={cx("truncate", !bk && "text-muted")}>{bk ? t(bk) : known ? t("load.res.none") : "—"}</span>)}
      {label(
        t("load.col.headroom"),
        <span title={room.title} className={cx("font-mono text-xs md:text-end", room.title && "cursor-help underline decoration-faint decoration-dotted underline-offset-[3px]")}>
          {room.text}
        </span>,
      )}
      <span>
        <StatusPill kind={kind} label={t(`load.status.${status}`)} sm />
      </span>
      <button
        type="button"
        aria-expanded={expanded}
        aria-label={t(expanded ? "load.collapse" : "load.expand", { name: n.nodeName })}
        onClick={onToggle}
        className="flex size-7 items-center justify-center justify-self-end rounded-ctl text-muted transition-colors hover:bg-surface-2 hover:text-fg max-md:absolute max-md:end-3 max-md:top-2"
      >
        <Icon name="chevronDown" size={14} className={cx("transition-transform duration-200", expanded && "rotate-180")} />
      </button>
    </div>
  );
}

/** What to do about a node, then the numbers the read went by. */
function Details({ n, hours, fmt }: { n: Row; hours: number; fmt: Fmt }) {
  const t = useT();
  const pct = (v: number | undefined) => (v === undefined ? t("load.d.unknown") : `${fmt.num(v, v < 10 ? 1 : 0)}%`);
  const b = (v: number | undefined) => (v === undefined ? t("load.d.unknown") : fmt.bytes(v, 1024));
  const fitWhy = fitKey(n.fit);
  const facts: { label: string; value: string }[] = [
    { label: t("load.d.hours"), value: t("load.d.hours.value", { n: String(n.hoursWithData), total: String(hours) }) },
  ];
  if (n.status !== "unknown") {
    facts.push(
      { label: t("load.d.busyHour"), value: t("load.d.busyHour.value", { time: fmt.stamp(n.busyHourUnix), cpu: pct(n.busyHourCpuPct), people: fmt.num(n.peopleBusyHour, 0) }) },
      { label: t("load.d.peaks"), value: pct(n.cpuPeakP95Pct) },
      { label: t("load.d.cpuPer100"), value: n.cpuPctPerHundredPeople !== undefined ? pct(n.cpuPctPerHundredPeople) : fitWhy ? t(fitWhy) : t("load.d.unknown") },
      { label: t("load.d.perPerson"), value: n.mbpsPerPerson !== undefined ? `${fmt.num(n.mbpsPerPerson, 2)} ${t("unit.mbit")}` : t("load.d.unknown") },
      { label: t("load.d.steal"), value: pct(n.stealP95Pct) },
      { label: t("load.d.ram"), value: pct(n.ramPeakPct) },
      { label: t("load.d.swap"), value: b(n.swapUsedMaxBytes) },
      { label: t("load.d.conntrack"), value: pct(n.conntrackPeakPct) },
      { label: t("load.d.disk"), value: pct(n.diskUsedPct) },
      {
        label: t("load.d.traffic"),
        value:
          n.capacityMbps > 0 && n.bandwidthPeakPct !== undefined
            ? t(n.capacitySource === "configured" ? "load.d.traffic.configured" : "load.d.traffic.nic", { pct: pct(n.bandwidthPeakPct), mbps: fmt.num(n.capacityMbps, 0) })
            : fmt.mbit(Math.max(n.rxPeakBps, n.txPeakBps)),
      },
      {
        label: t("load.d.agentCpu"),
        value: n.agentCpuAvgPct === undefined ? t("load.d.unknown") : t("load.d.agentCpu.value", { avg: pct(n.agentCpuAvgPct), max: pct(n.agentCpuMaxPct) }),
      },
      {
        label: t("load.d.agentMem"),
        value:
          n.agentRssMaxBytes === undefined
            ? t("load.d.unknown")
            : n.agentRssGrowthPctPerDay === undefined
              ? b(n.agentRssMaxBytes)
              : t("load.d.agentMem.value", { rss: b(n.agentRssMaxBytes), growth: fmt.num(n.agentRssGrowthPctPerDay, 1) }),
      },
    );
  }
  return (
    <div className="flex flex-col gap-3 border-b border-line bg-canvas px-4 py-3.5 last:border-b-0 md:px-5">
      {n.recommendations.length === 0 ? (
        <p className="text-[13px] text-muted">{n.status === "unknown" ? t("load.noData") : t("load.noRecs")}</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {n.recommendations.map((r, i) => (
            <li key={`${r.code}/${i}`} className="flex items-start gap-2.5 text-[13px] leading-snug">
              <span className="mt-[5px] flex">
                <StatusDot kind={severityKind(r.severity)} />
              </span>
              <span className="min-w-0 flex-1 text-pretty">{recText(t, r)}</span>
            </li>
          ))}
        </ul>
      )}
      <dl className="grid gap-x-6 gap-y-1.5 text-xs sm:grid-cols-2 lg:grid-cols-3">
        {facts.map((f) => (
          <div key={f.label} className="flex min-w-0 gap-2">
            <dt className="flex-none text-muted">{f.label}</dt>
            <dd className="min-w-0 truncate text-end font-mono text-fg sm:ms-auto" title={f.value}>
              {f.value}
            </dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

/** A recommendation in words; a code this page does not know shows as it is. */
export function recText(t: T, r: Rec): string {
  const k = recKey(r);
  return k ? t(k, r.params) : r.code;
}
