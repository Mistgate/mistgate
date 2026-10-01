import { useQuery } from "@tanstack/react-query";
import { Fragment, useState } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Pending, QueryError } from "@/components/ui/query-error";
import { StatusDot } from "@/components/ui/status";
import { Button } from "@/components/ui/button";
import type { CheckColumn, CheckRow } from "@/gen/mistgate/admin/v1/health_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { cellLook, checksQuery, type CheckCell } from "@/lib/health";
import type { Plain } from "@/lib/plain";
import { HistoryCard, ResultLine, useRunChecks } from "@/screens/health/history";

const tint: Record<string, string> = {
  ok: "tone-ok tint",
  warn: "tone-warn tint",
  bad: "tone-bad tint",
  busy: "tone-busy tint",
  blip: "tone-blip tint",
  off: "tone-off border border-line bg-surface-2",
};

/**
 * The checks of one node as a client sees them: a row per profile with its latency and exit IP (or why it fails), and its
 * 24 h history right under the row a click opens. The node Overview shows it, and the Health matrix on a phone.
 */
export function CheckRows({ row, columns, onRun, running }: { row: Plain<CheckRow>; columns: readonly Plain<CheckColumn>[]; onRun?: () => void; running?: boolean }) {
  const t = useT();
  const [open, setOpen] = useState<string | null>(null);
  const entries = row.cells
    .map((cell, i) => ({ cell, col: columns[i] }))
    .filter((x): x is { cell: CheckCell; col: Plain<CheckColumn> } => !!x.col && x.cell.deployed);
  return (
    <div className="flex flex-col gap-2">
      {entries.map(({ cell, col }) => {
        const look = cellLook(t, cell, row.nodeStatus);
        const kind = look.kind === "none" ? "off" : look.kind;
        const on = open === col.profileId;
        return (
          <Fragment key={col.profileId}>
            <button
              type="button"
              aria-expanded={on}
              onClick={() => setOpen(on ? null : col.profileId)}
              className={cx("flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 rounded-field px-3 py-2.5 text-left text-[13px]", tint[kind], on && "outline-2 outline-offset-2 outline-accent")}
            >
              <span className="flex min-w-0 flex-[1_1_140px] items-center gap-2">
                <StatusDot kind={kind} />
                <b className="break-words">{col.profileName}</b>
              </span>
              <span className="font-mono text-xs font-bold">{look.label}</span>
              <span className="min-w-0 flex-[2_1_160px] text-[11px] leading-snug text-muted">
                <ResultLine cell={cell} />
              </span>
            </button>
            {on && <HistoryCard title={col.profileName} nested cell={cell} onRun={onRun} running={running} />}
          </Fragment>
        );
      })}
    </div>
  );
}

/** The client-side checks of one node, on its Overview. */
export function NodeChecks({ nodeId }: { nodeId: string }) {
  const t = useT();
  const fmt = useFmt();
  const run = useRunChecks(nodeId);
  const q = useQuery(checksQuery(nodeId));

  const row = q.data?.rows.find((r) => r.nodeId === nodeId);
  const deployed = (row?.cells ?? []).filter((c) => c.deployed);
  const newest = Math.max(0, ...deployed.map((c) => c.last?.atUnix ?? 0));

  return (
    <section className="flex flex-col gap-3 rounded-card-lg border border-line bg-surface p-4">
      <div className="flex flex-col gap-1">
        <div className="flex items-center justify-between gap-2">
          <SectionLabel as="h2" icon="pulse" tone="rose">
            {t("hl.pills.title")}
          </SectionLabel>
          {run.can && deployed.length > 0 && (
            <Button variant="ghost" size="xs" onClick={run.run} disabled={run.running}>
              {t("hl.checks.now")}
            </Button>
          )}
        </div>
        <span className="text-[11px] text-muted">
          {newest > 0 ? t("hl.pills.when", { ago: fmt.ago(newest) }) : q.data ? t("hl.pills.interval", { min: Math.max(1, Math.round(q.data.intervalS / 60)) }) : ""}
        </span>
      </div>
      {q.isPending && <Pending compact />}
      {q.isError && !q.data && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
      {q.data && deployed.length === 0 && <p className="rounded-field border border-dashed border-line p-3.5 text-[13px] text-muted">{t("hl.checks.noneOnNode")}</p>}
      {q.data && row && <CheckRows row={row} columns={q.data.columns} onRun={run.can ? run.run : undefined} running={run.running} />}
    </section>
  );
}
