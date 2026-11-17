import { Link } from "@tanstack/react-router";
import { useId, useState } from "react";
import { useT, type T } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { fleetPhase, installable, isFleetRun, lockNowText, phaseOpen } from "@/lib/os-fleet";
import { resultHorizonS, rolloutBlocksOthers, useOsActions } from "@/lib/os-run";
import { osPill, osRows, osSummary, osSystem, osUpdatesLine, useOsUpdates, type OsRow } from "@/lib/os-updates";
import { useIsOwner } from "@/lib/updates";
import { G } from "@/screens/users/user-bits";
import { CardHead, D, ErrorCard, Gl, NodeName, Pill, Skeleton, gk, useFlags } from "./bits";
import { OsNodeActions } from "./os-actions";
import { OsFleetDialog } from "./os-fleet-dialog";
import { OsFleetRun, OsSelectionBar } from "./os-fleet-run";
import { OsKernels } from "./os-kernels";
import { SecurityList, SecurityToggle } from "./os-security";

// Updates -> Server operating system: the packages and the kernel of the panel's own server and of every node, as the servers read
// them from disk, in the board's table: the server, its system, its kernel, what waits, the state and a chevron that opens what can be
// done (install the security updates, reboot, check for updates). The run that is going on, and its outcome, sit under the row. For
// the owner each node has a tick: several ticked nodes are updated in one run (os-fleet-dialog), which then shows as a queue above the
// table (os-fleet-run).

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
  return out;
}

type Pick = { on: boolean; onChange: (on: boolean) => void };

/** One server: the row (a button that opens what can be done) and, under it, the run that is going on or its outcome. */
function ServerRow({ row, tick, owner, countryCode, defaultOpen }: { row: OsRow; tick?: Pick; owner: boolean; countryCode?: string; defaultOpen?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const [open, setOpen] = useState(!!defaultOpen);
  const [names, setNames] = useState(false);
  const id = useId();
  const s = row.status;
  const pill = osPill(row);
  const line = s && !s.error ? osUpdatesLine(t, s) : null;
  const next = s?.kernelNewest && s.kernelNewest !== s.kernelRunning ? s.kernelNewest : "";
  const lines = notes(t, row);
  const busy = pill.key === "up.os.state.busy";
  // a row opens when there is something behind it: the owner's three actions on a node, the names of the security packages, the node's page
  const act = !!row.nodeId && (owner || !!s?.security || !!s?.kernelRunning);
  const security = s?.security ?? 0;
  const just = !busy && row.step?.state !== undefined && row.rollout !== undefined && row.rollout.finishedUnix > 0 && (row.nowUnix ?? 0) - row.rollout.finishedUnix < 60;
  return (
    <div className={cx("uq-w", open && act && "open", !act && "flat", busy && "cking", just && "ckn")}>
      <button type="button" className="uq-r" aria-expanded={open && act} aria-controls={id} onClick={() => act && setOpen(!open)}>
        {tick && <span aria-hidden className="uq-tk0" />}
        <span className="uq-n">
          {row.nodeId ? <NodeName name={row.name ?? ""} countryCode={countryCode} /> : <b>{t("up.os.panelHost")}</b>}
          <em>{row.onPanelServer ? t("up.os.onPanel") : row.online !== undefined ? t("up.nodes.online", { n: row.online }) : t("up.os.panelHost.sub")}</em>
        </span>
        <span className="uq-sys" data-l={t("up.os.col.system")} data-k="sys">
          {osSystem(s) || "—"}
        </span>
        <span className="uq-k mono" data-l={t("up.os.col.kernel")}>
          {s?.kernelRunning ? (
            <>
              <span title={s.kernelRunning}>{s.kernelRunning}</span>
              {next ? <em title={next}>→ {next}</em> : s.container ? <em className="uq-cont">{t("up.os.container")}</em> : null}
            </>
          ) : (
            <span>—</span>
          )}
        </span>
        <span className="uq-up" data-l={t("up.os.col.updates")}>
          {line ? (
            <>
              <b className={cx(line.warn && "part")}>{line.main}</b>
              {line.note && <em>{line.note}</em>}
              <em>{s && s.listsUnix > 0 ? t("up.os.lists", { ago: fmt.ago(s.listsUnix) }) : t("up.os.lists.none")}</em>
            </>
          ) : (
            <span>—</span>
          )}
        </span>
        <span className="uq-st">
          <Pill k={gk(pill.kind)}>{t(pill.key)}</Pill>
          {lines.slice(0, 2).map((n) => (
            <em key={n.text} className={cx(n.warn && "uq-lx")}>
              {n.text}
            </em>
          ))}
        </span>
        <G d={D.chev} size={16} stroke={1.75} className="uq-chev" />
      </button>
      {tick && row.nodeId && (
        <span className="uq-rtick">
          <input type="checkbox" checked={tick.on} onChange={(e) => tick.onChange(e.target.checked)} aria-label={t("osf.sel.row", { name: row.name ?? "" })} />
        </span>
      )}
      <div id={id}>
        {open && act ? (
          <>
            <div className="uq-ob">
              <SecurityToggle status={s} open={names} onToggle={() => setNames(!names)} controls={`${id}-names`} />
              {names && <SecurityList status={s} node={row.name ?? ""} id={`${id}-names`} />}
              {owner ? <OsNodeActions row={row} installLabel={security > 0 ? t("uq.os.secBtn", { n: security }) : undefined} /> : <OsNodeActions row={row} compact />}
              <OsKernels row={row} />
            </div>
            <div className="uq-oa">
              {!owner && <span>{t("uq.ro.os")}</span>}
              {row.nodeId && (
                <Link to="/nodes/$id" params={{ id: row.nodeId }} className="nd-lk">
                  {t("uq.os.openNode")} ›
                </Link>
              )}
            </div>
          </>
        ) : (
          <div className="uq-ob">
            {/* under a closed row: the run that is going on or its outcome, and a reboot the kernel table says would be refused */}
            <OsNodeActions row={row} compact />
            <OsKernels row={row} onlyBlocked />
          </div>
        )}
      </div>
    </div>
  );
}

/** The chips over the table: how many servers wait for a reboot, how many for security updates. */
function Summary({ rows }: { rows: OsRow[] }) {
  const t = useT();
  const s = osSummary(rows);
  if (s.known === 0) return null;
  const calm = s.reboot + s.security === 0;
  return (
    <>
      {calm && (
        <p className="rc-nt uq-osn">
          <Gl k="ok" />
          <span>{s.updates > 0 ? t.n("up.os.summary.updates", s.updates) : t("uq.os.calm")}</span>
        </p>
      )}
      {!calm && (
        <div className="uq-chips">
          {s.reboot > 0 && (
            <span className="uq-chip">
              <Gl k="part" />
              {t("uq.os.chip.reboot")}
              <b className="mono">{s.reboot}</b>
            </span>
          )}
          {s.security > 0 && (
            <span className="uq-chip">
              <Gl k="part" />
              {t("uq.os.chip.sec")}
              <b className="mono">{s.security}</b>
            </span>
          )}
        </div>
      )}
    </>
  );
}

/** The "Server systems" tab of the Updates page. The Cloudflare edition has no such data and shows nothing here. */
/** The dev kit shows ticks and the lists of names already set (`defaultPicked`, `defaultOpenNames`: node ids). */
export function OsSection({ defaultPicked, defaultOpenNames }: { defaultPicked?: string[]; defaultOpenNames?: string[] } = {}) {
  const t = useT();
  const { off } = useEdition();
  const owner = useIsOwner();
  const actions = useOsActions();
  const flags = useFlags();
  const lacking = off("os_updates");
  const q = useOsUpdates(!lacking);
  const [picked, setPicked] = useState<ReadonlySet<string>>(() => new Set(defaultPicked));
  const [asking, setAsking] = useState(false);
  if (lacking) return null;
  if (!q.data) return q.isError ? <ErrorCard onRetry={() => void q.refetch()} /> : <Skeleton />;
  const rows = osRows(q.data);
  const nodeRows = rows.filter((r) => !!r.nodeId);
  const run = q.data.rollout;
  const nowSec = q.data.nowUnix;
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
  const allTicked = fit.length > 0 && fit.every((r) => picked.has(r.id));
  const showRun = isFleetRun(run) && (phaseOpen(fleetPhase(run)) || nowSec - (run.finishedUnix || run.createdUnix) <= resultHorizonS);
  return (
    <>
      <Summary rows={rows} />
      {showRun && run && (
        <div className="uq-sel">
          <OsFleetRun run={run} owner={owner} actions={actions} nowSec={nowSec} />
        </div>
      )}
      {canPick && (
        <div className="uq-sel">
          <OsSelectionBar
            count={ticked.length}
            installable={fit.length}
            onSelectAll={() => setPicked(new Set(fit.map((r) => r.id)))}
            onClear={() => setPicked(new Set())}
            onStart={() => setAsking(true)}
          />
        </div>
      )}
      <section className="uq-card pp-sec" aria-label={t("uq.os.title")}>
        <CardHead icon={D.chip} tone="sky" title={t("uq.os.title")} />
        <div className={cx("uq-t uq-os", canPick && "tick")}>
          <div className="uq-r uq-h" aria-hidden>
            {canPick && <span className="uq-tk0" />}
            <span>{t("up.os.col.server")}</span>
            <span>{t("up.os.col.system")}</span>
            <span>{t("up.os.col.kernel")}</span>
            <span>{t("up.os.col.updates")}</span>
            <span>{t("up.nodes.col.state")}</span>
            <span />
          </div>
          {canPick && (
            <span className="uq-rtick uq-selall">
              <input type="checkbox" checked={allTicked} disabled={fit.length === 0} onChange={() => setPicked(allTicked ? new Set() : new Set(fit.map((r) => r.id)))} aria-label={t("osf.sel.master")} />
            </span>
          )}
          {rows.map((row) => (
            <ServerRow key={row.id} row={row} tick={tickOf(row)} owner={owner} countryCode={row.nodeId ? flags.get(row.nodeId) : undefined} defaultOpen={defaultOpenNames?.includes(row.id)} />
          ))}
        </div>
        <p className="sb-vfoot">{t("up.os.readonly")}</p>
      </section>
      {asking && ticked.length > 0 && <OsFleetDialog rows={ticked} actions={actions} onClose={() => setAsking(false)} onStarted={() => setPicked(new Set())} />}
    </>
  );
}
