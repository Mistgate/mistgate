import { useMutation, useQueryClient } from "@tanstack/react-query";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import type { StatusKind } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { OsRolloutMode, OsRolloutStatus, OsStepState, type OsRebootPolicy } from "@/gen/mistgate/admin/v1/update_pb";
import { useT, type T } from "@/i18n";
import { en, type MessageKey } from "@/i18n/en";
import { updates } from "./api";
import { errorText } from "./errors";
import { minutesText } from "./format";
import { kernelBlock, kernelWhyKey, parseKernelBlock } from "./os-kernels";
import { osUpdatesQuery, type OsRow, type OsRun, type OsStepRow } from "./os-updates";
import { plain } from "./plain";

// The run of an OS update on one node: install the security updates and/or reboot (UpdateService.StartOsRollout), watch
// the step, close a stopped run (CancelOsRollout), refresh the package lists (RefreshOsUpdates). The data is in
// os-updates.ts (GetOsUpdates); the words are "os.*" (src/i18n/osupdates.ts).

const hasKey = (k: string): k is MessageKey => Object.hasOwn(en, k);
/** The last word of a key the server may send whole ("os.step.err.not_back") or bare ("not_back"). */
const last = (key: string) => key.slice(key.lastIndexOf(".") + 1);

/** A finished run stays on the node for a day; a stopped one (PAUSED) stays until the owner closes it. */
export const resultHorizonS = 86_400;

export const stepInFlight = (s: Pick<OsStepRow, "state">) => s.state === OsStepState.SENT || s.state === OsStepState.REBOOTING || s.state === OsStepState.GATING;
const stepOpen = (s: Pick<OsStepRow, "state">) => s.state === OsStepState.PENDING || stepInFlight(s);

/** Some OS update is running or stopped and waiting for its Close: nothing else may start. */
export const rolloutBlocksOthers = (r: OsRun | undefined) => !!r && (r.status === OsRolloutStatus.RUNNING || r.status === OsRolloutStatus.PAUSED);

export type OsNodeRun =
  | { kind: "idle" }
  /** A step is waiting or under way (the progress bar). */
  | { kind: "running"; step: OsStepRow; rollout: OsRun }
  /** A step ended; `stopped` = the run waits for the owner's Close (a banner and the button). */
  | { kind: "result"; step: OsStepRow; rollout: OsRun; stopped: boolean };

/** What to show for a node: its step while it moves, its outcome for a day (and for as long as the run is stopped), else nothing. */
export function osRunOf(row: Pick<OsRow, "step" | "rollout">, nowSec = Date.now() / 1000): OsNodeRun {
  const { step, rollout } = row;
  if (!step || !rollout || step.state === OsStepState.UNSPECIFIED) return { kind: "idle" };
  if (stepOpen(step)) return rollout.status === OsRolloutStatus.RUNNING ? { kind: "running", step, rollout } : { kind: "idle" };
  const stopped = rollout.status === OsRolloutStatus.PAUSED;
  const ended = step.finishedUnix || rollout.finishedUnix || rollout.createdUnix;
  return stopped || nowSec - ended <= resultHorizonS ? { kind: "result", step, rollout, stopped } : { kind: "idle" };
}

/** When the current phase of a step began (for "N min"). */
export function stepSince(step: OsStepRow, rollout: OsRun): number {
  switch (step.state) {
    case OsStepState.SENT:
      return step.phaseUnix || step.sentUnix || rollout.createdUnix;
    case OsStepState.REBOOTING:
      return step.rebootUnix || step.sentUnix || rollout.createdUnix;
    case OsStepState.GATING:
      return step.backUnix || step.rebootUnix || rollout.createdUnix;
    default:
      return rollout.createdUnix;
  }
}

const jobPhases = ["preflight", "waiting_lock", "repairing", "refreshing", "installing", "awg_module", "checking", "rebooting"];

/** The headline of a step under way: the node's phase, or the state when the node has not said one. */
export function phaseText(t: T, step: Pick<OsStepRow, "state" | "phase"> & { rebootUnix?: number }): string {
  switch (step.state) {
    case OsStepState.PENDING:
      return t("os.state.pending");
    case OsStepState.REBOOTING:
      return t("os.state.rebooting");
    case OsStepState.GATING:
      // a run without a reboot never "came back": its server was only checked after the job
      return t(step.rebootUnix ? "os.state.gating" : "os.state.gating.plain");
    default:
      return jobPhases.includes(step.phase) ? t(`os.phase.${step.phase}` as MessageKey) : t("os.state.sent");
  }
}

/** "under a minute" / "3 min" / "1 h 20 min" since `sinceUnix`. */
export function elapsedText(t: T, sinceUnix: number, nowSec: number): string {
  const s = Math.max(0, nowSec - sinceUnix);
  return s < 60 ? t("os.run.lessMin") : minutesText(t, Math.floor(s / 60));
}

/** Why a node's agent refused or failed a job: "os.job.err.<code>"; a code nobody knows reads as "unknown"; install_failed names its package. */
export function jobErrorText(t: T, code: string, pkg = ""): string {
  if (code === "install_failed" && pkg) return t("os.job.err.install_failed.pkg", { package: pkg });
  const key = `os.job.err.${code}`;
  return hasKey(key) ? t(key) : t("os.job.err.unknown");
}

const withJobCode = new Set(["agent_refused", "job_failed", "reboot_held"]);

/** A reboot the node refused reads differently when nothing was installed first: `params.mode` of a step or a pause is "reboot_only". */
const onlyReboot = (key: string, params: Record<string, string>) => key === "reboot_held" && params.mode === "reboot_only";

/** Why a step did not go through: a headline by its error key and, for a node's refusal or a failed job, the node's own reason below. */
export function stepReason(t: T, errorKey: string, params: Record<string, string> = {}): { title: string; detail: string } {
  const key = last(errorKey);
  if (!key) return { title: "", detail: "" };
  const stepKey = `os.step.err.${key}${onlyReboot(key, params) ? ".only" : ""}`;
  const title = hasKey(stepKey) ? t(stepKey) : t("os.step.err.unknown", { code: key });
  return { title, detail: withJobCode.has(key) ? jobErrorText(t, params.code ?? "", params.package) : "" };
}

/** The whole reason as one text ("title detail"), for an alert or an event line. */
export function reasonLine(t: T, errorKey: string, params: Record<string, string> = {}): string {
  const r = stepReason(t, errorKey, params);
  return [r.title, r.detail].filter(Boolean).join(" ");
}

/** The words of a stopped run's banner after "os.pause.": a step that lost its server, and a refused reboot with nothing installed, have their own. */
export function pauseCode(pauseKey: string, params: Record<string, string> = {}): string {
  const code = last(pauseKey);
  if (params.reason === "node_lost") return "node_lost";
  return onlyReboot(code, params) ? "reboot_held.only" : code;
}

/** The banner of a stopped run: "os.pause.<code>" with the node's name. `params` = the pause's own (reason, mode). */
export function pauseText(t: T, pauseKey: string, node: string, params: Record<string, string> = {}): string {
  const key = `os.pause.${pauseCode(pauseKey, params)}`;
  return t(hasKey(key) ? key : "os.pause.step_failed", { node });
}

/** The key of an alert's sentence (the server names the pause, the SPA knows the step's reason and mode). */
export function osAlertWhyKey(whyKey: string, params: Record<string, string>): string {
  return `${whyKey.slice(0, whyKey.lastIndexOf(".") + 1)}${pauseCode(whyKey, params)}`;
}

/** What the alert's sentence says after its own words: the node's reason alone when the sentence already names the step's outcome. */
export function osAlertReason(t: T, p: Record<string, string>): string {
  const r = stepReason(t, p.reason ?? "", { code: p.code ?? "", package: p.package ?? "", mode: p.mode ?? "" });
  return last(p.reason ?? "") === "reboot_held" ? r.detail : [r.title, r.detail].filter(Boolean).join(" ");
}

/** The line of a finished step: "Done · kernel 6.8.0-150-generic · 6 packages installed · no reboot needed" (`lead` = false drops "Done": a pill beside it says it). */
export function resultLine(t: T, step: OsStepRow, lead = true): string {
  if (step.state === OsStepState.SKIPPED) return stepReason(t, step.errorKey, step.params).title;
  if (step.state === OsStepState.FAILED) return reasonLine(t, step.errorKey, step.params);
  const p = step.params;
  const upgraded = Number(p.upgraded);
  const to = step.kernelTo || p.kernel || "";
  const kernel = to ? (step.kernelFrom && step.kernelFrom !== to ? t("os.done.kernelChange", { from: step.kernelFrom, to }) : t("os.done.kernel", { kernel: to })) : "";
  const rebootKey = `os.done.reboot.${p.reboot}`;
  const reboot = p.reboot && hasKey(rebootKey) ? t(rebootKey) : "";
  return [lead ? t("os.state.passed") : "", kernel, upgraded > 0 ? t.n("os.done.upgraded", upgraded) : "", reboot].filter(Boolean).join(" · ");
}

/** The pill of a step. */
export function stepPill(step: Pick<OsStepRow, "state">): { kind: StatusKind; key: MessageKey } {
  switch (step.state) {
    case OsStepState.PASSED:
      return { kind: "ok", key: "os.pill.passed" };
    case OsStepState.FAILED:
      return { kind: "bad", key: "os.pill.failed" };
    case OsStepState.SKIPPED:
      return { kind: "off", key: "os.pill.skipped" };
    case OsStepState.PENDING:
      return { kind: "off", key: "os.pill.pending" };
    default:
      return { kind: "busy", key: "os.pill.running" };
  }
}

// ---------------------------------------------------------------------------------------------------
// What the buttons may do

export type OsAction = "install" | "reboot" | "refresh";
type Blocks = Pick<OsRow, "updateBlock" | "rebootBlock" | "rollout" | "step"> & Partial<Pick<OsRow, "status">>;

/** Why an action is not available now ("" = it is): the server's word for the node, and any run that is not closed yet. */
export function blockOf(row: Blocks, action: OsAction): string {
  if (action === "refresh") {
    // refreshing only reads: a busy, offline or old node stops it, a run on another node does not
    return ["offline", "agent_old", "unsupported", "panel_host", "node_busy"].includes(row.updateBlock) ? row.updateBlock : row.step && stepOpen(row.step) ? "rollout_active" : "";
  }
  // a reboot the node's own kernel table says would be refused is off too, with that reason (the node refuses it all the same)
  const own = action === "reboot" ? row.rebootBlock || kernelBlock(row.status) : row.updateBlock;
  return own || (rolloutBlocksOthers(row.rollout) ? "rollout_active" : "");
}

/** The words for a block key ("os.block.<key>"); a key from a newer panel reads as sent. */
export function blockText(t: T, key: string): string {
  const kernel = parseKernelBlock(key);
  if (kernel) return t("up.os.kern.refused", { why: t(kernelWhyKey(kernel.code), { kernel: kernel.kernel }) });
  const k = `os.block.${key}`;
  return hasKey(k) ? t(k) : key;
}

/** The distinct reasons to show under the buttons (install and reboot often share one). */
export const blockKeys = (row: Blocks): string[] => [...new Set([blockOf(row, "install"), blockOf(row, "reboot")].filter(Boolean))];

/** What a refresh answered, as a sentence (a toast): started, or why not. */
export function refreshText(t: T, node: string, r: { outcome: string; code: string }): string {
  if (r.outcome === "refused") return r.code ? t("os.refresh.refusedWhy", { node, reason: jobErrorText(t, r.code) }) : t("os.refresh.refused", { node });
  const key = `os.refresh.${r.outcome}`;
  return hasKey(key) ? t(key, { node }) : t("os.refresh.refused", { node });
}

// ---------------------------------------------------------------------------------------------------
// Changes. Install/reboot and close ask for a fresh step-up (useStepUp runs its dialog and repeats the call); a cancelled
// dialog is not an error. Refreshing the lists only reads, and needs none.

export type OsStartArgs = { nodeId: string; name: string; mode: OsRolloutMode; reboot: OsRebootPolicy };
export type OsActions = ReturnType<typeof useOsActions>;

export function useOsActions() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: osUpdatesQuery.queryKey });
    void qc.invalidateQueries({ queryKey: ["nodes"] });
    void qc.invalidateQueries({ queryKey: ["health"] });
  };
  const failed = (e: unknown) => {
    if (!isStepUpCancelled(e)) toast.error(errorText(e, t));
  };

  const start = useMutation({
    mutationFn: (v: OsStartArgs) => guard(() => updates.startOsRollout({ nodeIds: [v.nodeId], mode: v.mode, reboot: v.reboot })),
    onSuccess: (_, v) => toast(t(v.mode === OsRolloutMode.REBOOT_ONLY ? "os.started.reboot" : "os.started.install", { node: v.name })),
    onSettled: invalidate,
    onError: failed,
  });
  const close = useMutation({
    mutationFn: (rolloutId: string) => guard(() => updates.cancelOsRollout({ rolloutId })),
    onSuccess: () => toast(t("os.closed")),
    onSettled: invalidate,
    onError: failed,
  });
  const refresh = useMutation({
    mutationFn: async (v: { nodeId: string; name: string }) => ({ ...v, results: plain(await updates.refreshOsUpdates({ nodeIds: [v.nodeId] })).results }),
    onSuccess: ({ name, nodeId, results }) => toast(refreshText(t, name, results.find((r) => r.nodeId === nodeId) ?? { outcome: "refused", code: "" })),
    onSettled: invalidate,
    onError: failed,
  });

  /** Each resolves true when the call went through (the dialog closes on it). */
  return {
    busy: start.isPending || close.isPending || refresh.isPending,
    refreshing: refresh.isPending,
    start: (v: OsStartArgs) => start.mutateAsync(v).then(() => true, () => false),
    close: (rolloutId: string) => close.mutateAsync(rolloutId).then(() => true, () => false),
    refresh: (v: { nodeId: string; name: string }) => refresh.mutateAsync(v).then(() => true, () => false),
  };
}
