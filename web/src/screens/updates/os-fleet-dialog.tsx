import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Segmented } from "@/components/ui/segmented";
import { useT, type T } from "@/i18n";
import { cx } from "@/lib/cx";
import { canaryFirst, defaultBatchSize, defaultScope, maxBatchSize, planStages, rebootOf, scopeBlock, scopeMode, type OsScope } from "@/lib/os-fleet";
import { blockText, type OsActions } from "@/lib/os-run";
import type { OsRow } from "@/lib/os-updates";
import { Fact, Heading, RebootRow } from "./os-confirm";

// The window before an OS update of several nodes, kept short: what to do (one switch and one line of words), whether to reboot
// (one row), the order as a chain of nodes with the batch size beside it, the nodes left out (one folded line) and a few facts.
// The call asks for a fresh step-up like the one for a single node (OsActions.startFleet). Only the nodes that can go now are
// sent: a node left out here would be skipped by the panel anyway, and the canary must be one that will really run.

const scopes: OsScope[] = ["security", "all", "reboot"];

/** One node of the chain: its name, "canary" on the first, and how many people are online on it, small. */
function Node({ row, canary, why, t }: { row: OsRow; canary: boolean; why: string; t: T }) {
  const n = row.online ?? 0;
  return (
    <li title={canary ? why : undefined} className={cx("flex min-h-7 items-center gap-1.5 rounded-ctl border bg-surface-2 px-2 py-0.5 text-xs", canary ? "border-accent-line" : "border-line")}>
      <b className="font-mono">{row.name}</b>
      {canary && <span className="text-[10px] font-semibold text-accent-text">({t("up.ro.canary")})</span>}
      <span aria-hidden className="flex items-center gap-0.5 text-[10px] text-muted">
        <Icon name="people" size={10} />
        {n}
      </span>
      <span className="sr-only">{n > 0 ? t("osf.online", { n }) : t("osf.online.none")}</span>
    </li>
  );
}

/** "Left out 2: sg1, msk1", folded; its reasons open with a click (open at once when no node can go and the reasons are the whole story). */
function Left({ rows, scope, openAtOnce, t }: { rows: OsRow[]; scope: OsScope; openAtOnce: boolean; t: T }) {
  const [touched, setTouched] = useState<boolean | null>(null);
  const open = touched ?? openAtOnce;
  return (
    <section className="flex flex-col gap-1">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setTouched(!open)}
        className="-mx-1 flex min-h-7 items-center gap-1.5 rounded-ctl px-1 text-start text-xs font-semibold text-muted transition-colors duration-200 hover:bg-surface-2 hover:text-fg"
      >
        <Icon name="chevronRight" size={12} className={cx("flex-none transition-transform duration-200 rtl:-scale-x-100", open && "rotate-90 rtl:rotate-90")} />
        <span className="min-w-0 truncate">{t("osf.skip.sum", { n: rows.length, names: rows.map((r) => r.name).join(", ") })}</span>
      </button>
      {open && (
        <ul className="flex flex-col gap-1 ps-4">
          {rows.map((r) => (
            <li key={r.id} className="text-xs leading-snug text-pretty text-muted">
              <b className="font-mono text-fg">{r.name}</b> · {blockText(t, scopeBlock(r, scope))}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

export function OsFleetDialog({ rows, actions, onClose, onStarted, initialScope }: { rows: OsRow[]; actions: OsActions; onClose: () => void; onStarted?: () => void; /** The dev kit opens the window on a chosen kind. */ initialScope?: OsScope }) {
  const t = useT();
  const [scope, setScope] = useState<OsScope>(() => initialScope ?? defaultScope(rows));
  const [reboot, setReboot] = useState<"if_required" | "never">("if_required");
  const [pick, setPick] = useState<number | null>(null);
  const eligible = rows.filter((r) => scopeBlock(r, scope) === "");
  const left = rows.filter((r) => scopeBlock(r, scope) !== "");
  const maxBatch = maxBatchSize(eligible.length);
  const batch = Math.min(pick ?? defaultBatchSize(), maxBatch);
  const stages = planStages(eligible, batch);
  const canary = stages[0]?.rows[0];
  const people = eligible.reduce((sum, r) => sum + (r.online ?? 0), 0);
  const reboots = scope === "reboot" || reboot === "if_required";
  const rebootOnly = scope === "reboot";
  const fits = rows.filter((r) => scopeBlock(r, scope) === "").length;

  const go = () =>
    void actions
      .startFleet({
        nodeIds: canaryFirst(eligible).map((r) => r.nodeId!),
        first: canary?.name ?? "",
        mode: scopeMode[scope],
        reboot: rebootOf(scope, reboot),
        batchSize: batch,
      })
      .then((ok) => {
        if (ok) {
          onStarted?.();
          onClose();
        }
      });

  return (
    <Modal
      open
      shot="os-fleet-confirm"
      onOpenChange={(o) => !o && !actions.busy && onClose()}
      title={t.n("osf.dlg.title", eligible.length)}
      description={t("osf.dlg.lead")}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={actions.busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          {/* a reboot cuts people off: the red button, like the other destructive windows */}
          <Button variant={rebootOnly ? "danger" : "primary"} size="md" disabled={actions.busy || eligible.length === 0} onClick={go}>
            {t.n(rebootOnly ? "osf.go.reboot" : "osf.go", eligible.length)}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <section aria-labelledby="osf-what" className="flex flex-col gap-1.5">
          <Heading id="osf-what">{t("osf.h.what")}</Heading>
          <Segmented
            variant="thumb"
            wrap
            aria-label={t("osf.dlg.what")}
            value={scope}
            onValueChange={setScope}
            options={scopes.map((s) => ({ value: s, label: t(`osf.seg.${s}`) }))}
          />
          <p className="text-xs leading-snug text-pretty text-muted">
            {t(`osf.scope.${scope}.hint`)} <b className="font-semibold text-fg">· {t("osf.scope.fits", { n: fits, total: rows.length })}</b>
          </p>
        </section>

        {!rebootOnly && <RebootRow on={reboot === "if_required"} onChange={(on) => setReboot(on ? "if_required" : "never")} />}

        {eligible.length === 0 ? (
          <Notice tone="quiet">{t("osf.skip.none")}</Notice>
        ) : (
          <section aria-labelledby="osf-order" className="flex flex-col gap-1.5">
            <div className="flex min-h-7 items-center justify-between gap-3">
              <Heading id="osf-order">{t("osf.order.title")}</Heading>
              {maxBatch > 1 && (
                <div className="flex items-center gap-2">
                  <span className="text-[11px] text-muted">{t("osf.batch.per")}</span>
                  <Segmented
                    aria-label={t("osf.batch.aria")}
                    value={String(batch)}
                    onValueChange={(v) => setPick(Number(v))}
                    options={Array.from({ length: maxBatch }, (_, i) => ({ value: String(i + 1), label: String(i + 1) }))}
                  />
                </div>
              )}
            </div>
            <ol className="flex flex-wrap items-center gap-x-1 gap-y-1.5">
              {stages.map((st, i) => (
                <li key={st.stage} className="flex items-center gap-1">
                  {i > 0 && <Icon name="chevronRight" size={12} className="flex-none text-faint rtl:-scale-x-100" aria-hidden />}
                  <ul className={cx("flex flex-wrap items-center gap-1", st.rows.length > 1 && "rounded-field border border-dashed border-line p-1")}>
                    {st.rows.map((r) => (
                      <Node key={r.id} row={r} canary={st.stage === 0} why={t("osf.order.canaryWhy", { node: r.name ?? "" })} t={t} />
                    ))}
                  </ul>
                </li>
              ))}
            </ol>
            {batch > 1 && <p className="text-[11px] leading-snug text-pretty text-muted">{t("osf.batch.hint")}</p>}
          </section>
        )}

        {left.length > 0 && <Left rows={left} scope={scope} openAtOnce={eligible.length === 0} t={t} />}

        {eligible.length > 0 && (
          <ul className="flex flex-col gap-1.5">
            <Fact icon="people" small>
              <b className="font-semibold text-fg">{t.n("osf.online.total", people)}</b>
              {people > 0 && reboots && ` ${t("os.dlg.online.drop")}`}
            </Fact>
            <Fact icon="pulse" small>
              {t("os.dlg.fact.profiles")} {t("osf.dlg.cut")}
            </Fact>
          </ul>
        )}
      </div>
    </Modal>
  );
}
