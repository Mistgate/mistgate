import { Scramble } from "@/components/fx/scramble-react";
import { useQuery } from "@tanstack/react-query";
import { Fragment, useEffect, useState, type CSSProperties } from "react";
import { EditionNotice } from "@/components/edition-notice";
import { ProtocolTag } from "@/components/ui/protocol-tag";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Button } from "@/components/ui/button";
import type { CheckColumn } from "@/gen/mistgate/admin/v1/health_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { cellLook, checksQuery, countryColumn, type CheckCell } from "@/lib/health";
import type { Plain } from "@/lib/plain";
import { CountryRow } from "@/screens/health/country";
import { nodeTicks } from "@/screens/overview-checks";
import { CardHead } from "@/screens/overview-parts";
import { Spin, transportOf } from "./parts";
import { HistoryCard, ResultLine, SlowMark, useRunChecks } from "@/screens/health/history";

/** The client-side checks of one node, on its Overview (the board's card): a row per profile with its day of hourly cells and its time. */
export function NodeChecks({ nodeId, inbounds }: { nodeId: string; inbounds?: readonly { id: string; port: number; protocol: string }[] }) {
  const t = useT();
  const fmt = useFmt();
  const [from, setFrom] = useState<number | null>(null); // the newest cell at the press: null while no round is waited for
  // a round that was scheduled starts the wait for a newer cell; the cell that is the newest now is the one to beat
  const run = useRunChecks(nodeId, (scheduled) => scheduled > 0 && setFrom((f) => f ?? newest));
  const noChecks = useEdition().off("health_checks"); // no rounds there: no "every 5 min"
  const q = useQuery(checksQuery(nodeId));
  const [open, setOpen] = useState<string | null>(null);

  const row = q.data?.rows.find((r) => r.nodeId === nodeId);
  const entries = (row?.cells ?? []).map((cell, i) => ({ cell, col: q.data?.columns[i] })).filter((x): x is { cell: CheckCell; col: Plain<CheckColumn> } => !!x.col && x.cell.deployed);
  const newest = Math.max(0, ...entries.map((e) => e.cell.last?.atUnix ?? 0));
  const country = countryColumn(q.data?.countryChecks);
  // The asked round answers at once ("scheduled") and the cells come a few seconds later (the panel looks again at 4 s and 14 s): the rows are being
  // looked at from the press until a newer cell is there (or 16 s have passed), and then for two seconds the strips slide one cell along with the new one
  // jumping out and the times come up (the board's nd-pr.now, nd-scan, nd-dots, then nd-adv, nd-new, nd-fresh).
  const [came, setCame] = useState(false);
  useEffect(() => {
    if (from === null) return;
    const give = setTimeout(() => setFrom(null), 16_000);
    const got =
      newest > from
        ? setTimeout(() => {
            setFrom(null);
            setCame(true);
          }, 0)
        : undefined;
    return () => {
      clearTimeout(give);
      clearTimeout(got);
    };
  }, [from, newest]);
  useEffect(() => {
    if (!came) return;
    const off = setTimeout(() => setCame(false), 2000);
    return () => clearTimeout(off);
  }, [came]);
  const busy = run.running || from !== null;

  return (
    <section className="rounded-card border border-line bg-surface">
      <CardHead title={t("hl.pills.title")}>
        <span className={cx("text-[13px] text-muted", run.running && "text-tone-sky")}>
          {run.running ? t("np.checks.running") : newest > 0 ? t("hl.pills.when", { ago: fmt.ago(newest) }) : q.data && !noChecks ? t("hl.pills.interval", { min: Math.max(1, Math.round(q.data.intervalS / 60)) }) : ""}
        </span>
        {run.can && entries.length > 0 && (
          <Button variant="secondary" size="md" onClick={run.run} disabled={run.running} className={cx(run.running && "pointer-events-none")}>
            {run.running && <Spin />}
            {run.running ? t("np.checks.checking") : t("hl.checks.now")}
          </Button>
        )}
      </CardHead>
      <i aria-hidden className={cx("nd-pg", run.running && "on")} style={{ "--w": run.running ? "92%" : "0%", transitionDuration: run.running ? "9s, .15s" : undefined } as CSSProperties} />
      <div className="px-6 max-[720px]:px-4">
        <EditionNotice feature="health_checks" />
      </div>
      {q.isPending && (
        <div className="border-t border-line px-6 py-3 max-[720px]:px-4">
          <Pending compact />
        </div>
      )}
      {q.isError && !q.data && (
        <div className="border-t border-line px-6 py-3 max-[720px]:px-4">
          <QueryError compact error={q.error} onRetry={() => void q.refetch()} />
        </div>
      )}
      {q.data && entries.length === 0 && <p className="border-t border-line px-6 py-4 text-[13px] text-muted max-[720px]:px-4">{t("hl.checks.noneOnNode")}</p>}
      {entries.map(({ cell, col }) => {
        const look = cellLook(t, fmt, cell, row!.nodeStatus);
        const kind = look.kind === "none" ? "off" : look.kind;
        const on = open === col.profileId;
        const inbound = inbounds?.find((i) => i.id === cell.inboundId);
        const ticks = nodeTicks({ cells: [cell] });
        return (
          <Fragment key={col.profileId}>
            <div className={cx("nd-pr grid grid-cols-[28px_minmax(0,200px)_minmax(0,1fr)_96px] items-center gap-3 border-t border-line px-6 py-2.5 max-[720px]:grid-cols-[28px_minmax(0,1fr)_auto] max-[720px]:px-4", busy && "now")}>
              <ProtocolTag protocol={col.protocol} compact className="nd-pf w-7 justify-center px-0" />
              <button
                type="button"
                aria-expanded={on}
                aria-label={t("hl.cell.aria", { node: row!.nodeName, profile: col.profileName, state: look.state })}
                onClick={() => setOpen(on ? null : col.profileId)}
                className="flex min-w-0 flex-col text-start"
              >
                <b className="truncate font-medium">{col.profileName}</b>
                {inbound && <span className="font-mono text-[11px] font-normal text-muted">{`${transportOf(inbound.protocol)}/${inbound.port}`}</span>}
              </button>
              <span role="img" aria-label={t("ov.tick.aria")} className={cx("nd-tk max-[720px]:order-3 max-[720px]:col-span-full", busy && "scan", came && "adv")}>
                {ticks.map((k, i) => (
                  <i
                    key={i}
                    className={cx(k.state !== "ok" && k.state, came && i === ticks.length - 1 && "new") || undefined}
                    data-t={k.from ? `${fmt.clock(k.from)}–${fmt.clock(k.to)}` : t("ov.tick.none")}
                    data-s={k.state === "none" ? (k.from ? t("ov.tick.none") : "") : k.state === "ok" ? t("ov.tick.ok", { ms: k.ms || "–" }) : k.state === "warn" ? t("ov.tick.part", { failed: k.failed, total: k.ok + k.failed }) : t("ov.tick.fail")}
                  />
                ))}
              </span>
              <span className={cx("nd-ms text-end font-mono text-[13px] font-medium whitespace-nowrap tabular-nums", kind === "warn" && "text-warn-text warn", kind === "bad" && "text-danger-text crit", busy && "dots", came && "fresh")}>{!busy && look.label && <Scramble value={look.label} once />}</span>
            </div>
            {on && (
              <div className="flex flex-col gap-2 border-t border-line px-6 py-3 max-[720px]:px-4">
                <span className="flex min-w-0 flex-wrap items-center gap-x-2.5 text-[11px] leading-snug text-muted">
                  {look.slow && <SlowMark text={t("hl.cell.connect", { time: look.slow })} className="text-[11px]" />}
                  <ResultLine cell={cell} />
                </span>
                <HistoryCard title={col.profileName} nested cell={cell} onRun={run.can ? run.run : undefined} running={run.running} />
              </div>
            )}
          </Fragment>
        );
      })}
      {country && (
        <div className="border-t border-line px-6 py-3 max-[720px]:px-4">
          <CountryRow check={country.byNode.get(nodeId)} code={country.code} />
        </div>
      )}
    </section>
  );
}
