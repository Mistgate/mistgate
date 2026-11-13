import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { Pending, QueryError } from "@/components/ui/query-error";
import { StatusPill, kindTextClass } from "@/components/ui/status";
import { useT, type T } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt, type Fmt } from "@/lib/format";
import { osPill, osRows, osSummary, osSystem, osUpdatesLine, useOsUpdates, type OsRow } from "@/lib/os-updates";
import { OsNodeActions, OsRowMenu } from "./os-actions";
import { OsKernels } from "./os-kernels";

// Updates -> Server operating system: the packages and the kernel of the panel's own server and of every node, as the
// servers read them from disk. A node's row ends in the ⋯ menu that installs its security updates, reboots it or checks for
// updates; the run that is going on, and its outcome, sit under the row (os-actions).

// The same columns for the header and every row: server, system, kernel, updates, state, the ⋯ of the row.
const cols = "md:grid-cols-[minmax(120px,1fr)_110px_minmax(170px,1.1fr)_minmax(190px,1fr)_minmax(180px,1.4fr)_36px]";

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

function Updates({ row, fmt }: { row: OsRow; fmt: Fmt }) {
  const t = useT();
  const s = row.status;
  if (!s || s.error) return <span className="text-xs text-faint">—</span>;
  const line = osUpdatesLine(t, s);
  return (
    <div className="flex min-w-0 flex-col gap-0.5 text-xs">
      <span className={cx("font-bold md:whitespace-nowrap", line.warn && kindTextClass.warn)}>{line.main}</span>
      {line.note && <span className="text-[11px] text-muted">{line.note}</span>}
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
  if (s.apt === "locked") out.push({ text: t("up.os.apt.locked"), warn: true });
  if (s.apt === "half_configured") out.push({ text: t("up.os.apt.half", { names: s.halfConfigured.slice(0, 3).join(", ") }), warn: true });
  if (s.apt === "no_lists") out.push({ text: t("up.os.apt.no_lists"), warn: true });
  if (s.awgModuleNext === "missing") out.push({ text: t("up.os.awg.missing"), warn: true });
  if (s.autoUpdates === "off") out.push({ text: t("up.os.auto.off"), warn: false });
  if (s.autoUpdates === "not_installed") out.push({ text: t("up.os.auto.not_installed"), warn: false });
  if (s.held.length > 0) out.push({ text: t("up.os.held", { names: s.held.slice(0, 3).join(", ") }), warn: false });
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

/** The "Server systems" tab of the Updates page. The Cloudflare edition has no such data and shows nothing here. */
export function OsSection() {
  const t = useT();
  const fmt = useFmt();
  const { off } = useEdition();
  const lacking = off("os_updates");
  const q = useOsUpdates(!lacking);
  if (lacking) return null;
  const rows = q.data ? osRows(q.data) : [];
  const heads = [t("up.os.col.server"), t("up.os.col.system"), t("up.os.col.kernel"), t("up.os.col.updates"), t("up.nodes.col.state")];
  return (
    <section className="flex flex-col gap-3">
      {!q.data ? (
        q.isError ? <QueryError error={q.error} onRetry={() => void q.refetch()} /> : <Pending />
      ) : (
        <>
          <Summary rows={rows} />
          <div className="hidden overflow-hidden rounded-card-lg border border-line bg-surface md:block">
            <div className={cx("grid h-10 items-center gap-4 border-b border-line px-5 text-xs font-semibold text-muted", cols)}>
              {heads.map((h) => (
                <span key={h}>{h}</span>
              ))}
              <span aria-hidden />
            </div>
            {rows.map((row) => (
              <div key={row.id} className={cx("grid min-h-[60px] items-center gap-x-4 gap-y-2.5 border-b border-line px-5 py-2.5 text-[13px] last:border-b-0", cols)}>
                <Server row={row} />
                <span className="truncate text-xs text-muted">{osSystem(row.status) || "—"}</span>
                <Kernel row={row} />
                <Updates row={row} fmt={fmt} />
                <State row={row} />
                <div className="-me-2 flex justify-end">
                  <OsRowMenu row={row} />
                </div>
                {/* under the row, the full width: the run that is going on or its outcome; a row with nothing to report has no such line */}
                <div className="col-span-full empty:hidden">
                  <OsNodeActions row={row} compact />
                </div>
                {/* a reboot the kernel table says would be refused: shown open, only then */}
                <div className="col-span-full empty:hidden">
                  <OsKernels row={row} onlyBlocked />
                </div>
              </div>
            ))}
          </div>
          <div className="flex flex-col gap-2 md:hidden">
            {rows.map((row) => (
              <div key={row.id} className="flex flex-col gap-3 rounded-card border border-line bg-surface p-3.5">
                <div className="flex items-start justify-between gap-2">
                  <Server row={row} />
                  <div className="-my-1.5 -me-2 flex shrink-0 items-center gap-1">
                    <span className="text-xs text-muted">{osSystem(row.status)}</span>
                    <OsRowMenu row={row} />
                  </div>
                </div>
                <div className="grid grid-cols-2 gap-3">
                  <Kernel row={row} />
                  <Updates row={row} fmt={fmt} />
                </div>
                <State row={row} />
                <div className="empty:hidden">
                  <OsNodeActions row={row} compact />
                </div>
                <div className="empty:hidden">
                  <OsKernels row={row} onlyBlocked />
                </div>
              </div>
            ))}
          </div>
          <p className="text-xs text-pretty text-muted">{t("up.os.readonly")}</p>
        </>
      )}
    </section>
  );
}
