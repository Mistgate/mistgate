export type LoadLevel = "low" | "medium" | "high";

/** A sample older than this is not "now" any more (access/node_load.go networkSampleMaxAge). */
const sampleMaxAgeS = 90;

/**
 * The load the people of a node see on their page, from the node's latest metrics: the larger of the busier network
 * direction against the configured capacity and the CPU, 0-100. The same rule as access/node_load.go
 * CurrentNetworkUtilization and subs loadLevel, so the admin and the page never disagree. null without a fresh sample.
 */
export function nodeLoad(
  m: { cpuPct: number; netRxBps: number; netTxBps: number; atUnix: number } | undefined,
  capacityMbps: number,
  nowUnix: number,
): { percent: number; level: LoadLevel } | null {
  if (!m || m.atUnix <= 0) return null;
  const age = nowUnix - m.atUnix;
  if (age < -5 || age > sampleMaxAgeS) return null;
  const clamp = (n: number) => Math.min(100, Math.max(0, Math.round(n)));
  let percent = clamp(m.cpuPct);
  if (capacityMbps > 0) percent = Math.max(percent, clamp((Math.max(m.netRxBps, m.netTxBps) / (capacityMbps * 1_000_000)) * 100));
  return { percent, level: percent >= 80 ? "high" : percent >= 50 ? "medium" : "low" };
}
