import type { HostHour, NodeMetrics, ProcessUsage } from "@/gen/mistgate/admin/v1/node_pb";
import type { LoadRecommendation, NodeLoad } from "@/gen/mistgate/ops/v1/ops_pb";
import type { StatusKind } from "@/components/ui/status";
import type { MessageKey } from "@/i18n/en";
import type { Plain } from "./plain";

// What a node's machine spends and what Mistgate itself costs it (NodeMetrics fields 11 to 20, HostHour 14 to 26), and the
// fleet's load (OpsService.GetFleetLoad). An agent that predates the resource fields sends no `agent`: then every one of them
// is "not known", never zero, and the helpers here answer null.

type Metrics = Plain<NodeMetrics>;

const clamp = (v: number) => (Number.isFinite(v) ? Math.min(100, Math.max(0, v)) : 0);

/** The agent reports the resource fields (HostMetrics.agent is there). */
export const hasResources = (m: Metrics | undefined): m is Metrics & { agent: Plain<ProcessUsage> } => !!m?.agent;

/**
 * The machine's CPU split into what took it, in percent of all CPUs, adding up to the busy share: Mistgate (the agent
 * process, every engine inside it), the kernel's softirq (packets, a kernel AmneziaWG), steal (the hypervisor gave it to
 * other guests) and everything else. null without the resource fields.
 */
export function cpuSplit(m: Metrics | undefined): { agent: number; softirq: number; steal: number; other: number; busy: number; iowait: number } | null {
  if (!hasResources(m)) return null;
  const busy = clamp(m.cpuPct);
  const steal = Math.min(clamp(m.stealPct), busy);
  const softirq = Math.min(clamp(m.softirqPct), busy - steal);
  const agent = Math.min(clamp(m.agent.cpuPct), busy - steal - softirq);
  return { agent, softirq, steal, other: Math.max(0, busy - steal - softirq - agent), busy, iowait: clamp(m.iowaitPct) };
}

/** Memory in percent of the RAM: the agent's resident memory, the rest in use, and swap in use of its size. */
export function memSplit(m: Metrics | undefined): { agent: number; other: number; used: number; swap: number | null } | null {
  if (!hasResources(m) || m.ramTotalBytes <= 0) return null;
  const used = clamp((m.ramUsedBytes / m.ramTotalBytes) * 100);
  const agent = Math.min(clamp((m.agent.rssBytes / m.ramTotalBytes) * 100), used);
  return { agent, other: used - agent, used, swap: m.swapTotalBytes > 0 ? clamp((m.swapUsedBytes / m.swapTotalBytes) * 100) : null };
}

/** Connection tracking entries against the limit, percent; null when conntrack is not loaded or not known. */
export const conntrackFill = (m: Metrics | undefined): number | null =>
  hasResources(m) && m.conntrackMax > 0 ? clamp((m.conntrackCount / m.conntrackMax) * 100) : null;

/** The agent's open files against its soft limit, percent; null when the limit is not known. */
export const fdFill = (m: Metrics | undefined): number | null =>
  hasResources(m) && m.agent.fdLimit > 0 ? clamp((m.agent.openFds / m.agent.fdLimit) * 100) : null;

/** The busier direction of the main NIC against its link speed, percent; null when the driver does not say. */
export const linkUse = (m: Metrics | undefined): number | null =>
  hasResources(m) && m.nicSpeedMbps > 0 ? clamp((Math.max(m.netRxBps, m.netTxBps) / (m.nicSpeedMbps * 1e6)) * 100) : null;

/** The meter's tone at a fill: the doctor's thresholds (warn 80%, fail 95%). */
export const fillTone = (pct: number): "ok" | "warn" | "bad" => (pct >= 95 ? "bad" : pct >= 80 ? "warn" : "ok");

// ---------------------------------------------------------------------------------------------------------------------
// The node's hours (GetNodeHostHistory)

export type HistoryMetric = "cpu" | "people" | "steal" | "agent";

export type HourSeries = {
  /** Start of each hour, unix seconds, every hour of the window (an hour without samples holds zeros, `samples` 0). */
  times: number[];
  samples: number[];
  mean: number[];
  peak: number[];
  /** At least one hour carried the resource fields: steal and the agent's CPU are known. */
  ext: boolean;
};

/**
 * One metric of the hourly host load as dense series over the last `hours` hours up to `nowUnix`: the mean and the peak of
 * each hour. Steal and the agent's CPU come only from hours whose samples carried them.
 */
export function hourSeries(rows: readonly Plain<HostHour>[], metric: HistoryMetric, hours: number, nowUnix: number): HourSeries {
  const newest = nowUnix - (nowUnix % 3600);
  const times = Array.from({ length: hours }, (_, i) => newest - (hours - 1 - i) * 3600);
  const at = new Map(times.map((t, i) => [t, i]));
  const s: HourSeries = { times, samples: times.map(() => 0), mean: times.map(() => 0), peak: times.map(() => 0), ext: false };
  for (const h of rows) {
    const i = at.get(h.hourStartUnix);
    if (i === undefined) continue;
    s.samples[i] = h.samples;
    const ext = h.extSamples > 0;
    s.ext ||= ext;
    const [mean, peak] =
      metric === "cpu"
        ? [h.cpuAvgPct, h.cpuMaxPct]
        : metric === "people"
          ? [h.sessionsAvg, h.sessionsMax]
          : metric === "steal"
            ? ext ? [h.stealAvgPct, h.stealMaxPct] : [0, 0]
            : ext ? [h.agentCpuAvgPct, h.agentCpuMaxPct] : [0, 0];
    s.mean[i] = Number.isFinite(mean) ? Math.max(0, mean) : 0;
    s.peak[i] = Number.isFinite(peak) ? Math.max(0, peak) : 0;
  }
  return s;
}

// ---------------------------------------------------------------------------------------------------------------------
// The fleet's load (OpsService.GetFleetLoad)

export type LoadStatus = "ok" | "busy" | "overloaded" | "unknown";

export const loadStatusKind = (s: string): StatusKind => (s === "overloaded" ? "bad" : s === "busy" ? "warn" : s === "ok" ? "ok" : "off");

export const isLoadStatus = (s: string): s is LoadStatus => s === "ok" || s === "busy" || s === "overloaded" || s === "unknown";

const resources = ["cpu", "steal", "bandwidth", "ram", "conntrack", "disk"] as const;
export const bottleneckKey = (b: string): MessageKey | null => ((resources as readonly string[]).includes(b) ? (`load.res.${b}` as MessageKey) : null);

const recCodes = ["update_agent", "steal_high", "conntrack_near_limit", "cpu_busy", "cpu_busy_add_node", "bandwidth_busy", "ram_high", "swap_in_use", "disk_filling", "agent_memory_growing"] as const;

/** The sentence of a recommendation: its code's, with the variant that names a target node when the panel found one; null for a code this page does not know (a newer panel). */
export function recKey(r: Pick<Plain<LoadRecommendation>, "code" | "params">): MessageKey | null {
  if (!(recCodes as readonly string[]).includes(r.code)) return null;
  if (r.code === "bandwidth_busy" && r.params.target) return "load.rec.bandwidth_busy_to";
  return `load.rec.${r.code}` as MessageKey;
}

export const severityKind = (s: string): StatusKind => (s === "crit" ? "bad" : s === "warn" ? "warn" : "off");

/** Why the per-person costs are not known, as a key; null when they are (fit "ok"). */
export const fitKey = (fit: string): MessageKey | null =>
  fit === "few_hours" || fit === "flat" || fit === "no_trend" ? (`load.fit.${fit}` as MessageKey) : fit === "ok" ? null : "load.fit.few_hours";

/** Most pressing first, as the panel sends them; a stable order for nodes the panel ranks equal. */
export const loadRank = (n: Pick<Plain<NodeLoad>, "status">) => ({ overloaded: 0, busy: 1, ok: 2, unknown: 3 })[isLoadStatus(n.status) ? n.status : "unknown"];
