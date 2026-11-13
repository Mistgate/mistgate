import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Callout, Notice } from "@/components/ui/notice";
import { StatusDot, StatusPill, kindTextClass, type StatusKind } from "@/components/ui/status";
import { Modal } from "@/components/ui/modal";
import { OsRebootPolicy, OsRolloutMode, OsStepState } from "@/gen/mistgate/admin/v1/update_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import {
  currentStage, fleetCounts, fleetPhase, fleetStages, lockRefusedText, phaseOpen, phaseView, retriedBefore, stepChip, stoppedBy, waitText, waitingRetry,
} from "@/lib/os-fleet";
import { keyTail, pauseCode, pauseText, stepReason, type OsActions } from "@/lib/os-run";
import type { Os, OsRun, OsStepRow } from "@/lib/os-updates";
import { vantageNote } from "@/lib/updates";

// The queue of an OS update that goes through several nodes: the stages (the canary, then the batches), the state of each node,
// why one waits ("can retry in 12 min"), and what the owner can do: pause, continue, cancel; for a stopped run, retry the node,
// skip it and go on, or close the run. The language is that of the agent rollout card (rollout.tsx): the same stage rows and chips.

const modeKey = { [OsRolloutMode.SECURITY]: "osf.mode.security", [OsRolloutMode.ALL]: "osf.mode.all", [OsRolloutMode.REBOOT_ONLY]: "osf.mode.reboot" } as const;
const rebootKey = { [OsRebootPolicy.NEVER]: "osf.rebootp.never", [OsRebootPolicy.IF_REQUIRED]: "osf.rebootp.if_required", [OsRebootPolicy.ALWAYS]: "osf.rebootp.always" } as const;

const segment: Record<StatusKind, string> = { ok: "bg-ok", warn: "bg-warn", bad: "bg-danger", busy: "bg-busy animate-pulse", off: "bg-faint", blip: "bg-faint" };

/** "Security updates · reboot if required · then 2 at a time · 12 min ago": what the run was asked to do. */
function subLine(t: T, r: OsRun, ago: string): string {
  const mode = t((modeKey as Record<number, MessageKey>)[r.mode] ?? "osf.mode.security");
  const reboot = (rebootKey as Record<number, MessageKey>)[r.reboot];
  return [mode, reboot && r.mode !== OsRolloutMode.REBOOT_ONLY ? t(reboot) : "", r.steps.length > 2 ? t("osf.batch.after", { n: Math.max(1, r.batchSize) }) : "", ago].filter(Boolean).join(" · ");
}

function Chip({ step, nowSec, t }: { step: OsStepRow; nowSec: number; t: T }) {
  const c = stepChip(t, step, nowSec);
  return (
    <li className="flex min-h-7 items-center gap-2 rounded-ctl border border-line bg-surface-2 px-2.5 py-1 text-xs">
      <StatusDot kind={c.kind} />
      <Link to="/nodes/$id" params={{ id: step.nodeId }} className="font-mono font-bold hover:underline">
        {step.nodeName}
      </Link>
      <span className={cx("whitespace-nowrap", c.kind === "off" || c.kind === "ok" ? "text-muted" : "text-fg")}>{c.label}</span>
    </li>
  );
}

/** The line under a stage for a node that waits, failed or was left alone: the reason, calm for a wait and a skip. */
function note(t: T, s: OsStepRow, status: Os | undefined, nowSec: number): { text: string; kind: StatusKind } | null {
  if (waitingRetry(s)) return { text: waitText(t, s, status, nowSec), kind: "off" };
  if (s.state !== OsStepState.FAILED && s.state !== OsStepState.SKIPPED) return null;
  if (keyTail(s.errorKey) === "cancelled") return null;
  const r = stepReason(t, s.errorKey, s.params);
  const lock = lockRefusedText(t, status, s.params.code ?? "");
  const text = [r.title, lock || r.detail].filter(Boolean).join(" ");
  return text ? { text, kind: s.state === OsStepState.FAILED ? "bad" : "off" } : null;
}

function Stages({ run, statusOf, nowSec, skipNoteOf }: { run: OsRun; statusOf: (nodeId: string) => Os | undefined; nowSec: number; /** The node the banner above already explains. */ skipNoteOf?: string }) {
  const t = useT();
  const stages = fleetStages(run);
  const phase = fleetPhase(run);
  const current = phase === "running" ? currentStage(stages) : undefined;
  return (
    <ol className="flex flex-col">
      {stages.map((st) => {
        const why = st.steps.filter((s) => s.nodeId !== skipNoteOf).map((s) => ({ s, n: note(t, s, statusOf(s.nodeId), nowSec) })).filter((x) => x.n);
        return (
          <li key={st.stage} className="grid gap-x-4 gap-y-1.5 border-t border-line py-3 first:border-t-0 first:pt-1 sm:grid-cols-[150px_minmax(0,1fr)]">
            <div className="flex min-w-0 flex-col gap-0.5">
              <b className={cx("flex items-center gap-1.5 text-[13px]", st.stage === current && "text-accent-text")}>
                {st.stage === 0 ? t("up.ro.canary") : t("up.ro.batch", { n: st.stage })}
                {st.stage === current && <span className="size-1.5 rounded-full bg-accent" aria-hidden />}
              </b>
              <span className="text-[11px] leading-snug text-muted">{stageWhat(t, st.stage, st.steps.length)}</span>
            </div>
            <div className="flex min-w-0 flex-col gap-2">
              <ul className="flex flex-wrap gap-1.5">
                {st.steps.map((s) => (
                  <Chip key={s.nodeId} step={s} nowSec={nowSec} t={t} />
                ))}
              </ul>
              {why.map(({ s, n }) => (
                <p key={s.nodeId} className={cx("text-xs leading-snug text-pretty", kindTextClass[n!.kind])}>
                  <b className="font-mono">{s.nodeName}</b> · {n!.text}
                </p>
              ))}
              {st.steps.map((s) => {
                const v = vantageNote(t, s.nodeName, s.params, s.state === OsStepState.PASSED);
                return v ? (
                  <p key={`v-${s.nodeId}`} className="text-xs leading-snug text-pretty text-muted">
                    <b className="font-mono">{s.nodeName}</b> · {v}
                  </p>
                ) : null;
              })}
            </div>
          </li>
        );
      })}
    </ol>
  );
}

const stageWhat = (t: T, stage: number, n: number) => (stage === 0 ? t("osf.stage.canary") : t.n("up.ro.batch.what", n));

/** One segment per node in the colour of its state, and the count of each kind under it. */
function Bar({ run, nowSec }: { run: OsRun; nowSec: number }) {
  const t = useT();
  const c = fleetCounts(run);
  const legend: { n: number; kind: StatusKind; key: MessageKey }[] = [
    { n: c.passed, kind: "ok", key: "osf.leg.done" },
    { n: c.working, kind: "busy", key: "osf.leg.working" },
    { n: c.queued, kind: "off", key: "osf.leg.queued" },
    { n: c.skipped, kind: "off", key: "osf.leg.skipped" },
    { n: c.failed, kind: "bad", key: "osf.leg.failed" },
  ];
  const label = t.n("osf.progress", c.total, { done: c.done });
  return (
    <div className="flex flex-col gap-2">
      <div role="progressbar" aria-valuemin={0} aria-valuemax={c.total} aria-valuenow={c.done} aria-label={label} className="flex h-2 gap-[3px]">
        {run.steps.map((s) => (
          <span key={s.nodeId} title={s.nodeName} className={cx("h-full min-w-1.5 flex-1 rounded-full transition-colors duration-300", segment[stepChip(t, s, nowSec).kind])} />
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted">
        <b className="font-mono text-[11px] font-semibold text-fg">{label}</b>
        {legend
          .filter((l) => l.n > 0)
          .map((l) => (
            <span key={l.key} className="flex items-center gap-1.5">
              <StatusDot kind={l.kind} />
              {t(l.key, { n: l.n })}
            </span>
          ))}
      </div>
    </div>
  );
}

/** The banner of a run a failed node stopped: why, what the three buttons do, and the buttons. */
function StoppedBanner({ run, step, owner, actions }: { run: OsRun; step: OsStepRow; owner: boolean; actions: OsActions }) {
  const t = useT();
  const reason = stepReason(t, step.errorKey, step.params);
  const pause = { ...step.params, reason: step.errorKey, ...run.pauseParams };
  const cause = pauseCode(run.pauseKey, pause).split(".")[0]!;
  const same = !!step.errorKey && (step.errorKey === cause || step.errorKey.endsWith(`.${cause}`));
  const retried = retriedBefore(step);
  return (
    <Callout
      kind="bad"
      alert
      action={
        owner && (
          <>
            <Button variant="secondary" size="sm" disabled={actions.busy || retried} onClick={() => void actions.resume(run.id, false)}>
              {t("osf.btn.retry")}
            </Button>
            <Button variant="primary" size="sm" disabled={actions.busy} onClick={() => void actions.resume(run.id, true)}>
              {t("osf.btn.skip")}
            </Button>
            <Button variant="ghost" size="sm" disabled={actions.busy} onClick={() => void actions.close(run.id)}>
              {t("os.btn.close")}
            </Button>
          </>
        )
      }
    >
      <div className="flex flex-col gap-1">
        <b>{pauseText(t, run.pauseKey, step.nodeName, pause)}</b>
        {reason.title && !same && <span>{reason.title}</span>}
        {reason.detail && <span>{reason.detail}</span>}
        <span className="text-muted">{t(retried ? "osf.stop.retried" : "osf.stop.hint")}</span>
      </div>
    </Callout>
  );
}

function CancelDialog({ busy, onConfirm, onClose }: { busy: boolean; onConfirm: () => void; onClose: () => void }) {
  const t = useT();
  return (
    <Modal
      open
      shot="os-fleet-cancel"
      onOpenChange={(o) => !o && !busy && onClose()}
      title={t("osf.cancel.title")}
      description={t("osf.cancel.body")}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={busy} onClick={onClose}>
            {t("osf.cancel.keep")}
          </Button>
          <Button variant="danger" size="md" disabled={busy} onClick={onConfirm}>
            {t("osf.cancel.go")}
          </Button>
        </>
      }
    />
  );
}

/**
 * The run of several nodes: a header (what, how far, the state), the bar, the banner when it waits for the owner, the stages.
 * A run that has ended folds to its header (the stages one click away) unless it ended badly; one under way is always open.
 */
export function OsFleetRun({ run, owner, actions, statusOf, nowSec, askingCancel }: { run: OsRun; owner: boolean; actions: OsActions; statusOf: (nodeId: string) => Os | undefined; nowSec: number; /** The dev kit opens the cancel window. */ askingCancel?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const phase = fleetPhase(run);
  const view = phaseView[phase];
  const live = phaseOpen(phase);
  const [touched, setTouched] = useState<boolean | null>(null);
  const [asking, setAsking] = useState(!!askingCancel);
  const open = touched ?? (live || phase === "failed");
  const culprit = phase === "stopped" ? stoppedBy(run) : undefined;
  return (
    <section data-shot="os-fleet-run" className="flex flex-col gap-3 rounded-card-lg border border-line bg-surface p-4 md:px-[18px]">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <div className="flex min-w-0 basis-full flex-col gap-0.5 sm:flex-1 sm:basis-0">
          <SectionLabel as="h2" icon="cpu" tone="mint">
            {t.n(live ? "osf.run.title" : "osf.run.titleLast", run.steps.length)}
          </SectionLabel>
          <span className="text-[11px] text-muted">{subLine(t, run, fmt.ago(run.createdUnix))}</span>
        </div>
        <StatusPill kind={view.kind} label={t(view.key)} sm />
        {owner && phase === "running" && (
          <div className="flex gap-2">
            <Button variant="secondary" size="sm" disabled={actions.busy} onClick={() => void actions.pause(run.id)}>
              {t("osf.btn.pause")}
            </Button>
            <Button variant="ghost" size="sm" disabled={actions.busy} title={t("osf.btn.cancel.hint")} onClick={() => setAsking(true)}>
              {t("osf.btn.cancel")}
            </Button>
          </div>
        )}
        {owner && phase === "paused" && (
          <div className="flex gap-2">
            <Button variant="primary" size="sm" disabled={actions.busy} onClick={() => void actions.resume(run.id)}>
              {t("osf.btn.resume")}
            </Button>
            <Button variant="ghost" size="sm" disabled={actions.busy} title={t("osf.btn.cancel.hint")} onClick={() => setAsking(true)}>
              {t("osf.btn.cancel")}
            </Button>
          </div>
        )}
        {!live && (
          <button
            type="button"
            aria-expanded={open}
            onClick={() => setTouched(!open)}
            className="-me-1 flex h-7 items-center gap-1 rounded-ctl px-2 text-xs font-bold text-muted transition-colors duration-200 hover:bg-surface-2 hover:text-fg"
          >
            {open ? t("up.ro.hide") : t("up.ro.show")}
            <Icon name="chevronRight" size={12} className={cx("block flex-none transition-transform duration-200", open ? "-rotate-90" : "rotate-90")} />
          </button>
        )}
      </div>
      <Bar run={run} nowSec={nowSec} />
      {phase === "paused" && <Notice tone="quiet">{t("osf.paused.text")}</Notice>}
      {phase === "cancelling" && <Notice tone="quiet">{t("osf.cancelling.text")}</Notice>}
      {culprit && <StoppedBanner run={run} step={culprit} owner={owner} actions={actions} />}
      {open && (
        <div className="border-t border-line pt-2">
          <Stages run={run} statusOf={statusOf} nowSec={nowSec} skipNoteOf={culprit?.nodeId} />
        </div>
      )}
      {asking && (
        <CancelDialog
          busy={actions.busy}
          onClose={() => setAsking(false)}
          onConfirm={() => void actions.close(run.id).then((ok) => ok && setAsking(false))}
        />
      )}
    </section>
  );
}

/** The bar above the table: tick nodes, select all that have something to install, then one button. */
export function OsSelectionBar({ count, installable, onSelectAll, onClear, onStart }: { count: number; installable: number; onSelectAll: () => void; onClear: () => void; onStart: () => void }) {
  const t = useT();
  return (
    <div data-shot="os-selection-bar" className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-card border border-line bg-surface px-3.5 py-2.5">
      <span className="min-w-0 flex-1 basis-28 text-[13px] text-muted">
        {count > 0 ? <b className="font-bold text-fg">{t.n("osf.sel.count", count)}</b> : installable > 0 ? t("osf.sel.hint") : t("osf.sel.none")}
      </span>
      <div className="flex flex-wrap items-center gap-2">
        {count > 0 ? (
          <Button variant="ghost" size="sm" onClick={onClear}>
            {t("osf.sel.clear")}
          </Button>
        ) : (
          installable > 0 && (
            <Button variant="secondary" size="sm" onClick={onSelectAll}>
              {t("osf.sel.all", { n: installable })}
            </Button>
          )
        )}
        {count > 0 && (
          <Button variant="primary" size="sm" onClick={onStart}>
            {t.n("osf.sel.go", count)}
          </Button>
        )}
      </div>
    </div>
  );
}
