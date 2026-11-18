import { queryOptions, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Fragment, useState, type CSSProperties, type ReactNode } from "react";
import { Scramble } from "@/components/fx/scramble-react";
import { Flag } from "@/components/ui/flag";
import { Icon } from "@/components/ui/icons";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Segmented } from "@/components/ui/segmented";
import { StatusDot } from "@/components/ui/status";
import type { GetFleetLoadResponse, LoadRecommendation, NodeLoad } from "@/gen/mistgate/ops/v1/ops_pb";
import type { ChannelLast } from "@/gen/mistgate/probe/v1/probe_pb";
import { useT, type T } from "@/i18n";
import { ops, probe } from "@/lib/api";
import { useEdition } from "@/lib/edition";
import { cx } from "@/lib/cx";
import { useFmt, type Fmt } from "@/lib/format";
import { isUnavailable } from "@/lib/ops";
import { plain, type Plain } from "@/lib/plain";
import { nodesQuery } from "@/lib/queries";
import { bottleneckKey, fitKey, isLoadStatus, loadStatusKind, recKey, severityKind } from "@/lib/resources";
import "./nodes.css";

// Nodes -> Load, as the redesign board draws it: three figures over a table with a row per node from the capacity read
// (OpsService.GetFleetLoad) over a window of its hourly host load: people at peak, the busy hour's CPU, what runs out first,
// how many more people it can take, and a state. A row opens in place to what to do about it, the five meters closest to their
// limit and the numbers behind. The VPS edition only: the Nodes page does not offer the view on the Cloudflare edition.

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

// The columns follow the card's own width: the node, people, the processor, what runs out first, the room, the state, an arrow.
const grid =
  "grid-cols-[minmax(170px,1.2fr)_110px_minmax(160px,1.2fr)_minmax(120px,1fr)_minmax(140px,1fr)_120px_16px] " +
  "@max-[1120px]/fzt:grid-cols-[minmax(150px,1.2fr)_96px_minmax(120px,1fr)_minmax(110px,1fr)_120px_16px] " +
  "@max-[780px]/fzt:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)_110px_16px] " +
  "@max-[520px]/fzt:grid-cols-[minmax(0,1fr)_104px_16px]";
const rowBase = "grid min-h-14 w-full items-center gap-4 border-t border-line px-6 text-start text-sm @max-[520px]/fzt:px-4 " + grid;
// the cells that give way as the card narrows: the room (1120), people and what runs out first (780), the processor (520)
const roomCell = "@max-[1120px]/fzt:hidden";
const midCell = "@max-[780px]/fzt:hidden";
const cpuCell = "@max-[520px]/fzt:hidden";

export function FleetLoad() {
  const t = useT();
  const fmt = useFmt();
  const [hours, setHours] = useState<LoadWindow>(168);
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const q = useQuery(fleetLoadQuery(hours));
  const fleet = useQuery(nodesQuery).data?.nodes;
  // the last "Measure the channel" of each node: a read of what the panel kept (the Cloudflare edition has no probe service)
  const edge = useEdition().edge;
  const measures = useQuery({ queryKey: ["channel-measures"], queryFn: () => probe.pathSpeedResults({ limit: 1 }), enabled: !edge, retry: false, staleTime: 60_000 }).data?.channels;
  const toggle = (id: string) => setOpen((s) => (s.has(id) ? new Set([...s].filter((x) => x !== id)) : new Set([...s, id])));

  if (!q.data) {
    if (q.isError) return isUnavailable(q.error) ? <Notice tone="quiet">{t("load.unavailable")}</Notice> : <QueryError error={q.error} onRetry={() => void q.refetch()} />;
    return <Pending />;
  }
  const data = q.data;
  const where = new Map((fleet ?? []).map((n) => [n.id, n]));
  return (
    <div className="flex flex-col gap-4">
      <Summary data={data} />
      {data.nodes.length === 0 ? (
        <p className="p-8 text-center text-[13px] text-muted">{t("load.empty")}</p>
      ) : (
        <section className="@container/fzt relative rounded-card border border-line bg-surface">
          <div className="flex items-center gap-3 px-6 pt-4 pb-3 @max-[520px]/fzt:px-4">
            <h3 className="me-auto text-base leading-6 font-medium">{t("nv.load.title")}</h3>
            <Segmented
              variant="flat"
              aria-label={t("load.range")}
              value={String(hours)}
              onValueChange={(v) => setHours(Number(v) as LoadWindow)}
              options={loadWindows.map((h) => ({ value: String(h), label: t(`load.range.${h}`) }))}
            />
          </div>
          <div role="row" className={cx(rowBase, "min-h-10 text-[13px] text-muted")}>
            <span>{t("load.col.node")}</span>
            <span className={midCell}>{t("load.col.people")}</span>
            <span className={cpuCell}>{t("load.col.cpu")}</span>
            <span className={midCell}>{t("load.col.bottleneck")}</span>
            <span className={roomCell}>{t("load.col.headroom")}</span>
            <span>{t("load.col.status")}</span>
            <span />
          </div>
          {data.nodes.map((n) => (
            <Fragment key={n.nodeId}>
              <NodeRow n={n} fmt={fmt} expanded={open.has(n.nodeId)} onToggle={() => toggle(n.nodeId)} country={where.get(n.nodeId)?.countryCode} place={where.get(n.nodeId)?.location} />
              {open.has(n.nodeId) && <Details n={n} hours={data.hours} fmt={fmt} channel={measures?.find((c) => c.nodeName === n.nodeName)?.last} />}
            </Fragment>
          ))}
        </section>
      )}
    </div>
  );
}

/** A figure of the three at the top: its name, the number, a line under it. */
function Figure({ k, children, note, className }: { k: string; children: ReactNode; note: ReactNode; className?: string }) {
  return (
    <div className={cx("flex flex-col gap-1 rounded-card border border-line bg-surface px-5 py-4", className)}>
      <span className="text-[13px] text-sec">{k}</span>
      <span className="text-xl leading-7 tracking-[-0.01em]">{children}</span>
      <span className="text-[12px] text-muted">{note}</span>
    </div>
  );
}

/** A sentence with a number in it, the number drawn big and in mono: split where the translation put it. */
function around(sentence: string, n: number, big: ReactNode) {
  const at = sentence.indexOf(String(n));
  if (at < 0) return sentence;
  return (
    <>
      {sentence.slice(0, at)}
      {big}
      {sentence.slice(at + String(n).length)}
    </>
  );
}

/** The room left across the fleet and where it is, how many are close to the limit, how many are fine. */
function Summary({ data }: { data: Load }) {
  const t = useT();
  const s = data.summary;
  if (!s) return null;
  const free = s.mostFree[0];
  const num = "font-mono text-[24px] font-medium tabular-nums";
  const roomN = s.headroomPeople;
  return (
    <div className="grid grid-cols-[1.4fr_1fr_1fr] gap-4 max-[1000px]:grid-cols-1">
      <Figure
        k={t("nv.load.room")}
        note={free && free.headroomPeople !== undefined ? t("load.sum.freest", { name: free.nodeName, n: String(free.headroomPeople) }) : "—"}
      >
        {s.headroomNodes > 0 ? around(t.n("load.sum.room", roomN), roomN, <Scramble once value={String(roomN)} className={num} />) : "—"}
      </Figure>
      <Figure k={t("nv.load.busy")} note={t("nv.load.busyNote")}>
        <Scramble once value={String(s.busy + s.overloaded)} className={num} />
      </Figure>
      <Figure k={t("load.status.ok")} note={t("nv.load.okNote") + (s.unknown > 0 ? ` · ${t("nv.load.noData", { n: s.unknown })}` : "")}>
        <Scramble once value={String(s.ok)} className={num} />
      </Figure>
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

function NodeRow({ n, fmt, expanded, onToggle, country, place }: { n: Row; fmt: Fmt; expanded: boolean; onToggle: () => void; country?: string; place?: string }) {
  const t = useT();
  const status = isLoadStatus(n.status) ? n.status : "unknown";
  const kind = loadStatusKind(status);
  const known = status !== "unknown";
  const bk = bottleneckKey(n.bottleneck);
  const room = headroomText(t, n);
  const cpu = Math.min(100, Math.max(0, n.busyHourCpuPct));
  const fill = cpu >= 80 ? "var(--danger)" : cpu >= 60 ? "var(--warning)" : "var(--accent-mark)";
  return (
    <button
      type="button"
      aria-expanded={expanded}
      aria-label={t(expanded ? "load.collapse" : "load.expand", { name: n.nodeName })}
      onClick={onToggle}
      className={cx(rowBase, "cursor-pointer transition-colors duration-150 ease-out hover:bg-[color-mix(in_srgb,var(--text)_3%,transparent)]", expanded && "bg-[color-mix(in_srgb,var(--text)_3%,transparent)]")}
    >
      <span className="flex min-w-0 items-center gap-3">
        {country && <Flag code={country} size={16} />}
        <span className="flex min-w-0 flex-col">
          <b className="truncate font-mono font-medium tabular-nums">{n.nodeName}</b>
          {(place || !n.connected) && <span className="truncate text-[13px] text-sec">{[place, !n.connected && known ? t("load.offline") : ""].filter(Boolean).join(" · ")}</span>}
        </span>
      </span>
      <span className={cx("font-mono font-medium tabular-nums", midCell)}>{known ? fmt.num(n.peoplePeak, 0) : "—"}</span>
      <span className={cx("grid grid-cols-[44px_minmax(0,1fr)] items-center gap-2", cpuCell)}>
        {known ? (
          <>
            <span className="text-end font-mono text-[13px] font-medium tabular-nums">{fmt.num(n.busyHourCpuPct, 0)}%</span>
            <i aria-hidden className="block h-1 rounded-[2px]" style={{ background: `linear-gradient(90deg, ${fill} ${cpu}%, var(--step) 0)` } as CSSProperties} />
          </>
        ) : (
          <span className="text-muted">—</span>
        )}
      </span>
      <span className={cx("truncate", !bk && "text-muted", midCell)}>{bk ? t(bk) : known ? t("load.res.none") : "—"}</span>
      <span title={room.title} className={cx("text-[14px]", room.title && "cursor-help", roomCell)}>
        {room.text}
      </span>
      <span className={cx("inline-flex items-center gap-2 whitespace-nowrap", `tone-${kind}`, kind !== "ok" && "tone-text")}>
        <StatusDot kind={kind} />
        {t(`load.status.${status}`)}
      </span>
      <Icon name="chevronDown" size={16} className={cx("block flex-none text-muted transition-[transform,color] duration-[250ms] ease-out", expanded && "rotate-180 text-fg")} />
    </button>
  );
}

/** One of the five meters: the peak of a resource over the period, as a bar; the one that runs out first says so. */
function Meter({ label, value, note, first, shown }: { label: string; value: number | undefined; note?: string; first?: boolean; shown: string }) {
  const t = useT();
  const pct = value === undefined ? 0 : Math.max(2, Math.min(100, value));
  return (
    <div className="grid h-8 grid-cols-[150px_minmax(0,1fr)_56px_120px] items-center gap-3 @max-[520px]/fzt:h-auto @max-[520px]/fzt:grid-cols-[minmax(0,1fr)_56px] @max-[520px]/fzt:gap-y-1 @max-[520px]/fzt:py-1.5">
      <span>{label}</span>
      <i
        aria-hidden
        className={cx("fz-meter relative h-1.5 overflow-hidden rounded-[3px] bg-step @max-[520px]/fzt:order-3 @max-[520px]/fzt:col-span-full", value !== undefined && value >= 60 && "[--meter:var(--warning)]")}
        style={{ "--w": `${pct}%` } as CSSProperties}
      />
      <b className="text-end font-mono text-[13px] font-medium tabular-nums">
        <Scramble once value={shown} />
      </b>
      <em className={cx("text-[12px] whitespace-nowrap not-italic text-muted @max-[520px]/fzt:hidden", first && "justify-self-start rounded-chip bg-warn-soft px-2 py-0.5 text-warn-text")}>{first ? t("nv.load.first") : note}</em>
    </div>
  );
}

/** What to do about a node, the meters closest to their limit, then the numbers the read went by. */
function Details({ n, hours, fmt, channel }: { n: Row; hours: number; fmt: Fmt; channel?: ChannelLast }) {
  const t = useT();
  const known = n.status !== "unknown";
  const pct = (v: number | undefined) => (v === undefined ? t("load.d.unknown") : `${fmt.num(v, v < 10 ? 1 : 0)}%`);
  const b = (v: number | undefined) => (v === undefined ? t("load.d.unknown") : fmt.bytes(v, 1024));
  const fitWhy = fitKey(n.fit);
  const facts: { label: string; value: string }[] = [
    { label: t("load.d.hours"), value: t("load.d.hours.value", { n: String(n.hoursWithData), total: String(hours) }) },
  ];
  if (known) {
    facts.push(
      { label: t("load.d.busyHour"), value: t("load.d.busyHour.value", { time: fmt.stamp(n.busyHourUnix), cpu: pct(n.busyHourCpuPct), people: fmt.num(n.peopleBusyHour, 0) }) },
      { label: t("load.d.peaks"), value: t("load.d.peaks.value", { typical: pct(n.cpuPeakTypicalPct), top: pct(n.cpuPeakP95Pct) }) },
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
            ? t(n.capacitySource === "configured" ? "load.d.traffic.configured" : "load.d.traffic.nic", {
                typical: pct(n.bandwidthTypicalPct ?? n.bandwidthPeakPct),
                top: pct(n.bandwidthPeakPct),
                mbps: fmt.num(n.capacityMbps, 0),
              })
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
  if (channel) {
    // what the owner's "Measure the channel" found, next to the traffic the number is judged against
    const cap = (d?: { capped: boolean }) => (d?.capped ? `${t("cm.last.capped")} ` : "");
    const mbps = (d?: { mbitS: number }) => t("np.sv.ch.mbps", { n: Math.round(d?.mbitS ?? 0) });
    facts.push({
      label: t("cm.load.label"),
      value: t("cm.last", { when: fmt.ago(Number(channel.atUnix)), down: cap(channel.down) + mbps(channel.down), up: cap(channel.up) + mbps(channel.up) }),
    });
  }
  const [lead, ...rest] = n.recommendations;
  const warn = n.status === "busy" || n.status === "overloaded" || lead?.severity === "crit" || lead?.severity === "warn";
  // the five meters: the peak over the period of what a node runs out of (the first of them says so)
  const meter = (label: string, key: string, value: number | undefined, note?: string) => (
    <Meter key={key} label={label} value={value} note={note} first={known && n.bottleneck === key} shown={value === undefined ? "—" : `${fmt.num(value, value < 10 ? 1 : 0)}%`} />
  );
  return (
    <div className="fz-ld grid animate-[mg-up_0.25s_var(--e)_both] grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)] gap-x-10 gap-y-5 bg-[color-mix(in_srgb,var(--text)_3%,transparent)] px-6 pt-4 pb-6 @max-[940px]/fzt:grid-cols-1 @max-[520px]/fzt:px-4 @max-[520px]/fzt:pt-3 @max-[520px]/fzt:pb-5">
      <div className={cx("col-span-full flex items-center gap-2.5 rounded-field px-3 py-2.5 @max-[520px]/fzt:flex-wrap", warn ? "bg-warn-soft" : "bg-step")}>
        <i aria-hidden className={cx("size-2 flex-none rounded-full @max-[520px]/fzt:mt-2 @max-[520px]/fzt:self-start", warn ? "bg-warn" : "bg-accent-mark")} />
        <b className="font-medium text-pretty @max-[520px]/fzt:min-w-0 @max-[520px]/fzt:flex-1">{lead ? recText(t, lead) : known ? t("load.noRecs") : n.hoursWithData > 0 ? t("load.young") : t("load.noData")}</b>
        <Link to="/nodes/$id" params={{ id: n.nodeId }} className="ms-auto text-[13px] whitespace-nowrap text-fg underline decoration-line underline-offset-4 hover:decoration-current @max-[520px]/fzt:ms-[18px] @max-[520px]/fzt:basis-full">
          {t("fm.openNode")}
        </Link>
      </div>
      {known && (
        <div className={cx(rest.length === 0 && "col-span-full")}>
          <h4 className="mb-2 flex items-baseline justify-between text-[13px] font-medium text-sec">
            {t("nv.load.near")} <span className="text-[12px] font-normal text-muted">{t("nv.load.peak")}</span>
          </h4>
          {meter(t("load.res.cpu"), "cpu", n.cpuPeakP95Pct, n.cpuPeakTypicalPct !== undefined ? t("nv.load.usually", { v: pct(n.cpuPeakTypicalPct) }) : undefined)}
          {meter(t("load.res.ram"), "ram", n.ramPeakPct)}
          {meter(t("load.res.disk"), "disk", n.diskUsedPct, t("nv.load.now"))}
          {meter(t("load.res.bandwidth"), "bandwidth", n.bandwidthPeakPct, n.bandwidthTypicalPct !== undefined ? t("nv.load.usually", { v: pct(n.bandwidthTypicalPct) }) : undefined)}
          {meter(t("load.res.conntrack"), "conntrack", n.conntrackPeakPct)}
        </div>
      )}
      {rest.length > 0 && (
        <ul className="flex flex-col gap-2">
          {rest.map((r, i) => (
            <li key={`${r.code}/${i}`} className="flex items-start gap-2.5 text-[13px] leading-snug">
              <span className="mt-[5px] flex">
                <StatusDot kind={severityKind(r.severity)} />
              </span>
              <span className="min-w-0 flex-1 text-pretty">{recText(t, r)}</span>
            </li>
          ))}
        </ul>
      )}
      <dl className="col-span-full m-0 grid grid-cols-4 gap-x-6 gap-y-3 border-t border-line pt-4 @max-[940px]/fzt:grid-cols-2">
        {facts.map((f) => (
          <div key={f.label} className="min-w-0">
            <dt className="text-[12px] text-muted">{f.label}</dt>
            <dd className="mt-0.5 font-mono text-[13px] font-medium break-words" title={f.value}>
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
