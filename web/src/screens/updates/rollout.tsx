import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Icon } from "@/components/ui/icons";
import { StatusDot, StatusPill, kindTextClass } from "@/components/ui/status";
import { RolloutStatus, StepState } from "@/gen/mistgate/admin/v1/update_pb";
import { useT, type T } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { isActive, progressOf, rolloutInfo, stagesOf, stepErrorText, stepInfo, type Rollout, type Stage, type Step } from "@/lib/updates";

const decided = (s: Step) => s.state === StepState.PASSED || s.state === StepState.FAILED || s.state === StepState.ROLLED_BACK || s.state === StepState.SKIPPED;

const downloadPhaseKeys: Record<string, Parameters<T>[0]> = {
  accepted: "up.download.phase.accepted",
  downloading: "up.download.phase.downloading",
  verifying: "up.download.phase.verifying",
  installing: "up.download.phase.installing",
  restarting: "up.download.phase.restarting",
  completed: "up.download.phase.completed",
  failed: "up.download.phase.failed",
};

function size(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(0)} KiB`;
  return `${(n / 1024 ** 2).toFixed(1)} MiB`;
}

/** A node whose update is under way and reports how far it got: its chip takes the whole row for the bar. */
const showsProgress = (step: Step) => !!step.downloadPhase && step.state === StepState.SENT;

function DownloadProgress({ step }: { step: Step }) {
  const t = useT();
  if (!showsProgress(step)) return null;
  const total = step.totalBytes;
  const done = Math.min(step.downloadedBytes, total || step.downloadedBytes);
  const percent = total > 0 ? Math.min(100, Math.round((done / total) * 100)) : 0;
  const downloading = step.downloadPhase === "downloading";
  return (
    <div
      className="flex min-w-48 basis-full flex-col gap-1 text-[11px] text-muted sm:ms-2 sm:flex-1 sm:basis-0"
      role="progressbar"
      aria-label={t("up.download.aria", { percent })}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={percent}
    >
      <div className="flex items-center justify-between gap-2 font-mono">
        <span>{t(downloadPhaseKeys[step.downloadPhase] ?? "up.download.phase.downloading")}</span>
        <span>{total ? `${percent}%${downloading ? ` · ${size(step.bytesPerSecond)}/s` : ""}` : t("up.download.phase.waiting")}</span>
      </div>
      <div className="h-1.5 overflow-hidden rounded-full bg-surface-3">
        <div className="h-full rounded-full bg-accent transition-[width] duration-500" style={{ width: `${percent}%` }} />
      </div>
      {total > 0 && <span className="font-mono">{size(done)} / {size(total)}</span>}
    </div>
  );
}

function stageTexts(t: T, st: Stage): { name: string; what: string } {
  if (st.kind === "skipped") return { name: t("up.ro.skipped"), what: t("up.ro.skipped.what") };
  if (st.kind === "canary") return { name: t("up.ro.canary"), what: t("up.ro.canary.what") };
  return { name: t("up.ro.batch", { n: st.stage }), what: t.n("up.ro.batch.what", st.steps.length) };
}

/** One node of a stage: its name (a link to it), a dot and the word for its step; why, when it has not gone well, goes under the stage. */
function NodeChip({ step, toVersion }: { step: Step; toVersion: string }) {
  const t = useT();
  const info = stepInfo(step.state);
  const label = step.state === StepState.PASSED ? t("up.step.to", { version: toVersion }) : t(info.key);
  return (
    <li className={cx("flex min-h-7 flex-wrap items-center gap-2 rounded-ctl border border-line bg-surface-2 px-2.5 py-1 text-xs", showsProgress(step) && "w-full")}>
      <StatusDot kind={info.kind} />
      <Link to="/nodes/$id" params={{ id: step.nodeId }} className="font-mono font-bold hover:underline">
        {step.nodeName}
      </Link>
      <span className={cx("whitespace-nowrap", info.kind === "off" ? "text-muted" : "text-fg")}>{label}</span>
      <DownloadProgress step={step} />
    </li>
  );
}

/**
 * The stages of a rollout, one row each: the stage on the left, its nodes as chips on the right. The canary is one chip,
 * a batch is a few; nothing stretches across the page. What went wrong with a node is said under its stage.
 */
function Stages({ rollout }: { rollout: Rollout }) {
  const t = useT();
  const stages = stagesOf(rollout.steps);
  const active = isActive(rollout);
  // the stage being worked on: the first one with a node in flight, else the first that is not decided yet
  const current = active ? (stages.find((s) => s.steps.some((x) => x.state === StepState.SENT || x.state === StepState.GATING)) ?? stages.find((s) => !s.steps.every(decided)))?.stage : undefined;
  return (
    <ol className="flex flex-col">
      {stages.map((st) => {
        const tx = stageTexts(t, st);
        const why = st.steps.filter((s) => s.errorKey);
        return (
          <li key={st.stage} className="grid gap-x-4 gap-y-1.5 border-t border-line py-3 first:border-t-0 first:pt-1 sm:grid-cols-[150px_minmax(0,1fr)]">
            <div className="flex min-w-0 flex-col gap-0.5">
              <b className={cx("flex items-center gap-1.5 text-[13px]", st.stage === current && "text-accent-text", st.kind === "skipped" && "text-muted")}>
                {tx.name}
                {st.stage === current && <span className="size-1.5 rounded-full bg-accent" aria-hidden />}
              </b>
              <span className="text-[11px] leading-snug text-muted">{tx.what}</span>
            </div>
            <div className="flex min-w-0 flex-col gap-2">
              <ul className="flex flex-wrap gap-1.5">
                {st.steps.map((s) => (
                  <NodeChip key={s.nodeId} step={s} toVersion={rollout.toVersion} />
                ))}
              </ul>
              {why.map((s) => (
                <p key={s.nodeId} className={cx("text-xs leading-snug text-pretty", s.state === StepState.SKIPPED ? "text-muted" : kindTextClass[stepInfo(s.state).kind])}>
                  <b className="font-mono">{s.nodeName}</b> · {stepErrorText(t, s.errorKey, s.params)}
                </p>
              ))}
            </div>
          </li>
        );
      })}
    </ol>
  );
}

/**
 * The rollout in progress, or the last one: a header with the release, the time and the state, then the stages. A finished
 * rollout folds to its header (its stages one click away) unless it ended badly; a running one is always open.
 */
export function RolloutCard({ rollout }: { rollout: Rollout }) {
  const t = useT();
  const fmt = useFmt();
  const info = rolloutInfo(rollout.status);
  const active = isActive(rollout);
  const { done, total } = progressOf(rollout);
  const [touched, setTouched] = useState<boolean | null>(null);
  const open = touched ?? (active || rollout.status === RolloutStatus.FAILED);
  const noProbe = useEdition().off("health_checks"); // the gate there has no client-eye check yet

  return (
    <section className="flex flex-col gap-2 rounded-card-lg border border-line bg-surface p-4 md:px-[18px]">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <div className="flex min-w-0 basis-full flex-col gap-0.5 sm:flex-1 sm:basis-0">
          <SectionLabel as="h2" icon="refresh" tone="mint">
            {active ? t("up.ro.title") : t("up.ro.titleLast")}
          </SectionLabel>
          <span className="font-mono text-[11px] text-muted">
            {t("up.ro.sub", { version: rollout.toVersion, ago: fmt.ago(rollout.createdUnix) })}
            {!active && total > 0 && ` · ${t.n("up.progress", total, { done })}`}
          </span>
        </div>
        <StatusPill kind={info.kind} label={t(info.key)} sm />
        {!active && (
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
      {open && (
        <>
          <Stages rollout={rollout} />
          <p className="border-t border-line pt-3 text-xs leading-normal text-pretty text-muted">
            {t("up.ro.safety")}
            {noProbe && ` ${t("up.gate.noProbe")}`}
          </p>
        </>
      )}
    </section>
  );
}
