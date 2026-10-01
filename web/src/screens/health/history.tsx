import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useMemo, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { StatusDot } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { CheckStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { useT } from "@/i18n";
import { health } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { barsOf, checkError, useCan, type Bar, type CheckCell } from "@/lib/health";
import { plain } from "@/lib/plain";

const toneCls: Record<Bar["tone"], string> = {
  ok: "bg-accent",
  warn: "bg-warn",
  bad: "bg-danger",
  none: "bg-line",
};

/** The 24 h strip: 48 half-hour bars, red where the client check failed. */
export function Bars({ cell }: { cell: CheckCell }) {
  const t = useT();
  const fmt = useFmt();
  const bars = useMemo(() => barsOf(cell.history), [cell.history]);
  const failed = bars.reduce((n, b) => n + b.bucket.failed, 0);
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex h-10 items-end gap-0.5" role="img" aria-label={`${t("hl.hist.h24")} → ${t("hl.hist.now")}: ${failed} ✕`}>
        {bars.map((b, i) => (
          <span
            key={b.bucket.startUnix || i}
            title={
              b.bucket.ok + b.bucket.failed === 0
                ? t("hl.hist.barEmpty", { time: fmt.stamp(b.bucket.startUnix) })
                : t("hl.hist.bar", { time: fmt.stamp(b.bucket.startUnix), ok: b.bucket.ok, failed: b.bucket.failed, ms: b.bucket.latencyMs })
            }
            className={cx("flex-1 rounded-[2px]", toneCls[b.tone])}
            style={{ height: `${b.height}%` }}
          />
        ))}
      </div>
      <div className="flex justify-between font-mono text-[10px] text-muted">
        <span>{t("hl.hist.h24")}</span>
        <span>{t("hl.hist.h12")}</span>
        <span>{t("hl.hist.now")}</span>
      </div>
    </div>
  );
}

/** The last result of a cell as one line: exit IP and country when it works, the reason when it does not. */
export function ResultLine({ cell }: { cell: CheckCell }) {
  const t = useT();
  const last = cell.last;
  if (!last) return null;
  if (last.status === CheckStatus.OK || last.status === CheckStatus.DEGRADED) {
    const exit = [last.exitIp, last.exitCountry].filter(Boolean).join(" · ");
    return <span className="font-mono">{exit ? t("hl.hist.exit", { ip: exit }) : ""}</span>;
  }
  return <span>{checkError(t, last.errorCode)}</span>;
}

/** What a selected cell shows under the matrix and under a pill: its title, the last result, the 24 h strip, "check now". */
export function HistoryCard({
  title,
  cell,
  onRun,
  running,
  nested,
}: {
  title: ReactNode;
  cell: CheckCell;
  onRun?: () => void;
  running?: boolean;
  /** Inside another card: no border of its own. */
  nested?: boolean;
}) {
  const t = useT();
  const last = cell.last;
  const failed = last?.status === CheckStatus.FAILED;
  return (
    <div className={cx("screen-enter flex flex-col gap-3", nested ? "rounded-field bg-surface-2 p-3" : "rounded-card-lg border border-line bg-surface p-4")}>
      <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
        <b className="text-[15px]">{title}</b>
        <span className="min-w-[180px] flex-1 text-xs text-muted">{t("hl.hist.sub")}</span>
        {!failed && (
          <span className="text-xs text-muted">
            <ResultLine cell={cell} />
          </span>
        )}
      </div>
      {failed && last && (
        <div className="flex items-start gap-2 text-xs text-danger-text">
          <span className="mt-[3px] flex">
            <StatusDot kind="bad" />
          </span>
          <span className="min-w-0 flex-1">
            <b>{checkError(t, last.errorCode)}</b>
            {last.errorDetail && <span className="ml-2 font-mono text-[11px] break-words text-muted">{last.errorDetail}</span>}
            {cell.failStreak > 1 && <span className="ml-2 text-muted">{t("hl.hist.streak", { n: cell.failStreak })}</span>}
          </span>
        </div>
      )}
      <Bars cell={cell} />
      {onRun && (
        <div className="flex">
          <Button variant="secondary" size="sm" onClick={onRun} disabled={running}>
            {t("hl.checks.nowOne")}
          </Button>
        </div>
      )}
    </div>
  );
}

/** "Check now": schedule an immediate round, then look at the results after a few seconds. The server limits it to one round per profile per 30 s. */
export function useRunChecks(nodeId = "") {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const can = useCan();
  const m = useMutation({
    mutationFn: async () => plain(await health.runChecksNow({ nodeId })),
    onSuccess: (r) => {
      toast(r.scheduled > 0 ? t.n("hl.checks.nowToast", r.scheduled) : t("hl.checks.nowNone"));
      // the round takes a few seconds (a failing one up to 25 s): look twice
      for (const ms of [4000, 14000]) setTimeout(() => void qc.invalidateQueries({ queryKey: ["health", "checks"] }), ms);
    },
    onError: (e) => toast.error(ConnectError.from(e).code === Code.ResourceExhausted ? t("hl.checks.tooSoon") : errorText(e, t)),
  });
  return { run: () => m.mutate(), running: m.isPending, can: can.run };
}
