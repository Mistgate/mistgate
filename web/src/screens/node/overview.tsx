import type { ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import type { IconName, Tone } from "@/components/ui/icons";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import type { Plain } from "@/lib/plain";
import { useFmt } from "@/lib/format";
import { protocolName } from "@/lib/series";
import { NodeChecks } from "./checks";
import { NodeAlerts, NodeTraffic } from "./overview-parts";

const cardCls = "flex flex-col gap-3 rounded-card-lg border border-line bg-surface p-4";

/** Gauges (CPU with softirq, RAM, disk, network), people online per protocol and the host facts. */
export function OverviewTab({ data }: { data: Plain<GetNodeResponse> }) {
  const t = useT();
  const fmt = useFmt();
  const m = data.metrics;
  const f = data.facts;
  const node = data.node!;
  const offline = node.status === NodeStatus.DOWN || node.status === NodeStatus.BLIP;
  const pct = (used: number, total: number) => (total > 0 ? Math.min(100, (used / total) * 100) : 0);

  const metrics: { label: string; icon: IconName; tone: Tone; value: string; w1: number; w2?: number; sub: string; bar: boolean }[] = [
    {
      label: t("node.m.cpu"),
      icon: "cpu",
      tone: "lavender",
      value: m ? `${Math.round(m.cpuPct)}%` : "—",
      w1: m ? Math.max(0, m.cpuPct - m.softirqPct) : 0,
      w2: m ? m.softirqPct : 0,
      sub: m ? [`softirq ${fmt.num(m.softirqPct, 0)}%`, f?.cpuCount ? `${f.cpuCount} vCPU` : "", `load ${fmt.num(m.load1, 2)}`].filter(Boolean).join(" · ") : "",
      bar: true,
    },
    {
      label: t("node.m.ram"),
      icon: "memory",
      tone: "sage",
      value: m && m.ramTotalBytes > 0 ? `${Math.round(pct(m.ramUsedBytes, m.ramTotalBytes))}%` : "—",
      w1: m ? pct(m.ramUsedBytes, m.ramTotalBytes) : 0,
      sub: m && m.ramTotalBytes > 0 ? t("node.m.of", { used: fmt.bytes(m.ramUsedBytes, 1024), total: fmt.bytes(m.ramTotalBytes, 1024) }) : "",
      bar: true,
    },
    {
      label: t("node.m.disk"),
      icon: "disk",
      tone: "sand",
      value: m && m.diskTotalBytes > 0 ? `${Math.round(pct(m.diskUsedBytes, m.diskTotalBytes))}%` : "—",
      w1: m ? pct(m.diskUsedBytes, m.diskTotalBytes) : 0,
      sub: m && m.diskTotalBytes > 0 ? t("node.m.of", { used: fmt.bytes(m.diskUsedBytes, 1024), total: fmt.bytes(m.diskTotalBytes, 1024) }) : "",
      bar: true,
    },
    {
      label: t("node.m.net"),
      icon: "traffic",
      tone: "sky",
      value: m ? `↓${fmt.mbitNum(m.netRxBps)} ↑${fmt.mbitNum(m.netTxBps)}` : "—",
      w1: 0,
      sub: m ? t("unit.mbit") : "",
      bar: false,
    },
  ];

  const fact = (k: string, v: ReactNode) => ({ k, v });
  const facts: { k: string; v: ReactNode }[] = f
    ? [
        fact(t("node.f.hostname"), f.hostname),
        fact(t("node.f.os"), f.os),
        fact(t("node.f.kernel"), f.kernel),
        fact(t("node.f.arch"), f.arch),
        fact(t("node.f.cpu"), f.cpuCount ? String(f.cpuCount) : ""),
        fact(t("node.f.ram"), f.ramTotalBytes ? fmt.bytes(f.ramTotalBytes, 1024) : ""),
        fact(t("node.f.disk"), f.diskTotalBytes ? fmt.bytes(f.diskTotalBytes, 1024) : ""),
        fact(t("node.f.virt"), f.virt),
        fact("IPv6", f.hasIpv6 ? t("common.yes") : t("common.no")),
        fact(t("node.f.booted"), f.bootUnix ? fmt.dateTime(f.bootUnix) : ""),
        fact(
          t("node.f.engines"),
          f.engines.length ? (
            <span className="flex flex-col items-end gap-0.5">
              {f.engines.map((e) => (
                <span key={e}>{e}</span>
              ))}
            </span>
          ) : (
            ""
          ),
        ),
      ]
    : [];

  return (
    <div className="flex flex-col gap-3.5">
      <div className="grid grid-cols-2 gap-2.5 md:grid-cols-4">
        {metrics.map((x) => (
          <div key={x.label} className="flex min-w-0 flex-col gap-2.5 rounded-card border border-line bg-surface p-3.5">
            <SectionLabel as="span" icon={x.icon} tone={x.tone}>
              {x.label}
            </SectionLabel>
            <span className="font-mono text-[22px] leading-none font-bold tracking-[-0.04em] whitespace-nowrap">{x.value}</span>
            {x.bar ? (
              <div className="flex h-1.5 gap-px overflow-hidden rounded-[3px] bg-surface-2">
                <span className="bg-accent" style={{ width: `${x.w1}%` }} />
                <span className="bg-warn" style={{ width: `${x.w2 ?? 0}%` }} />
              </div>
            ) : (
              <div className="h-1.5" />
            )}
            <span className="min-h-3.5 text-[11px] text-muted">{x.sub}</span>
          </div>
        ))}
      </div>
      {/* a node that was there and went quiet has stale readings, not "not yet" ones */}
      {(!m || offline) && (
        <p className="-mt-1 text-xs text-muted">{offline && node.lastSeenUnix > 0 ? t("node.m.offline", { time: fmt.stamp(node.lastSeenUnix) }) : t("node.m.none")}</p>
      )}
      <NodeTraffic nodeId={node.id} />

      <div className="grid items-start gap-3.5 md:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)]">
        <div className="flex min-w-0 flex-col gap-3.5">
          <NodeAlerts nodeId={node.id} nodeName={node.name} />
          <NodeChecks nodeId={node.id} />
          <section className={cardCls}>
            <SectionLabel as="h2" icon="people" tone="mint">
              {t("node.onlineBy")}
            </SectionLabel>
            <div className="flex flex-wrap gap-6">
              {node.online.length === 0 && (
                <div className="flex flex-col gap-0.5">
                  <span className="font-mono text-[22px] font-bold">0</span>
                  <span className="text-xs text-muted">{offline ? t("node.onlineOffline") : t("node.onlineNone")}</span>
                </div>
              )}
              {node.online.map((o) => (
                <div key={o.protocol} className="flex flex-col gap-0.5">
                  <span className="font-mono text-[22px] font-bold">{o.users}</span>
                  <span className="text-xs text-muted">{protocolName(o.protocol)}</span>
                </div>
              ))}
            </div>
          </section>
        </div>
        <section className="flex flex-col rounded-card-lg border border-line bg-surface px-4 pt-4 pb-1.5">
          <SectionLabel as="h2" icon="info" tone="sand" className="pb-2">
            {t("node.facts")}
          </SectionLabel>
          {facts.length === 0 && <p className="border-t border-line py-3 text-[13px] text-muted">{t("node.factsNone")}</p>}
          {facts
            .filter(({ v }) => v !== "")
            .map(({ k, v }) => (
              <div key={k} className="flex min-h-[38px] items-center gap-2.5 border-t border-line py-1.5 text-[13px]">
                <span className="flex-1 text-muted">{k}</span>
                <span className="min-w-0 text-right font-mono text-xs font-semibold break-all">{v}</span>
              </div>
            ))}
        </section>
      </div>
    </div>
  );
}
