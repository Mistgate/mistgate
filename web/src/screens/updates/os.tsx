import { Link } from "@tanstack/react-router";
import { useId, useState, type ReactNode } from "react";
import { Pending, QueryError } from "@/components/ui/query-error";
import { StatusPill, kindTextClass } from "@/components/ui/status";
import { useT, type T } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt, type Fmt } from "@/lib/format";
import { fleetPhase, installable, isFleetRun, leftoverText, lockNowText, phaseOpen } from "@/lib/os-fleet";
import { resultHorizonS, rolloutBlocksOthers, useOsActions } from "@/lib/os-run";
import { osPill, osRows, osSummary, osSystem, osUpdatesLine, useOsUpdates, type OsRow } from "@/lib/os-updates";
import { useIsOwner } from "@/lib/updates";
import { OsNodeActions, OsRowMenu } from "./os-actions";
import { OsFleetDialog } from "./os-fleet-dialog";
import { OsFleetRun, OsSelectionBar } from "./os-fleet-run";
import { OsKernels } from "./os-kernels";
import { SecurityList, SecurityToggle } from "./os-security";

// Updates -> Server operating system: the packages and the kernel of the panel's own server and of every node, as the
// servers read them from disk. A node's row ends in the ⋯ menu that installs its updates, reboots it or checks for
// updates; the run that is going on, and its outcome, sit under the row (os-actions). For the owner each node has a tick:
// several ticked nodes are updated in one run (os-fleet-dialog), which then shows as a queue above the table (os-fleet-run).

// The same columns for the header and every row: server, system, kernel, updates, state, the ⋯ of the row; for the owner a tick in front.
const cols = "md:grid-cols-[minmax(120px,1fr)_110px_minmax(170px,1.1fr)_minmax(190px,1fr)_minmax(180px,1.4fr)_36px]";
const colsTick = "md:grid-cols-[20px_minmax(120px,1fr)_110px_minmax(170px,1.1fr)_minmax(190px,1fr)_minmax(180px,1.4fr)_36px]";

function Server({ row }: { row: OsRow }) {
  const t = useT();
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      {row.nodeId ? (
        <Link to="/nodes/$id" params={{ id: row.nodeId }} className="truncate text-sm font-bold hover:underline">
          {row.name}
        </Link>
      ) : (
        <span className="truncate text-sm font-bold">{t("up.os.panelHost")}</span>
      )}
      <span className="text-[11px] text-muted">
        {row.onPanelServer ? t("up.os.onPanel") : row.online !== undefined ? t("up.nodes.online", { n: row.online }) : t("up.os.panelHost.sub")}
      </span>
    </div>
  );
}

function Kernel({ row }: { row: OsRow }) {
  const t = useT();
  const s = row.status;
  if (!s?.kernelRunning) return <span className="text-xs text-faint">—</span>;
  const next = s.kernelNewest && s.kernelNewest !== s.kernelRunning ? s.kernelNewest : "";
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <span className="truncate font-mono text-xs font-bold" title={s.kernelRunning}>
        {s.kernelRunning}
      </span>
      {next ? (
        <span className={cx("truncate font-mono text-[11px]", kindTextClass.warn)} title={next}>
          → {next}
        </span>
      ) : s.container ? (
        <span className="text-[11px] text-muted">{t("up.os.container")}</span>
      ) : null}
    </div>
  );
}

type Names = { open: boolean; onToggle: () => void; id: string };

function Updates({ row, fmt, names }: { row: OsRow; fmt: Fmt; names: Names }) {
  const t = useT();
  const s = row.status;
  if (!s || s.error) return <span className="text-xs text-faint">—</span>;
  const line = osUpdatesLine(t, s);
  return (
    <div className="flex min-w-0 flex-col gap-0.5 text-xs">
      <span className={cx("font-bold md:whitespace-nowrap", line.warn && kindTextClass.warn)}>{line.main}</span>
      {line.note && <span className="text-[11px] text-muted">{line.note}</span>}
      <SecurityToggle status={s} open={names.open} onToggle={names.onToggle} controls={names.id} />
      <span className="text-[11px] text-muted">{s.listsUnix > 0 ? t("up.os.lists", { ago: fmt.ago(s.listsUnix) }) : t("up.os.lists.none")}</span>
    </div>
  );
}

/** The lines under the pill: why a reboot, and what on the server needs a look before anyone updates it. */
function notes(t: T, row: OsRow): { text: string; warn: boolean }[] {
  if (row.why) return [{ text: t(row.why), warn: false }];
  const s = row.status;
  if (!s) return [];
  const out: { text: string; warn: boolean }[] = [];
  if (s.rebootRequired) {
    const packages = s.rebootReasons.filter((r) => r !== "kernel");
    if (s.rebootReasons.includes("kernel") && s.kernelNewest) out.push({ text: t("up.os.reboot.kernel", { version: s.kernelNewest }), warn: false });
    else if (packages.length > 0) out.push({ text: t("up.os.reboot.packages", { names: packages.slice(0, 3).join(", ") }), warn: false });
  }
  // a lock held by a named process is an automatic update at work, not a fault: said calmly, with how long it has been going
  const lock = s.apt === "locked" ? lockNowText(t, s, row.nowUnix ?? Date.now() / 1000) : "";
  if (s.apt === "locked") out.push({ text: lock || t("up.os.apt.locked"), warn: !lock });
  if (s.apt === "half_configured") out.push({ text: t("up.os.apt.half", { names: s.halfConfigured.slice(0, 3).join(", ") }), warn: true });
  if (s.apt === "no_lists") out.push({ text: t("up.os.apt.no_lists"), warn: true });
  if (s.awgModuleNext === "missing") out.push({ text: t("up.os.awg.missing"), warn: true });
  if (s.autoUpdates === "off") out.push({ text: t("up.os.auto.off"), warn: false });
  if (s.autoUpdates === "not_installed") out.push({ text: t("up.os.auto.not_installed"), warn: false });
  if (s.held.length > 0) out.push({ text: t("up.os.held", { names: s.held.slice(0, 3).join(", ") }), warn: false });
  const leftover = leftoverText(t, s);
  if (leftover) out.push({ text: leftover, warn: false });
  return out;
}

function State({ row }: { row: OsRow }) {
  const t = useT();
  const pill = osPill(row);
  return (
    <div className="flex min-w-0 flex-col items-start gap-1">
      <StatusPill kind={pill.kind} label={t(pill.key)} sm />
      {notes(t, row).map((n) => (
        <span key={n.text} className={cx("text-[11px] leading-snug text-pretty", n.warn ? kindTextClass.warn : "text-muted")}>
          {n.text}
        </span>
      ))}
    </div>
  );
}

function Summary({ rows }: { rows: OsRow[] }) {
  const t = useT();
  const s = osSummary(rows);
  if (s.known === 0) return null;
  const parts: ReactNode[] = [];
  if (s.reboot > 0) parts.push(<span key="reboot" className={kindTextClass.warn}>{t("up.os.summary.reboot", { n: s.reboot })}</span>);
  if (s.security > 0) parts.push(<span key="security" className={kindTextClass.warn}>{t("up.os.summary.security", { n: s.security })}</span>);
  if (parts.length === 0) parts.push(<span key="ok">{s.updates > 0 ? t.n("up.os.summary.updates", s.updates) : t("up.os.summary.ok")}</span>);
  return (
    <p className="flex flex-wrap gap-x-3 gap-y-1 text-[13px] font-semibold text-muted">
      {parts}
    </p>
  );
}

function Tick({ row, on, onChange }: { row: OsRow; on: boolean; onChange: (on: boolean) => void }) {
  const t = useT();
  return <input type="checkbox" checked={on} onChange={(e) => onChange(e.target.checked)} aria-label={t("osf.sel.row", { name: row.name ?? "" })} className="size-4 flex-none accent-accent" />;
}

type Pick = { on: boolean; onChange: (on: boolean) => void };
type RowProps = { row: OsRow; fmt: Fmt; tick?: Pick; defaultOpen?: boolean };

/** A node's row of the table (the wide screen). */
function TableRow({ row, fmt, tick, defaultOpen }: RowProps) {
  const [open, setOpen] = useState(!!defaultOpen);
  const id = useId();
  return (
    <div className={cx("grid min-h-[60px] items-center gap-x-4 gap-y-2.5 border-b border-line px-5 py-2.5 text-[13px] last:border-b-0", tick ? colsTick : cols)}>
      {tick && (row.nodeId ? <Tick row={row} {...tick} /> : <span aria-hidden />)}
      <Server row={row} />
      <span className="truncate text-xs text-muted">{osSystem(row.status) || "—"}</span>
      <Kernel row={row} />
      <Updates row={row} fmt={fmt} names={{ open, onToggle: () => setOpen(!open), id }} />
      <State row={row} />
      <div className="-me-2 flex justify-end">
        <OsRowMenu row={row} />
      </div>
      {/* under the row, the full width: the names of the security packages, then the run that is going on or its outcome; a row with nothing to report has no such line */}
      <div className="col-span-full empty:hidden">{open && <SecurityList status={row.status} node={row.name ?? ""} id={id} />}</div>
      <div className="col-span-full empty:hidden">
        <OsNodeActions row={row} compact />
      </div>
      {/* a reboot the kernel table says would be refused: shown open, only then */}
      <div className="col-span-full empty:hidden">
        <OsKernels row={row} onlyBlocked />
      </div>
    </div>
  );
}

/** A node's card (the phone). */
function Card({ row, fmt, tick, defaultOpen }: RowProps) {
  const [open, setOpen] = useState(!!defaultOpen);
  const id = useId();
  return (
    <div className="flex flex-col gap-3 rounded-card border border-line bg-surface p-3.5">
      <div className="flex items-start justify-between gap-2">
        <div className="flex min-w-0 items-start gap-3">
          {tick && row.nodeId && (
            <span className="mt-0.5">
              <Tick row={row} {...tick} />
            </span>
          )}
          <Server row={row} />
        </div>
        <div className="-my-1.5 -me-2 flex shrink-0 items-center gap-1">
          <span className="text-xs text-muted">{osSystem(row.status)}</span>
          <OsRowMenu row={row} />
        </div>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <Kernel row={row} />
        <Updates row={row} fmt={fmt} names={{ open, onToggle: () => setOpen(!open), id }} />
      </div>
      <div className="empty:hidden">{open && <SecurityList status={row.status} node={row.name ?? ""} id={id} />}</div>
      <State row={row} />
      <div className="empty:hidden">
        <OsNodeActions row={row} compact />
      </div>
      <div className="empty:hidden">
        <OsKernels row={row} onlyBlocked />
      </div>
    </div>
  );
}

/** The "Server systems" tab of the Updates page. The Cloudflare edition has no such data and shows nothing here. */
/** The dev kit shows ticks and the lists of names already set (`defaultPicked`, `defaultOpenNames`: node ids). */
export function OsSection({ defaultPicked, defaultOpenNames }: { defaultPicked?: string[]; defaultOpenNames?: string[] } = {}) {
  const t = useT();
  const fmt = useFmt();
  const { off } = useEdition();
  const owner = useIsOwner();
  const actions = useOsActions();
  const lacking = off("os_updates");
  const q = useOsUpdates(!lacking);
  const [picked, setPicked] = useState<ReadonlySet<string>>(() => new Set(defaultPicked));
  const [asking, setAsking] = useState(false);
  if (lacking) return null;
  const rows = q.data ? osRows(q.data) : [];
  const nodeRows = rows.filter((r) => !!r.nodeId);
  const run = q.data?.rollout;
  const nowSec = q.data?.nowUnix ?? 0;
  // ticks only for the owner, when there is more than one node to choose from and no run holds the next one
  const canPick = owner && nodeRows.length > 1 && !rolloutBlocksOthers(run);
  const ticked = nodeRows.filter((r) => picked.has(r.id));
  const fit = installable(nodeRows);
  const toggle = (id: string) => (on: boolean) =>
    setPicked((prev) => {
      const next = new Set(prev);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });
  const tickOf = (r: OsRow): Pick | undefined => (canPick ? { on: picked.has(r.id), onChange: toggle(r.id) } : undefined);
  const heads = [t("up.os.col.server"), t("up.os.col.system"), t("up.os.col.kernel"), t("up.os.col.updates"), t("up.nodes.col.state")];
  const allTicked = fit.length > 0 && fit.every((r) => picked.has(r.id));
  const showRun = isFleetRun(run) && (phaseOpen(fleetPhase(run)) || nowSec - (run.finishedUnix || run.createdUnix) <= resultHorizonS);
  return (
    <section className="flex flex-col gap-3">
      {!q.data ? (
        q.isError ? <QueryError error={q.error} onRetry={() => void q.refetch()} /> : <Pending />
      ) : (
        <>
          <Summary rows={rows} />
          {showRun && run && <OsFleetRun run={run} owner={owner} actions={actions} nowSec={nowSec} statusOf={(id) => rows.find((r) => r.nodeId === id)?.status} />}
          {canPick && (
            <OsSelectionBar
              count={ticked.length}
              installable={fit.length}
              onSelectAll={() => setPicked(new Set(fit.map((r) => r.id)))}
              onClear={() => setPicked(new Set())}
              onStart={() => setAsking(true)}
            />
          )}
          <div className="hidden overflow-hidden rounded-card-lg border border-line bg-surface md:block">
            <div className={cx("grid h-10 items-center gap-4 border-b border-line px-5 text-xs font-semibold text-muted", canPick ? colsTick : cols)}>
              {canPick && (
                <input
                  type="checkbox"
                  checked={allTicked}
                  disabled={fit.length === 0}
                  onChange={() => setPicked(allTicked ? new Set() : new Set(fit.map((r) => r.id)))}
                  aria-label={t("osf.sel.master")}
                  className="size-4 accent-accent"
                />
              )}
              {heads.map((h) => (
                <span key={h}>{h}</span>
              ))}
              <span aria-hidden />
            </div>
            {rows.map((row) => (
              <TableRow key={row.id} row={row} fmt={fmt} tick={tickOf(row)} defaultOpen={defaultOpenNames?.includes(row.id)} />
            ))}
          </div>
          <div className="flex flex-col gap-2 md:hidden">
            {rows.map((row) => (
              <Card key={row.id} row={row} fmt={fmt} tick={tickOf(row)} defaultOpen={defaultOpenNames?.includes(row.id)} />
            ))}
          </div>
          <p className="text-xs text-pretty text-muted">{t("up.os.readonly")}</p>
          {asking && ticked.length > 0 && <OsFleetDialog rows={ticked} actions={actions} onClose={() => setAsking(false)} onStarted={() => setPicked(new Set())} />}
        </>
      )}
    </section>
  );
}
