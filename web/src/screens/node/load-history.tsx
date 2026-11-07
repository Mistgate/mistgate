import { queryOptions, useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { ChartCard, type ChartLine } from "@/components/chart";
import { Segmented } from "@/components/ui/segmented";
import { useT } from "@/i18n";
import { nodes } from "@/lib/api";
import { useFmt } from "@/lib/format";
import { plain } from "@/lib/plain";
import { hourSeries, type HistoryMetric } from "@/lib/resources";
import { niceCeil } from "@/lib/series";

// "Load history" on the node's Overview: the node's hourly host load (GetNodeHostHistory), one metric at a time with the
// mean and the peak of each hour, over 24 hours or 7 days. The VPS edition keeps these hours; the Cloudflare one answers
// UNIMPLEMENTED and the card is not drawn. Steal and Mistgate's own CPU exist only in hours an updated agent reported.

type Range = "24h" | "7d";
const rangeHours: Record<Range, number> = { "24h": 24, "7d": 168 };

export const hostHistoryQuery = (nodeId: string, hours: number) =>
  queryOptions({
    queryKey: ["nodes", nodeId, "host-history", hours],
    queryFn: async ({ signal }) => plain(await nodes.getNodeHostHistory({ nodeId, hours }, { signal })),
    refetchInterval: 5 * 60_000, // the hours change once an hour; the current one fills as it goes
    retry: false,
  });

const pad2 = (n: number) => String(n).padStart(2, "0");
const hourLabel = (unix: number) => `${pad2(new Date(unix * 1000).getHours())}:00`;

export function LoadHistory({ nodeId }: { nodeId: string }) {
  const t = useT();
  const fmt = useFmt();
  const [range, setRange] = useState<Range>("24h");
  const [chosen, setMetric] = useState<HistoryMetric>("cpu");
  const hours = rangeHours[range];
  const q = useQuery(hostHistoryQuery(nodeId, hours));
  const data = q.data;
  const ext = useMemo(() => (data?.hours ?? []).some((h) => h.extSamples > 0), [data]);
  // steal and Mistgate's CPU need an updated agent; with none, the choice falls back to CPU
  const metric: HistoryMetric = !ext && (chosen === "steal" || chosen === "agent") ? "cpu" : chosen;
  const s = useMemo(() => hourSeries(data?.hours ?? [], metric, hours, data?.nowUnix ?? 0), [data, metric, hours]);
  if (!data || data.hours.length === 0) return null; // no hours yet, an error, or the edition without them

  const people = metric === "people";
  const top = Math.max(0, ...s.peak);
  const max = people ? Math.max(4, niceCeil(top)) : Math.min(100, Math.max(10, niceCeil(top)));
  const value = (v: number) => (people ? fmt.num(v, v < 10 ? 1 : 0) : `${fmt.num(v, v < 10 ? 1 : 0)}%`);
  const lines: ChartLine[] = [
    { key: "mean", values: s.mean, tone: "accent", width: 2 },
    { key: "peak", values: s.peak, tone: "them", width: 1.5 },
  ];
  // the busiest hour by its mean: the number the capacity read goes by
  let busiest = 0;
  s.mean.forEach((v, i) => {
    if (v >= s.mean[busiest]!) busiest = i;
  });
  const n = s.times.length;
  const daily = range === "7d";
  const xLabels = daily
    ? [0, 1 / 6, 2 / 6, 3 / 6, 4 / 6, 5 / 6, 1].map((f) => new Date(s.times[Math.round(f * (n - 1))]! * 1000).toLocaleDateString(fmt.locale, { weekday: "short" }))
    : [0, 0.25, 0.5, 0.75].map((f) => hourLabel(s.times[Math.round(f * (n - 1))]!)).concat(t("ov.now"));
  const when = (i: number) => {
    const x = s.times[i]!;
    const day = daily ? `${new Date(x * 1000).toLocaleDateString(fmt.locale, { weekday: "short", day: "numeric" })} ` : "";
    return `${day}${hourLabel(x)}–${hourLabel(x + 3600)}`;
  };
  const metricLabel = t(`res.hist.${metric}`);
  const window = t(daily ? "ov.last7" : "ov.last24");

  return (
    <ChartCard
      title={t("res.hist.title")}
      icon="pulse"
      iconTone="lavender"
      control={
        <div className="flex flex-wrap items-center gap-2">
          <Segmented
            variant="flat"
            aria-label={t("res.hist.metric")}
            value={metric}
            onValueChange={setMetric}
            options={(["cpu", "people", ...(ext ? (["steal", "agent"] as const) : [])] as HistoryMetric[]).map((m) => ({ value: m, label: t(`res.hist.${m}`) }))}
          />
          <Segmented
            variant="flat"
            aria-label={t("ov.range")}
            value={range}
            onValueChange={setRange}
            options={[
              { value: "24h", label: t("ov.range24h") },
              { value: "7d", label: t("ov.range7d") },
            ]}
          />
        </div>
      }
      lines={lines}
      max={max}
      height={120}
      yLabels={[value(max), value(max / 2), people ? "0" : "0%"]}
      xLabels={xLabels}
      idle={{ when: t("res.hist.busiest", { window, time: when(busiest) }), main: value(s.mean[busiest] ?? 0) }}
      at={(i) => ({ when: when(i), main: s.samples[i] ? value(s.mean[i] ?? 0) : t("res.hist.noData") })}
      legend={(i) =>
        i !== null && !s.samples[i]
          ? []
          : [
              { key: "mean", label: t("res.hist.mean"), value: value(i === null ? (s.mean[busiest] ?? 0) : (s.mean[i] ?? 0)), tone: "accent" },
              { key: "peak", label: t("res.hist.peak"), value: value(i === null ? top : (s.peak[i] ?? 0)), tone: "them" },
            ]
      }
      label={t("common.labeled", { label: `${t("res.hist.title")}, ${metricLabel}, ${window}`, value: value(s.mean[busiest] ?? 0) })}
    />
  );
}
