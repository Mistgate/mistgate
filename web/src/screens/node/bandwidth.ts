import type { T } from "@/i18n";

/**
 * The capacity a measurement gives a VPN node: the slower direction. Every byte a person downloads comes in from the
 * internet and goes out to them, and providers often leave the inbound free while they cap the outbound. Without an
 * upload figure, the download. (The server's auto-measure does the same: fleet/bandwidth.go capacityOf.)
 */
export const capacityOf = (down: number, up: number): number => (up > 0 && up < down ? up : down);

/** The node's answer, the part about how the runs went. An agent that does not say has runsTotal 0: it is read as "all worked". */
export type RunInfo = { server: string; serverDetail: string; runs: number; runsTotal: number; runFailures: readonly string[] };

/** Why a run did not work, in words (the reason codes of the agent; an unknown one, or none, is the generic sentence). */
function reasonText(t: T, code: string): string {
  const http = /^http_(\d{3})$/.exec(code);
  if (http) return t("node.settings.bandwidthFail.http", { code: http[1]! });
  switch (code) {
    case "rate_limited":
    case "timeout":
    case "unreachable":
      return t(`node.settings.bandwidthFail.${code}`);
    default:
      return t("node.settings.bandwidthFail.failed");
  }
}

/**
 * "Through Ookla · MTS, Moscow, the best of 3 runs" or, when some failed, "Through speed.cloudflare.com, 1 of 3 runs worked:
 * the others — the server limited the requests". The count is honest: runs that did not work are never folded into "best of".
 */
export function runsLine(t: T, r: RunInfo): string {
  const via = r.serverDetail ? t("node.settings.bandwidthViaDetail", { server: r.server, detail: r.serverDetail }) : t("node.settings.bandwidthVia", { server: r.server });
  const ok = Math.max(r.runs, 1);
  const failed = Math.max(r.runsTotal - ok, 0);
  if (failed === 0) return `${via}, ${t.n("node.settings.bandwidthRunsAll", ok)}`;
  const reasons = [...new Set(r.runFailures.map((c) => reasonText(t, c)))].join(", ");
  const some = t.n("node.settings.bandwidthRunsSome", ok, { total: r.runsTotal });
  return reasons ? `${via}, ${some}: ${t.n("node.settings.bandwidthRunsFailed", failed, { reasons })}` : `${via}, ${some}`;
}

/**
 * The value "Use" puts into the capacity field: the capacity (capacityOf) rounded the way a plan is written, because a test
 * is an estimate and "937" would only look more exact than it is. Under 10 as it is, under 100 to 5, under 1000 to 10, above to 50.
 * At least 1: 0 means "unknown" and would switch the percentages off.
 */
export function roundMbps(measured: number): number {
  const step = measured < 10 ? 1 : measured < 100 ? 5 : measured < 1000 ? 10 : 50;
  return Math.min(1_000_000, Math.max(1, Math.round(measured / step) * step));
}
