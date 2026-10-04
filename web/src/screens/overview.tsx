import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useMemo, useState } from "react";
import { useAddNode } from "@/components/add-node";
import { ChartCard, type ChartLine } from "@/components/chart";
import { Avatar, PageTitle, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Segmented } from "@/components/ui/segmented";
import { kindTextClass, StatusDot } from "@/components/ui/status";
import { OverviewRange, type NodeCard as NodeCardMsg, type OverviewResponse as OverviewMsg } from "@/gen/mistgate/admin/v1/fleet_pb";
import type { Plain } from "@/lib/plain";

type NodeCard = Plain<NodeCardMsg>;
type OverviewResponse = Plain<OverviewMsg>;
import { useT, type T } from "@/i18n";
import { cx } from "@/lib/cx";
import { foldView } from "@/lib/fleet";
import { scaleBytes, useFmt } from "@/lib/format";
import { useIsPhone } from "@/lib/media";
import { nodeKind, sortByProblems, useNodeStatus } from "@/lib/node-status";
import { overviewQuery } from "@/lib/queries";
import { FirstRunView, useAddNodeFromUrl, useFirstRun } from "./first-run";
import { HealthStrip } from "./health/strip";
import { EventsCard } from "./overview-events";
import { foldDays, niceCeil, protocolName, tail, toSeries, type Series } from "@/lib/series";

export function OverviewScreen() {
  const t = useT();
  const fmt = useFmt();
  const addNode = useAddNode();
  useAddNodeFromUrl();
  const firstRun = useFirstRun();
  const [range, setRange] = useState<"24h" | "7d">("24h");
  const q = useQuery(overviewQuery(range === "24h" ? OverviewRange.OVERVIEW_RANGE_24H : OverviewRange.OVERVIEW_RANGE_7D));
  const data = q.data;

  if (!data) {
    return q.isError ? (
      <div className="flex flex-col gap-4">
        <PageTitle>{t("nav.overview")}</PageTitle>
        <QueryError error={q.error} onRetry={() => void q.refetch()} />
      </div>
    ) : (
      <Pending />
    );
  }

  const hasNodes = data.nodesTotal > 0;
  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex flex-wrap items-center gap-3">
        <div className="flex min-w-0 flex-1 flex-col gap-[3px]">
          <PageTitle>{t("nav.overview")}</PageTitle>
          <span className="text-xs text-muted">{t("ov.when", { time: fmt.clock(data.nowUnix) })}</span>
        </div>
        {hasNodes && (
          <Button variant="primary" size="md" onClick={() => addNode()}>
            <Icon name="plus" size={14} />
            {t("node.add.button")}
          </Button>
        )}
      </div>
      {/* the checklist hangs above the fleet until everything is done; on an empty panel it is the whole page */}
      {firstRun && <FirstRunView steps={firstRun.steps} onHide={firstRun.hide} />}
      {hasNodes && <Fleet data={data} range={range} onRange={setRange} onboarding={!!firstRun?.open} />}
    </div>
  );
}

function Fleet({ data, range, onRange, onboarding }: { data: OverviewResponse; range: "24h" | "7d"; onRange: (r: "24h" | "7d") => void; onboarding: boolean }) {
  return (
    <>
      <HealthStrip cards={data.nodes} usersOnline={data.usersOnline} alertCount={data.alertsActive} criticalCount={data.alertsCritical} onboarding={onboarding} />
      <NodeGrid cards={data.nodes} />
      <div className="grid items-start gap-3.5 md:grid-cols-[minmax(0,1.55fr)_minmax(0,1fr)]">
        <div className="flex min-w-0 flex-col gap-3.5">
          <TrafficChart data={data} range={range} onRange={onRange} />
          <OnlineChart data={data} />
        </div>
        <div className="flex min-w-0 flex-col gap-3.5">
          <EventsCard events={data.events} />
          <TopCard data={data} />
        </div>
      </div>
    </>
  );
}

const onlineOf = (n: { online: { users: number }[] }) => n.online.reduce((a, o) => a + o.users, 0);

/**
 * 5 columns on the desktop, problems first. A dashed "add" tile fills the last row. Past 10 nodes (past 4 on the
 * phone) the first 9 (4) stay and the rest folds into one tile that opens the Nodes list.
 */
function NodeGrid({ cards }: { cards: NodeCard[] }) {
  const t = useT();
  const fmt = useFmt();
  const st = useNodeStatus();
  const addNode = useAddNode();
  const phone = useIsPhone();
  const sorted = useMemo(() => sortByProblems(cards), [cards]);
  const over = sorted.length > (phone ? 4 : 10);
  const shown = over ? sorted.slice(0, phone ? 4 : 9) : sorted;
  const hidden = sorted.slice(shown.length);
  const fold = foldView(hidden); // a host blip or a pending node is not a problem here either, nor green
  const addTile = !phone && !over && sorted.length % 5 !== 0;

  return (
    <div className="grid grid-cols-1 gap-2.5 md:grid-cols-3 wide:grid-cols-5">
      {shown.map((n, i) => {
        const kind = nodeKind(n);
        const line = st.line(n);
        return (
          <Link
            key={n.id}
            to="/nodes/$id"
            params={{ id: n.id }}
            className="card-hover screen-enter flex min-w-0 flex-col gap-2.5 rounded-card border border-line bg-surface p-3.5"
            style={{ animationDelay: `${i * 45}ms` }}
          >
            <div className="flex min-w-0 items-baseline gap-2">
              <span className="text-[15px] font-extrabold tracking-[-0.02em]">{n.name}</span>
              <span className="min-w-0 flex-1 truncate text-xs text-muted">{n.location || fmt.country(n.countryCode)}</span>
              <span className="font-mono text-[10px] font-bold text-faint">{n.countryCode}</span>
            </div>
            {/* the reason is what the owner has to read: up to three lines, and the whole of it on hover */}
            <div className={cx("flex min-h-8 min-w-0 flex-1 items-start gap-[7px] text-xs leading-4 font-semibold", kindTextClass[kind])} title={`${st.word(n)} · ${line}`}>
              <span className="flex h-4 flex-none items-center">
                <StatusDot kind={kind} />
              </span>
              <span className="line-clamp-3 min-w-0 text-pretty">{line}</span>
            </div>
            {/* a narrow tile puts the speed on a line of its own, never "↓ 58" here and "Mbit/s" there */}
            <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5 border-t border-line pt-2.5 font-mono text-xs text-muted">
              <span className="whitespace-nowrap">
                <b className="font-bold text-fg">{onlineOf(n)}</b> {t("ov.online")}
              </span>
              <span className="whitespace-nowrap">↓ {fmt.mbit(n.downBps)}</span>
            </div>
          </Link>
        );
      })}
      {over && (
        <Link
          to="/nodes"
          className="flex min-h-[104px] min-w-0 flex-col justify-between gap-2.5 rounded-card border border-line bg-surface-2 p-3.5 transition-transform duration-300 ease-spring hover:-translate-y-0.5"
        >
          <span className="text-[15px] font-extrabold tracking-[-0.02em]">{t.n("ov.moreNodes", hidden.length)}</span>
          <span className="flex items-center gap-[7px] text-xs font-semibold text-muted">
            <StatusDot kind={fold.kind} />
            {fold.problems ? t.n("ov.moreBad", fold.problems) : fold.pending ? t.n("ov.tail.pending", fold.pending) : t("ov.moreOk")}
          </span>
          <span className="border-t border-line pt-2.5 text-xs font-bold text-accent-text">{t("ov.allNodes")} ›</span>
        </Link>
      )}
      {addTile && (
        <button
          type="button"
          onClick={() => addNode()}
          className="flex min-h-[104px] min-w-0 flex-col items-center justify-center gap-1.5 rounded-card border-[1.5px] border-dashed border-line p-3.5 text-muted transition-colors duration-200 hover:border-accent-line hover:text-fg"
        >
          <span aria-hidden className="text-xl leading-none">
            +
          </span>
          <span className="text-xs font-bold">{t("ov.addTile")}</span>
        </button>
      )}
    </div>
  );
}

const pad2 = (n: number) => String(n).padStart(2, "0");
const hourLabel = (unix: number) => `${pad2(new Date(unix * 1000).getHours())}:00`;

function weekday(unix: number, lang: string) {
  return new Date(unix * 1000).toLocaleDateString(lang, { weekday: "short" });
}

/** Axis labels from the actual bucket times: five hour marks ending with "now", or one weekday per bucket. */
function xLabels(s: Series, daily: boolean, t: T, lang: string): string[] {
  const n = s.times.length;
  if (daily) return s.times.map((x) => weekday(x, lang));
  if (n < 2) return [];
  return [0, 0.25, 0.5, 0.75].map((f) => hourLabel(s.times[Math.round(f * (n - 1))]!)).concat(t("ov.now"));
}

function whenLabel(s: Series, i: number, daily: boolean, lang: string): string {
  const x = s.times[i]!;
  if (daily) return new Date(x * 1000).toLocaleDateString(lang, { weekday: "short", day: "numeric", month: "short" });
  return `${hourLabel(x)}–${hourLabel(x + 3600)}`;
}

function TrafficChart({ data, range, onRange }: { data: OverviewResponse; range: "24h" | "7d"; onRange: (r: "24h" | "7d") => void }) {
  const t = useT();
  const fmt = useFmt();
  const daily = range === "7d";
  const s = useMemo(() => {
    const hourly = toSeries(data.traffic);
    return daily ? foldDays(hourly, "sum") : hourly;
  }, [data.traffic, daily]);

  const peak = Math.max(0, ...s.total);
  const unit = peak > 0 ? scaleBytes(peak).unit : 3;
  const div = 1000 ** unit;
  const step = niceCeil(peak / div / 2);
  const max = step * 2;
  const digits = step < 1 ? 2 : 1;
  const sum = (a: readonly number[]) => a.reduce((x, y) => x + y, 0);
  const unitName = t(`unit.${(["B", "KB", "MB", "GB", "TB"] as const)[unit]}`);

  const lines: ChartLine[] = [
    { key: "total", values: s.total.map((v) => v / div), tone: "accent", width: 2 },
    ...s.protocols.slice(1).map((p) => ({ key: p, values: s.values[p]!.map((v) => v / div), tone: "them" as const, width: 1.5 })),
  ];
  return (
    <ChartCard
      title={`${t("ov.traffic")} · ${t(daily ? "ov.unitDay" : "ov.unitHour", { unit: unitName })}`}
      icon="traffic"
      iconTone="sky"
      control={
        <Segmented
          variant="thumb"
          aria-label={t("ov.range")}
          value={range}
          onValueChange={onRange}
          options={[
            { value: "24h", label: t("ov.range24h") },
            { value: "7d", label: t("ov.range7d") },
          ]}
          className="w-[140px]"
        />
      }
      lines={lines}
      max={max}
      height={150}
      yLabels={[fmt.num(max, digits), fmt.num(step, digits), "0"]}
      xLabels={xLabels(s, daily, t, fmt.lang)}
      idle={{ when: t(daily ? "ov.last7" : "ov.last24"), main: fmt.bytes(sum(s.total)) }}
      at={(i) => ({ when: whenLabel(s, i, daily, fmt.lang), main: fmt.bytes(s.total[i] ?? 0) })}
      legend={(i) =>
        s.protocols.map((p, k) => ({
          key: p,
          label: protocolName(p),
          value: fmt.bytes(i === null ? sum(s.values[p]!) : (s.values[p]![i] ?? 0)),
          tone: k === 0 ? "accent" : "them",
        }))
      }
      label={`${t("ov.traffic")}, ${t(daily ? "ov.last7" : "ov.last24")}: ${fmt.bytes(sum(s.total))}`}
    />
  );
}

function OnlineChart({ data }: { data: OverviewResponse }) {
  const t = useT();
  const fmt = useFmt();
  // always the last 24 hours, whatever the traffic range says
  const s = useMemo(() => tail(toSeries(data.online), 24), [data.online]);
  const peak = Math.max(4, ...s.total);
  const step = Math.ceil(peak / 2);
  const max = step * 2;

  // "now" is live: the people counted on the node cards, per protocol
  const live: Record<string, number> = {};
  for (const n of data.nodes) for (const o of n.online) live[o.protocol] = (live[o.protocol] ?? 0) + o.users;
  const protocols = s.protocols.length > 0 ? s.protocols : Object.keys(live);

  const lines: ChartLine[] = [
    { key: "total", values: s.total, tone: "accent", width: 2 },
    ...s.protocols.slice(1).map((p) => ({ key: p, values: s.values[p]!, tone: "them" as const, width: 1.5 })),
  ];
  return (
    <ChartCard
      title={t("ov.onlineTitle")}
      icon="people"
      iconTone="mint"
      lines={lines}
      max={max}
      height={72}
      yLabels={[String(max), String(step), "0"]}
      xLabels={xLabels(s, false, t, fmt.lang)}
      idle={{ when: t("ov.now"), main: t.n("ov.onlineCount", data.usersOnline) }}
      at={(i) => ({ when: hourLabel(s.times[i]!), main: t.n("ov.onlineCount", s.total[i] ?? 0) })}
      legend={(i) =>
        protocols.map((p, k) => ({
          key: p,
          label: protocolName(p),
          value: String(i === null ? (live[p] ?? 0) : (s.values[p]?.[i] ?? 0)),
          tone: k === 0 ? "accent" : "them",
        }))
      }
      label={`${t("ov.onlineTitle")}: ${t.n("ov.onlineCount", data.usersOnline)}`}
    />
  );
}

function TopCard({ data }: { data: OverviewResponse }) {
  const t = useT();
  const fmt = useFmt();
  const top = data.topConsumers;
  const best = Math.max(1, ...top.map((u) => u.downBps));
  const rate = (bps: number) => {
    if (bps >= 1_000_000) return fmt.mbit(bps);
    if (bps >= 1_000) return `${fmt.num(bps / 1_000, bps >= 10_000 ? 0 : 1)} ${t("unit.kbit")}`;
    return `${fmt.num(bps, 0)} ${t("unit.bit")}`;
  };
  return (
    <section className="flex flex-col gap-2.5 rounded-card-lg border border-line bg-surface p-4">
      <SectionLabel as="h2" icon="traffic" tone="sky">
        {t("ov.top")}
      </SectionLabel>
      {top.map((u, i) => (
        <div key={`${u.userId}/${u.nodeId}`} className="flex items-center gap-2.5">
          <Avatar name={u.userName} index={i} size={26} />
          <div className="flex min-w-0 flex-1 flex-col gap-1">
            <div className="flex gap-1.5 text-[13px]">
              <Link to="/users/$id" params={{ id: u.userId }} className="min-w-0 flex-1 truncate font-bold transition-colors hover:text-accent-text">
                {u.userName}
              </Link>
              <span className="font-mono text-xs">{rate(u.downBps)}</span>
            </div>
            <div className="h-1 overflow-hidden rounded-[2px] bg-surface-2">
              <div className="h-full rounded-[2px] bg-accent" style={{ width: `${Math.max(3, Math.round((u.downBps / best) * 100))}%` }} />
            </div>
          </div>
          <Link to="/nodes/$id" params={{ id: u.nodeId }} title={u.nodeName} className="w-14 truncate text-right font-mono text-[11px] text-muted transition-colors hover:text-fg">
            {u.nodeName}
          </Link>
        </div>
      ))}
      {top.length === 0 && <p className="text-[13px] text-muted">{t("ov.noTop")}</p>}
    </section>
  );
}
