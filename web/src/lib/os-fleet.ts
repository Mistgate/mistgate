import type { StatusKind } from "@/components/ui/status";
import { OsRebootPolicy, OsRolloutMode, OsRolloutStatus, OsStepState } from "@/gen/mistgate/admin/v1/update_pb";
import type { T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { elapsedText, keyTail, minutesUntil, blockOf, stepPill } from "./os-run";
import { minutesText } from "./format";
import type { Os, OsRow, OsRun, OsStepRow } from "./os-updates";

// The OS update of several nodes in one run (UpdateService.StartOsRollout with up to 20 node ids): what the window offers and
// how it orders the nodes, and how a running or stopped run reads (a queue by stages, the state of each node, why one waits).
// The words are "osf.*" (src/i18n/fleetui.ts) and, for what a node answered, "os.*".

/** What the window offers: the three modes of OsRolloutMode. */
export type OsScope = "security" | "all" | "reboot";

export const scopeMode: Record<OsScope, OsRolloutMode> = {
  security: OsRolloutMode.SECURITY,
  all: OsRolloutMode.ALL,
  reboot: OsRolloutMode.REBOOT_ONLY,
};

/** The reboot the run asks for: a reboot-only run always reboots; an install reboots if required or never, as the owner chose. */
export const rebootOf = (scope: OsScope, choice: "if_required" | "never"): OsRebootPolicy =>
  scope === "reboot" ? OsRebootPolicy.ALWAYS : choice === "never" ? OsRebootPolicy.NEVER : OsRebootPolicy.IF_REQUIRED;

/** Why the node cannot take part in a run of this kind now ("" = it can): the server's word for it, `os.block.<key>`. */
export function scopeBlock(row: OsRow, scope: OsScope): string {
  if (!row.nodeId) return "panel_host";
  if (scope === "reboot") return blockOf(row, "reboot");
  // blockOf already counts ordinary packages as something to install; for "security" they are not
  const b = blockOf(row, "install");
  return scope === "security" && !b && (row.status?.security ?? 0) === 0 && row.status?.apt !== "half_configured" && (row.status?.pending ?? 0) > 0 ? "nothing_to_do" : b;
}

/** The nodes that have anything to install (security or ordinary): what "Select all with something to install" ticks. */
export const installable = (rows: readonly OsRow[]) => rows.filter((r) => !!r.nodeId && scopeBlock(r, "all") === "");

/** The kind the window opens on: security updates when any ticked node has them, else all packages when any has those. */
export function defaultScope(rows: readonly OsRow[]): OsScope {
  if (rows.some((r) => scopeBlock(r, "security") === "")) return "security";
  return rows.some((r) => scopeBlock(r, "all") === "") ? "all" : "security";
}

/** The order the panel takes nodes in: fewest people online first (the canary), then by name (it also looks at profiles: not known here). */
export const canaryFirst = <R extends Pick<OsRow, "online" | "name">>(rows: readonly R[]): R[] =>
  [...rows].sort((a, b) => (a.online ?? 0) - (b.online ?? 0) || (a.name ?? "").localeCompare(b.name ?? ""));

/** Nodes after the canary that go together when the owner has no wish: the panel's default for an agent rollout. */
export const defaultBatchSize = (n: number) => Math.min(5, n < 5 ? 1 : 2);

/** The most that makes sense after the canary: all the rest, but never above the server's 5. */
export const maxBatchSize = (n: number) => Math.max(1, Math.min(5, n - 1));

export type PlanStage = { stage: number; rows: OsRow[] };

/** The stages a run would have: the canary alone, then `batch` nodes at a time. */
export function planStages(rows: readonly OsRow[], batch: number): PlanStage[] {
  const ordered = canaryFirst(rows);
  const out: PlanStage[] = [];
  ordered.forEach((row, i) => {
    const stage = i === 0 ? 0 : 1 + Math.floor((i - 1) / Math.max(1, batch));
    (out[stage] ??= { stage, rows: [] }).rows.push(row);
  });
  return out.filter(Boolean);
}

// ---------------------------------------------------------------------------------------------------
// The run as it goes

export type FleetPhase = "running" | "paused" | "stopped" | "cancelling" | "done" | "cancelled" | "failed";

/** A run of more than one node gets the queue card; one node is shown on its own row. */
export const isFleetRun = (r: OsRun | undefined): r is OsRun => !!r && r.steps.length > 1;

const failedOf = (r: OsRun) => r.steps.find((s) => s.state === OsStepState.FAILED);

/** Where the run stands: paused by the owner and stopped by a failed step are both PAUSED on the server; the failed step tells them apart. */
export function fleetPhase(r: OsRun): FleetPhase {
  switch (r.status) {
    case OsRolloutStatus.RUNNING:
      return "running";
    case OsRolloutStatus.PAUSED:
      if (keyTail(r.pauseKey) === "cancel_wait") return "cancelling";
      return failedOf(r) ? "stopped" : "paused";
    case OsRolloutStatus.CANCELLED:
      return "cancelled";
    case OsRolloutStatus.FAILED:
      return "failed";
    default:
      return "done";
  }
}

export const phaseView: Record<FleetPhase, { kind: StatusKind; key: MessageKey }> = {
  running: { kind: "busy", key: "osf.phase.running" },
  paused: { kind: "off", key: "osf.phase.paused" },
  stopped: { kind: "bad", key: "osf.phase.stopped" },
  cancelling: { kind: "busy", key: "osf.phase.cancelling" },
  done: { kind: "ok", key: "osf.phase.done" },
  cancelled: { kind: "off", key: "osf.phase.cancelled" },
  failed: { kind: "warn", key: "osf.phase.failed" },
};

export const phaseOpen = (p: FleetPhase) => p === "running" || p === "paused" || p === "stopped" || p === "cancelling";

export type FleetStage = { stage: number; steps: OsStepRow[] };

export type FleetCounts = { total: number; done: number; passed: number; working: number; queued: number; skipped: number; failed: number };

export function fleetCounts(r: OsRun): FleetCounts {
  const c = { total: r.steps.length, done: 0, passed: 0, working: 0, queued: 0, skipped: 0, failed: 0 };
  for (const s of r.steps) {
    switch (s.state) {
      case OsStepState.PASSED:
        c.passed++;
        c.done++;
        break;
      case OsStepState.SKIPPED:
        c.skipped++;
        c.done++;
        break;
      case OsStepState.FAILED:
        c.failed++;
        break;
      case OsStepState.PENDING:
        c.queued++;
        break;
      default:
        c.working++;
    }
  }
  return c;
}

/** The steps by stage, stage 0 (the canary) first. */
export function fleetStages(r: OsRun): FleetStage[] {
  const by = new Map<number, OsStepRow[]>();
  for (const s of r.steps) by.set(s.stage, [...(by.get(s.stage) ?? []), s]);
  return [...by.entries()].sort((a, b) => a[0] - b[0]).map(([stage, steps]) => ({ stage, steps }));
}

/** The stage being worked on: the first with a node under way, else the first not decided yet. */
export function currentStage(stages: readonly FleetStage[]): number | undefined {
  const open = (s: OsStepRow) => s.state === OsStepState.PENDING || s.state === OsStepState.SENT || s.state === OsStepState.REBOOTING || s.state === OsStepState.GATING;
  return (stages.find((st) => st.steps.some((s) => s.state === OsStepState.SENT || s.state === OsStepState.REBOOTING || s.state === OsStepState.GATING)) ?? stages.find((st) => st.steps.some(open)))?.stage;
}

/** The failed step that stopped the run, and whether it was retried already (the server then allows only "skip" or "close"). */
export const stoppedBy = (r: OsRun) => failedOf(r);
export const retriedBefore = (s: OsStepRow | undefined) => Number(s?.params.retry_count) > 0;

const jobPhases = ["preflight", "waiting_lock", "repairing", "refreshing", "installing", "awg_module", "checking", "rebooting"];
const waitReasons = ["busy", "apt_lock", "too_soon", "reboot_limit"];

/** The step waits to be tried again (a refusal that only said "not now"): the panel does it by itself. */
export const waitingRetry = (s: OsStepRow) => s.state === OsStepState.PENDING && s.phase === "waiting_retry";

/** The state of a node in the run as a chip: a dot and a short word. A node that waits is a quiet grey one, never red. */
export function stepChip(t: T, s: OsStepRow, nowSec: number): { kind: StatusKind; label: string } {
  switch (s.state) {
    case OsStepState.PENDING:
      if (!waitingRetry(s)) return { kind: "off", label: t("osf.step.queued") };
      return s.retryAfterUnix > nowSec
        ? { kind: "off", label: t("osf.step.waitIn", { time: minutesText(t, minutesUntil(s.retryAfterUnix, nowSec)) }) }
        : { kind: "busy", label: t("osf.step.retrying") };
    case OsStepState.SENT:
      return { kind: "busy", label: jobPhases.includes(s.phase) ? t(`osf.ph.${s.phase}` as MessageKey) : t("osf.step.sent") };
    case OsStepState.REBOOTING:
      return { kind: "busy", label: t("osf.step.rebooting") };
    case OsStepState.GATING:
      return { kind: "busy", label: t("osf.step.gating") };
    default: {
      const pill = stepPill(s);
      return { kind: pill.kind, label: t(pill.key) };
    }
  }
}

/** Why a waiting node waits, in a sentence; the holder of the package lock is named when the node's status has it. */
export function waitText(t: T, s: OsStepRow, status: Os | undefined, nowSec: number, withLock = true): string {
  const code = waitReasons.includes(s.params.retry_code ?? "") ? s.params.retry_code! : "busy";
  const reason = t(`osf.wait.${code}` as MessageKey);
  const lock = withLock && code === "apt_lock" ? lockNowText(t, status, nowSec) : "";
  const line = s.retryAfterUnix > nowSec ? t("osf.wait.line", { reason, time: minutesText(t, minutesUntil(s.retryAfterUnix, nowSec)) }) : t("osf.wait.line.now", { reason });
  return lock ? `${line} ${lock}` : line;
}

// ---------------------------------------------------------------------------------------------------
// The names of the security packages, and who holds the package lock

/** "unattended-upgr has been installing packages on the node for 14 min"; "" when the node names no holder. */
export function lockNowText(t: T, s: Pick<Os, "aptLockHolder" | "aptLockSinceUnix"> | undefined, nowSec: number): string {
  if (!s?.aptLockHolder || s.aptLockSinceUnix <= 0) return "";
  return t("osf.lock.now", { holder: s.aptLockHolder, time: elapsedText(t, s.aptLockSinceUnix, nowSec) });
}

/** What the node said about the lock when it refused a job (OsJob.apt_lock_holder / apt_lock_minutes); "" when it did not. */
export function lockRefusedText(t: T, s: Os | undefined, code: string): string {
  const job = s?.job;
  if (code !== "apt_lock" || !job?.aptLockHolder) return "";
  return t("osf.lock.refused", { holder: job.aptLockHolder, minutes: job.aptLockMinutes || 30 });
}

/** The node's note on a finished job (OsJob.note_code): only the one the panel knows, in its own words; the node's sentence is not shown. */
export const leftoverText = (t: T, s: Os | undefined) => (s?.job?.noteCode === "security_leftover_failed" ? t("osf.lock.leftover") : "");

/** The names of the security updates a node lists (at most 32), which of them are stuck, and how many more there are than names. */
export function securityNames(s: Pick<Os, "security" | "securityPending" | "securityStuck"> | undefined): { names: string[]; stuck: ReadonlySet<string>; more: number } {
  const names = s?.securityPending ?? [];
  return { names, stuck: new Set(s?.securityStuck ?? []), more: Math.max(0, (s?.security ?? 0) - names.length) };
}
