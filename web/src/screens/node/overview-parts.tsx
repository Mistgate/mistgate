import { queryOptions, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useMemo } from "react";
import { ChartCard } from "@/components/chart";
import { SectionLabel } from "@/components/ui/bits";
import { StatusDot } from "@/components/ui/status";
import { useT } from "@/i18n";
import { health } from "@/lib/api";
import { scaleBytes, useFmt } from "@/lib/format";
import { alertTitle, resolutionWord, severityKind } from "@/lib/health";
import { plain } from "@/lib/plain";
import { overviewQuery, pollMs } from "@/lib/queries";
import { niceCeil } from "@/lib/series";
import { useNow } from "@/lib/time";
import { AlertCard } from "@/screens/health/alerts";
import { useFixFlow } from "@/screens/health/fix";

/** The alerts of one node, open and of the last 7 days; under "health", so a mute or a fix refreshes it with the Health page. */
export const nodeAlertsQuery = (nodeId: string) =>
  queryOptions({
    queryKey: ["health", "alerts", nodeId],
    queryFn: async ({ signal }) => plain(await health.listAlerts({ nodeId }, { signal })),
    refetchInterval: pollMs,
  });

/** "Alerts" on the node's Overview: what is open on this node now, with the same buttons as on the Health page, and the last three that closed. */
export function NodeAlerts({ nodeId, nodeName }: { nodeId: string; nodeName: string }) {
  const t = useT();
  const fmt = useFmt();
  const q = useQuery(nodeAlertsQuery(nodeId));
  const flow = useFixFlow();
  const clock = useNow();
  if (!q.data) return null;
  const now = q.data.nowUnix || Math.floor(clock / 1000);
  const active = q.data.active;
  const closed = q.data.history.slice(0, 3);
  if (active.length === 0 && closed.length === 0) return null;
  return (
    <section className="flex flex-col gap-3 rounded-card-lg border border-line bg-surface p-4">
      <div className="flex items-center gap-2">
        <SectionLabel as="h2" icon="bell" tone="rose" className="flex-1">
          {t("node.alerts")}
        </SectionLabel>
        <Link to="/health" className="text-xs font-bold text-muted hover:text-fg">
          {t("node.alertsAll")}
        </Link>
      </div>
      {active.length === 0 ? (
        <p className="text-[13px] text-muted">{t("node.alertsQuiet", { name: nodeName })}</p>
      ) : (
        <div className="flex flex-col gap-2.5 [&>article]:bg-surface-2">
          {active.map((a) => (
            <AlertCard key={a.id} alert={a} now={now} flow={flow} onNodePage />
          ))}
        </div>
      )}
      {closed.length > 0 && (
        <div className="flex flex-col">
          <span className="pb-1.5 text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("node.alertsClosed")}</span>
          {closed.map((h) => (
            <div key={`${h.id}/${h.resolvedAtUnix}`} className="flex items-start gap-2.5 border-t border-line py-2 text-[13px]">
              <span className="mt-[5px] flex">
                <StatusDot kind={severityKind(h.severity)} />
              </span>
              <span className="min-w-0 flex-1 leading-snug">
                {alertTitle(t, h)}
                <span className="text-xs text-muted">
                  {" · "}
                  {t("hl.alerts.lasted", { duration: fmt.duration(Math.max(0, h.resolvedAtUnix - (h.openedUnix || h.firstSeenUnix))) })} · {t(resolutionWord(h.resolution))}
                </span>
              </span>
              <span className="font-mono text-[11px] whitespace-nowrap text-muted">{fmt.stamp(h.resolvedAtUnix)}</span>
            </div>
          ))}
        </div>
      )}
      {flow.modal}
    </section>
  );
}

const pad2 = (n: number) => String(n).padStart(2, "0");
const hourLabel = (unix: number) => `${pad2(new Date(unix * 1000).getHours())}:00`;

/** "Traffic · 24 h" of one node: the hourly bytes the fleet Overview already brings for its card (no request of its own). */
export function NodeTraffic({ nodeId }: { nodeId: string }) {
  const t = useT();
  const fmt = useFmt();
  const q = useQuery(overviewQuery());
  const spark = q.data?.nodes.find((n) => n.id === nodeId)?.sparkBytes;
  const now = q.data?.nowUnix ?? 0;
  const s = useMemo(() => {
    const values = spark ?? [];
    const start = now - (now % 3600) - (values.length - 1) * 3600;
    return { values, times: values.map((_, i) => start + i * 3600) };
  }, [spark, now]);
  const total = s.values.reduce((a, b) => a + b, 0);
  if (s.values.length < 2 || total === 0) return null;

  const peak = Math.max(...s.values);
  const unit = scaleBytes(peak).unit;
  const div = 1000 ** unit;
  const step = niceCeil(peak / div / 2);
  const digits = step < 1 ? 2 : 1;
  const unitName = t(`unit.${(["B", "KB", "MB", "GB", "TB"] as const)[unit]}`);
  const n = s.times.length;
  return (
    <ChartCard
      title={`${t("node.traffic")} · ${t("ov.unitHour", { unit: unitName })}`}
      icon="traffic"
      iconTone="sky"
      lines={[{ key: "total", values: s.values.map((v) => v / div), tone: "accent", width: 2 }]}
      max={step * 2}
      height={110}
      yLabels={[fmt.num(step * 2, digits), fmt.num(step, digits), "0"]}
      xLabels={[0, 0.25, 0.5, 0.75].map((f) => hourLabel(s.times[Math.round(f * (n - 1))]!)).concat(t("ov.now"))}
      idle={{ when: t("ov.last24"), main: fmt.bytes(total) }}
      at={(i) => ({ when: `${hourLabel(s.times[i]!)}–${hourLabel(s.times[i]! + 3600)}`, main: fmt.bytes(s.values[i] ?? 0) })}
      legend={() => []}
      label={`${t("node.traffic")}: ${fmt.bytes(total)}`}
    />
  );
}
