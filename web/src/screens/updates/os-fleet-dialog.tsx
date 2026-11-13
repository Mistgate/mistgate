import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { useT, type T } from "@/i18n";
import { canaryFirst, defaultBatchSize, defaultScope, maxBatchSize, planStages, rebootOf, scopeBlock, scopeMode, type OsScope } from "@/lib/os-fleet";
import { blockText, type OsActions } from "@/lib/os-run";
import type { OsRow } from "@/lib/os-updates";
import { Choice, Fact } from "./os-confirm";

// The window before an OS update of several nodes: what will be done, the order (the canary named), how many at a time, who is
// online on each node, and which of the ticked nodes are left out for now and why. The call asks for a fresh step-up like the
// one for a single node (OsActions.startFleet). Only the nodes that can go now are sent: a node left out here would be skipped
// by the panel anyway, and the canary must be one that will really run.

const scopes: OsScope[] = ["security", "all", "reboot"];

/** "5 online" / "nobody online". */
const onlineText = (t: T, n: number) => (n > 0 ? t("osf.online", { n }) : t("osf.online.none"));

function NodeChip({ row, t }: { row: OsRow; t: T }) {
  return (
    <li className="flex min-h-7 items-center gap-2 rounded-ctl border border-line bg-surface-2 px-2.5 py-1 text-xs">
      <b className="font-mono">{row.name}</b>
      <span className="text-muted">{onlineText(t, row.online ?? 0)}</span>
    </li>
  );
}

export function OsFleetDialog({ rows, actions, onClose, onStarted, initialScope }: { rows: OsRow[]; actions: OsActions; onClose: () => void; onStarted?: () => void; /** The dev kit opens the window on a chosen kind. */ initialScope?: OsScope }) {
  const t = useT();
  const [scope, setScope] = useState<OsScope>(() => initialScope ?? defaultScope(rows));
  const [reboot, setReboot] = useState<"if_required" | "never">("if_required");
  const [pick, setPick] = useState<number | null>(null);
  const eligible = rows.filter((r) => scopeBlock(r, scope) === "");
  const left = rows.filter((r) => scopeBlock(r, scope) !== "");
  const batch = Math.min(pick ?? defaultBatchSize(eligible.length), maxBatchSize(eligible.length));
  const stages = planStages(eligible, batch);
  const canary = stages[0]?.rows[0];
  const people = eligible.reduce((sum, r) => sum + (r.online ?? 0), 0);
  const reboots = scope === "reboot" || reboot === "if_required";
  const rebootOnly = scope === "reboot";
  const fits = (s: OsScope) => rows.filter((r) => scopeBlock(r, s) === "").length;

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
      <RadioGroup value={scope} onValueChange={(v) => setScope(v as OsScope)} aria-label={t("osf.dlg.what")} className="flex flex-col gap-2">
        {scopes.map((s) => (
          <Choice key={s} value={s} label={t(`osf.scope.${s}`)} hint={t(`osf.scope.${s}.hint`)} aside={t("osf.scope.fits", { n: fits(s), total: rows.length })} />
        ))}
      </RadioGroup>

      {!rebootOnly && (
        <RadioGroup value={reboot} onValueChange={(v) => setReboot(v as typeof reboot)} aria-label={t("os.dlg.after")} className="flex flex-col gap-2">
          <Choice value="if_required" label={t("os.dlg.reboot.ifRequired")} hint={t("os.dlg.reboot.ifRequired.hint")} />
          <Choice value="never" label={t("os.dlg.reboot.never")} hint={t("os.dlg.reboot.never.hint")} />
        </RadioGroup>
      )}

      {eligible.length === 0 ? (
        <Notice tone="quiet">{t("osf.skip.none")}</Notice>
      ) : (
        <section aria-labelledby="osf-order" className="flex flex-col gap-2.5">
          <h3 id="osf-order" className="text-[11px] font-bold tracking-wide text-muted uppercase">
            {t("osf.order.title")}
          </h3>
          <ol className="flex flex-col">
            {stages.map((st) => (
              <li key={st.stage} className="grid gap-x-3 gap-y-1.5 border-t border-line py-2.5 first:border-t-0 first:pt-0 sm:grid-cols-[132px_minmax(0,1fr)]">
                <div className="flex min-w-0 flex-col gap-0.5">
                  <b className="text-[13px]">{st.stage === 0 ? t("up.ro.canary") : t("up.ro.batch", { n: st.stage })}</b>
                  <span className="text-[11px] leading-snug text-muted">{st.stage === 0 ? t("osf.order.canaryWhy", { node: canary?.name ?? "" }) : t.n("up.ro.batch.what", st.rows.length)}</span>
                </div>
                <ul className="flex flex-wrap content-start gap-1.5">
                  {st.rows.map((r) => (
                    <NodeChip key={r.id} row={r} t={t} />
                  ))}
                </ul>
              </li>
            ))}
          </ol>

          {eligible.length > 2 && (
            <div className="flex flex-col gap-1.5 rounded-field border border-line bg-surface-2 p-3">
              <span id="osf-batch" className="text-[13px] font-bold">
                {t("osf.batch.title")}
              </span>
              <RadioGroup value={String(batch)} onValueChange={(v) => setPick(Number(v))} aria-labelledby="osf-batch" className="flex flex-wrap gap-1.5">
                {Array.from({ length: maxBatchSize(eligible.length) }, (_, i) => i + 1).map((n) => (
                  <Radio.Root
                    key={n}
                    value={String(n)}
                    aria-label={t("osf.batch.aria") + " " + n}
                    className="grid size-9 cursor-pointer place-items-center rounded-ctl border border-line bg-surface text-sm font-bold transition-colors duration-200 data-checked:border-accent-line data-checked:bg-accent-soft data-checked:text-accent-text"
                  >
                    {n}
                  </Radio.Root>
                ))}
              </RadioGroup>
              <span className="text-xs leading-snug text-pretty text-muted">{t("osf.batch.hint")}</span>
            </div>
          )}
        </section>
      )}

      {left.length > 0 && eligible.length > 0 && (
        <section aria-labelledby="osf-left" className="flex flex-col gap-1.5">
          <h3 id="osf-left" className="text-[11px] font-bold tracking-wide text-muted uppercase">
            {t("osf.skip.title")}
          </h3>
          <ul className="flex flex-col gap-1">
            {left.map((r) => (
              <li key={r.id} className="flex items-start gap-2 text-xs leading-snug text-pretty text-muted">
                <Icon name="minus" size={12} className="mt-0.5 flex-none" aria-hidden />
                <span>
                  <b className="font-mono text-fg">{r.name}</b> · {blockText(t, scopeBlock(r, scope))}
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}
      {left.length > 0 && eligible.length === 0 && (
        <ul className="flex flex-col gap-1">
          {left.map((r) => (
            <li key={r.id} className="text-xs leading-snug text-pretty text-muted">
              <b className="font-mono text-fg">{r.name}</b> · {blockText(t, scopeBlock(r, scope))}
            </li>
          ))}
        </ul>
      )}

      {eligible.length > 0 && (
        <ul className="flex flex-col gap-2">
          <Fact icon="people">
            {t.n("osf.online.total", people)}
            {people > 0 && reboots && <span className="block text-xs text-muted">{t("os.dlg.online.drop")}</span>}
          </Fact>
          <Fact icon="pulse">{t("os.dlg.fact.profiles")}</Fact>
          <Fact icon="clock">{t("osf.dlg.cut")}</Fact>
        </ul>
      )}
    </Modal>
  );
}
