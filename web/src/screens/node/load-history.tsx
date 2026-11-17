import { keepPreviousData, queryOptions, useQuery } from "@tanstack/react-query";
import { useId, useMemo, useState, type CSSProperties, type KeyboardEvent, type PointerEvent } from "react";
import { Segmented } from "@/components/ui/segmented";
import { OverviewRange } from "@/gen/mistgate/admin/v1/fleet_pb";
import { useT } from "@/i18n";
import { nodes } from "@/lib/api";
import { cx } from "@/lib/cx";
import { scaleBytes, useFmt } from "@/lib/format";
import { plain } from "@/lib/plain";
import { overviewQuery } from "@/lib/queries";
import { hourSeries, type HistoryMetric } from "@/lib/resources";
import { CardHead } from "@/screens/overview-parts";
import { gridStep, linePath, PLOT_H, PLOT_W } from "@/screens/overview-chart";

// "Load history" on the node's Overview (the board's chart of PnNode): the node's hourly host load (GetNodeHostHistory), one thing at
// a time (CPU, people, steal, Mistgate's own CPU, traffic) with the mean and the peak of each hour, over 24 hours or 7 days. The VPS
// edition keeps these hours; the Cloudflare one answers UNIMPLEMENTED and the card is not drawn. Steal and Mistgate's own CPU exist
// only in hours an updated agent reported. Traffic is the node's bytes per hour of the last 24 hours (the fleet Overview brings
// them), so for a week it says so.

type Range = "24h" | "7d";
type Kind = HistoryMetric | "traffic";
const rangeHours: Record<Range, number> = { "24h": 24, "7d": 168 };

export const hostHistoryQuery = (nodeId: string, hours: number) =>
  queryOptions({
    queryKey: ["nodes", nodeId, "host-history", hours],
    queryFn: async ({ signal }) => plain(await nodes.getNodeHostHistory({ nodeId, hours }, { signal })),
    refetchInterval: 5 * 60_000, // the hours change once an hour; the current one fills as it goes
    retry: false,
    placeholderData: keepPreviousData,
  });

const pad2 = (n: number) => String(n).padStart(2, "0");
const hourLabel = (unix: number) => `${pad2(new Date(unix * 1000).getHours())}:00`;

export function LoadHistory({ nodeId, dead }: { nodeId: string; dead?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const uid = useId().replace(/:/g, "");
  const [range, setRange] = useState<Range>("24h");
  const [chosen, setKind] = useState<Kind>("cpu");
  const [hover, setHover] = useState(-1);
  const hours = rangeHours[range];
  const q = useQuery(hostHistoryQuery(nodeId, hours));
  const fleet = useQuery(overviewQuery(OverviewRange.OVERVIEW_RANGE_24H));
  const data = q.data;
  const ext = useMemo(() => (data?.hours ?? []).some((h) => h.extSamples > 0), [data]);
  // steal and Mistgate's CPU need an updated agent; with none, the choice falls back to CPU
  const kind: Kind = !ext && (chosen === "steal" || chosen === "agent") ? "cpu" : chosen;
  const metric: HistoryMetric = kind === "traffic" ? "cpu" : kind;
  const s = useMemo(() => hourSeries(data?.hours ?? [], metric, hours, data?.nowUnix ?? 0), [data, metric, hours]);

  // the node's bytes of the last 24 hours, oldest first
  const traffic = kind === "traffic";
  const sparkAll = fleet.data?.nodes.find((n) => n.id === nodeId)?.sparkBytes;
  const daily = range === "7d";
  const week = traffic && daily;
  const sparkNow = fleet.data?.nowUnix ?? 0;
  const tSeries = useMemo(() => {
    const spark = sparkAll ?? [];
    const start = sparkNow - (sparkNow % 3600) - (spark.length - 1) * 3600;
    const times = spark.map((_, i) => start + i * 3600);
    return { times, samples: spark.map(() => 1), mean: [...spark], peak: [...spark], ext: true };
  }, [sparkAll, sparkNow]);
  const series = traffic ? tSeries : s;

  if (q.isError && !data) return null; // the Cloudflare edition: no hours
  if (!data) return null;
  const nothing = data.hours.length === 0;

  const people = kind === "people";
  const n = series.times.length;
  const topPeak = Math.max(0, ...series.peak, ...series.mean);
  // traffic is drawn in one unit (GB, MB...) so the axis reads in round numbers
  const size = scaleBytes(topPeak);
  const div = traffic ? 1000 ** size.unit : 1;
  const unit = traffic ? t(`unit.${(["B", "KB", "MB", "GB", "TB"] as const)[size.unit]}`) : people ? "" : "%";
  const step = traffic || people ? gridStep(topPeak / div) : gridStep(Math.min(100, topPeak));
  const top = Math.min(traffic || people ? Infinity : 100, step * 4);
  const fv = (v: number) => (traffic ? fmt.num(v / div, v / div >= 100 ? 0 : 1) : fmt.num(v, v < 10 ? 1 : 0));
  const f = (v: number) => (traffic ? `${fv(v)} ${unit}` : people ? fv(v) : `${fv(v)}%`);
  const yLabel = (v: number) => (traffic ? fmt.num(v, 0) : people ? fmt.num(v, 0) : `${fmt.num(v, 0)}%`);
  const hi = hover < 0 || hover >= n ? n - 1 : hover;
  const left = n < 2 ? 50 : (hi / (n - 1)) * 100;
  const y = (v: number) => (1 - Math.min(v / div, top) / top) * 100;
  const peakAt = Math.max(0, series.mean.indexOf(Math.max(0, ...series.mean)));
  const tPeakAt = Math.max(0, series.peak.indexOf(Math.max(0, ...series.peak)));
  const when = (i: number) => {
    const x = series.times[i];
    if (x === undefined) return "";
    const day = daily ? `${new Date(x * 1000).toLocaleDateString(fmt.locale, { weekday: "short", day: "numeric" })} ` : "";
    return `${day}${hourLabel(x)}–${hourLabel(x + 3600)}`;
  };
  const xl = daily
    ? [0, 1 / 6, 2 / 6, 3 / 6, 4 / 6, 5 / 6, 1].map((q2) => new Date((series.times[Math.round(q2 * (n - 1))] ?? 0) * 1000).toLocaleDateString(fmt.locale, { weekday: "short" }))
    : [0, 0.25, 0.5, 0.75].map((q2) => hourLabel(series.times[Math.round(q2 * (n - 1))] ?? 0)).concat(t("ov.now"));
  const noSample = (i: number) => !traffic && !series.samples[i];

  const lines = [{ key: "peak", v: series.peak, cls: "ln pk" }, { key: "mean", v: series.mean, cls: "ln" }];
  const total = traffic ? series.mean.reduce((a, b) => a + b, 0) : 0;
  // the newest hour that has readings is "now"
  const lastAt = traffic ? n - 1 : Math.max(0, series.samples.map((x, i) => (x ? i : -1)).reduce((a, b) => Math.max(a, b), -1));
  const nowValue = traffic ? fv(total) : fv(series.mean[lastAt] ?? 0);
  const note = traffic
    ? t("np.chart.trafficNote")
    : people
      ? t("np.chart.peopleNote", { window: t(daily ? "ov.last7" : "ov.last24"), time: when(peakAt) })
      : t("res.hist.busiest", { window: t(daily ? "ov.last7" : "ov.last24"), time: when(peakAt) }) + (kind === "steal" ? ` · ${t("np.chart.stealNote")}` : kind === "agent" ? ` · ${t("np.chart.agentNote")}` : "");

  function move(e: PointerEvent<HTMLDivElement>) {
    const r = e.currentTarget.getBoundingClientRect();
    if (n > 1 && r.width > 0) setHover(Math.max(0, Math.min(n - 1, Math.round(((e.clientX - r.left) / r.width) * (n - 1)))));
  }
  function key(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
      e.preventDefault();
      setHover(Math.max(0, Math.min(n - 1, hi + (e.key === "ArrowLeft" ? -1 : 1))));
    } else if (e.key === "Escape") setHover(-1);
  }
  const kinds: Kind[] = ["cpu", "people", ...(ext ? (["steal", "agent"] as const) : []), "traffic"];

  return (
    <section className={cx("rounded-card border border-line bg-surface", dead && "[&_.nd-gv]:opacity-35 [&_.nd-plot]:opacity-35")}>
      <CardHead>
        <Segmented
          variant="flat"
          aria-label={t("res.hist.metric")}
          value={kind}
          onValueChange={(v) => {
            setKind(v);
            setHover(-1);
          }}
          className="max-w-full flex-nowrap overflow-x-auto"
          options={kinds.map((k) => ({ value: k, label: t(k === "traffic" ? "np.chart.traffic" : `res.hist.${k}`) }))}
        />
        <span className="flex-1" />
        <Segmented
          variant="flat"
          aria-label={t("ov.range")}
          value={range}
          onValueChange={(v) => {
            setRange(v);
            setHover(-1);
          }}
          options={[
            { value: "24h", label: t("ov.range24h") },
            { value: "7d", label: t("ov.range7d") },
          ]}
        />
      </CardHead>
      {nothing && !traffic ? (
        <div className="nd-nh mx-6 mt-2 mb-6 flex flex-col items-start gap-1 rounded-xl border border-dashed border-line px-6 py-8 max-[720px]:mx-4">
          <b className="font-medium">{t("np.chart.soonTitle")}</b>
          <span className="text-[13px] text-sec">{t("np.chart.soonBody")}</span>
          <i aria-hidden className="nd-bar mt-3 w-60 max-w-full" style={{ "--w": `${Math.max(4, Math.min(100, ((data.nowUnix - data.sinceUnix) / 3600) * 100))}%` } as CSSProperties} />
        </div>
      ) : week ? (
        <div className="nd-nh mx-6 mt-2 mb-6 flex flex-col items-start gap-1 rounded-xl border border-dashed border-line px-6 py-8 max-[720px]:mx-4">
          <b className="font-medium">{t("np.chart.weekTitle")}</b>
          <span className="text-[13px] text-sec">{t("np.chart.weekBody")}</span>
        </div>
      ) : (
        <>
          <div className="nd-gv flex flex-wrap items-start gap-x-6 gap-y-2 px-6 pt-1 max-[720px]:px-4">
            <div>
              <div className="text-[32px] leading-9 font-medium tracking-[-0.02em] tabular-nums">
                {nowValue}
                <small className="ms-1 text-sm font-normal tracking-normal text-sec">{traffic ? unit : people ? t("np.chart.onlineNow") : t("np.chart.nowPct")}</small>
              </div>
              <div className="mt-1 text-[13px] text-muted">{note}</div>
            </div>
            <div className="ms-auto flex flex-wrap gap-x-5 gap-y-1 pt-2.5 text-[13px] text-sec">
              {traffic ? (
                <span className="inline-flex items-center gap-2">
                  <i aria-hidden className="h-0.5 w-3 rounded-[1px] bg-accent-mark" />
                  {t("np.chart.nodeTraffic")} <b className="font-mono font-medium text-fg tabular-nums">{f(total)}</b>
                </span>
              ) : (
                <>
                  <span className="inline-flex items-center gap-2">
                    <i aria-hidden className="h-0.5 w-3 rounded-[1px] bg-accent-mark" />
                    {t("res.hist.mean")} <b className="font-mono font-medium text-fg tabular-nums">{f(series.mean[peakAt] ?? 0)}</b>
                  </span>
                  <span className="inline-flex items-center gap-2">
                    <i aria-hidden className="h-0.5 w-3 rounded-[1px] bg-[color-mix(in_srgb,var(--text)_55%,transparent)]" />
                    {t("res.hist.peak")} <b className="font-mono font-medium text-fg tabular-nums">{f(topPeak)}</b>
                  </span>
                </>
              )}
            </div>
          </div>
          {/* time runs left to right in every language: the axis labels sit under the svg's own coordinates */}
          <div dir="ltr">
            <div
              role="img"
              aria-label={t("common.labeled", { label: `${t("res.hist.title")}, ${traffic ? t("np.chart.traffic") : t(`res.hist.${kind}`)}, ${t(daily ? "ov.last7" : "ov.last24")}`, value: traffic ? f(total) : f(series.mean[peakAt] ?? 0) })}
              tabIndex={0}
              onPointerMove={move}
              onPointerDown={move}
              onPointerLeave={() => setHover(-1)}
              onBlur={() => setHover(-1)}
              onKeyDown={key}
              className="nd-plot relative mt-4 me-6 ms-14 h-60 cursor-crosshair touch-pan-y outline-none focus-visible:outline-2 focus-visible:outline-accent-mark max-[720px]:me-4 max-[720px]:ms-11 max-[720px]:h-[184px]"
            >
              <svg viewBox={`0 0 ${PLOT_W} ${PLOT_H}`} preserveAspectRatio="none" aria-hidden>
                <defs>
                  <linearGradient id={`${uid}-a`} x1="0" y1="0" x2="0" y2="1">
                    <stop offset="0" stopColor="var(--accent)" stopOpacity={0.34} />
                    <stop offset="1" stopColor="var(--accent)" stopOpacity={0.03} />
                  </linearGradient>
                </defs>
                <path className="grid" d="M0 0.5H720M0 60H720M0 120H720M0 180H720" />
                <path className="base" d="M0 239.5H720" />
                {(() => {
                  const d = linePath(series.mean.map((v) => v / div), top, people && !traffic);
                  return <path className="area" fill={`url(#${uid}-a)`} d={d ? `${d}L${PLOT_W} ${PLOT_H}L0 ${PLOT_H}Z` : ""} />;
                })()}
                {!traffic && <path className="ln pk" d={linePath(series.peak.map((v) => v / div), top, people)} />}
                <path className="ln" d={linePath(series.mean.map((v) => v / div), top, people && !traffic)} />
              </svg>
              {[4, 3, 2, 1].map((qq, i) => (
                <span key={qq} aria-hidden className="absolute -translate-y-1/2 font-mono text-[11px] text-muted" style={{ insetInlineEnd: "calc(100% + 12px)", top: `${i * 25}%` }}>
                  {yLabel(step * qq)}
                </span>
              ))}
              {topPeak > 0 && (
                <span aria-hidden className="pointer-events-none absolute flex -translate-x-1/2 -translate-y-full flex-col items-center gap-1 pb-1.5" style={{ left: `${n < 2 ? 50 : (tPeakAt / (n - 1)) * 100}%`, top: `${y(series.peak[tPeakAt] ?? 0)}%` }}>
                  <b className="rounded-chip bg-surface-2 px-1.5 font-mono text-[11px] leading-4 font-medium whitespace-nowrap text-sec">{t("np.chart.peakMark", { value: f(series.peak[tPeakAt] ?? 0) })}</b>
                  <i className="order-2 -mb-[9px] size-1.5 rounded-full bg-tone-lavender" />
                </span>
              )}
              <span aria-hidden className="pointer-events-none absolute top-0 bottom-0 w-px bg-[color-mix(in_srgb,var(--text)_16%,transparent)]" style={{ left: `${left}%` }} />
              {!noSample(hi) && <span aria-hidden className="pointer-events-none absolute -mt-1 -ml-1 size-2 rounded-full bg-accent-mark shadow-[0_0_0_3px_var(--surface)]" style={{ left: `${left}%`, top: `${y(series.mean[hi] ?? 0)}%` }} />}
              <div
                aria-hidden
                className="pointer-events-none absolute top-2 z-[2] min-w-[184px] rounded-field border border-line bg-surface-2 px-3 py-2.5 text-xs leading-4 whitespace-nowrap"
                style={{ left: `${left}%`, transform: hi > n / 2 ? "translateX(calc(-100% - 16px))" : "translateX(16px)" }}
              >
                <b className="mb-1.5 block font-mono font-medium text-muted">{when(hi)}</b>
                {noSample(hi) ? (
                  <span className="flex items-center gap-2 py-0.5 text-sec">{t("res.hist.noData")}</span>
                ) : (
                  lines
                    .filter((l) => !traffic || l.key === "mean")
                    .reverse()
                    .map((l) => (
                      <span key={l.key} className="flex items-center gap-2 py-0.5">
                        <i className={cx("h-0.5 w-2", l.key === "mean" ? "bg-accent-mark" : "bg-[color-mix(in_srgb,var(--text)_55%,transparent)]")} />
                        <span className="text-sec">{traffic ? t("np.chart.nodeTraffic") : t(l.key === "mean" ? "res.hist.mean" : "res.hist.peak")}</span>
                        <em className="ms-auto ps-4 font-mono font-medium not-italic tabular-nums">{f(l.v[hi] ?? 0)}</em>
                      </span>
                    ))
                )}
              </div>
            </div>
            <div aria-hidden className="mt-2 mb-5 me-6 ms-14 flex justify-between font-mono text-[11px] text-muted max-[720px]:mb-4 max-[720px]:me-4 max-[720px]:ms-11">
              {xl.map((x, i) => (
                <span key={i}>{x}</span>
              ))}
            </div>
          </div>
        </>
      )}
    </section>
  );
}
