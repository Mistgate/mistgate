import { Code, ConnectError } from "@connectrpc/connect";
import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import type { StatusKind } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { BundleStatus, NodeUpdateState, RolloutStatus, StepState, type GetUpdatesResponse } from "@/gen/mistgate/admin/v1/update_pb";
import { useT, type T } from "@/i18n";
import { en, type MessageKey } from "@/i18n/en";
import { updates } from "./api";
import { errorText } from "./errors";
import { plain, type Plain } from "./plain";
import { meQuery } from "./session";

// Everything the Updates screen reads and the small pure helpers it shares (node states, rollout stages, the wording of
// keys the server sends). The server sends keys and params, never prose (update.proto); the wording is in i18n/updates.ts.

export type Updates = Plain<GetUpdatesResponse>;
export type NodeUpdate = Updates["nodes"][number];
export type Rollout = NonNullable<Updates["rollout"]>;
export type Step = Rollout["steps"][number];
export type Bundle = NonNullable<Updates["bundle"]>;
export type LastUpdate = NonNullable<NodeUpdate["lastUpdate"]>;
type Params = Record<string, string>;

// ---------------------------------------------------------------------------------------------------
// Query: every 5 s while something is in flight, every 30 s otherwise (the page is about slow events).

export const fastPollMs = 5_000;
export const slowPollMs = 30_000;

export const isActive = (r?: { status: RolloutStatus } | null) => r?.status === RolloutStatus.RUNNING || r?.status === RolloutStatus.PAUSED;

export function updatesPollMs(d?: Updates): number {
  if (!d) return slowPollMs;
  return d.rollout?.status === RolloutStatus.RUNNING || d.nodes.some((n) => n.state === NodeUpdateState.UPDATING) ? fastPollMs : slowPollMs;
}

export const updatesQuery = queryOptions({
  queryKey: ["updates"],
  queryFn: async ({ signal }) => plain(await updates.getUpdates({}, { signal })),
  refetchInterval: (q) => updatesPollMs(q.state.data),
});

export const useUpdates = () => useQuery(updatesQuery);

/** Only the owner changes what runs on the nodes (policy.go); everyone else sees the page without the buttons. */
export function useIsOwner(): boolean {
  return useQuery(meQuery).data?.admin?.role === Role.OWNER;
}

// ---------------------------------------------------------------------------------------------------
// Node states

const nodeStates: Record<NodeUpdateState, { kind: StatusKind; key: MessageKey }> = {
  [NodeUpdateState.UNSPECIFIED]: { kind: "off", key: "up.state.offline" },
  [NodeUpdateState.UP_TO_DATE]: { kind: "ok", key: "up.state.up_to_date" },
  [NodeUpdateState.OUTDATED]: { kind: "warn", key: "up.state.outdated" },
  [NodeUpdateState.UPDATING]: { kind: "busy", key: "up.state.updating" },
  [NodeUpdateState.ROLLED_BACK]: { kind: "warn", key: "up.state.rolled_back" },
  [NodeUpdateState.FAILED]: { kind: "bad", key: "up.state.failed" },
  // the one thing the panel cannot do itself: amber, not the grey of "offline"
  [NodeUpdateState.UNSUPPORTED]: { kind: "warn", key: "up.state.unsupported" },
  [NodeUpdateState.OFFLINE]: { kind: "off", key: "up.state.offline" },
};

export const nodeStateKind = (s: NodeUpdateState): StatusKind => nodeStates[s].kind;
export const nodeStateKey = (s: NodeUpdateState): MessageKey => nodeStates[s].key;

/** A node with an older supported agent; offline nodes can still be scheduled for later. */
export const canUpdateNode = (n: Pick<NodeUpdate, "state" | "supportsUpdate" | "built">, targetBuilt: number) =>
  n.supportsUpdate && n.built < targetBuilt &&
  (n.state === NodeUpdateState.OUTDATED || n.state === NodeUpdateState.OFFLINE || n.state === NodeUpdateState.ROLLED_BACK || n.state === NodeUpdateState.FAILED);

export function updateTimezoneName(offsetMinutes: number): string {
  const sign = offsetMinutes < 0 ? "−" : "+";
  const absolute = Math.abs(offsetMinutes);
  return `UTC${sign}${String(Math.floor(absolute / 60)).padStart(2, "0")}:${String(absolute % 60).padStart(2, "0")}`;
}

/** Format a UTC timestamp as the wall-clock time in its saved fixed offset, independent of the browser timezone. */
export function updateDateTimeAtOffset(unixSeconds: number, offsetMinutes: number): string {
  const date = new Date((unixSeconds + offsetMinutes * 60) * 1000);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${date.getUTCFullYear()}-${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())} ${pad(date.getUTCHours())}:${pad(date.getUTCMinutes())}`;
}

export function updateDateTimeInputAtOffset(unixSeconds: number, offsetMinutes: number): string {
  return updateDateTimeAtOffset(unixSeconds, offsetMinutes).replace(" ", "T");
}

/** A node that ran an update and is not busy: the agent still holds the previous binary (it says so when it does not). */
export const canRollbackNode = (n: Pick<NodeUpdate, "state" | "supportsUpdate" | "lastUpdate">) =>
  n.supportsUpdate && n.lastUpdate?.outcome === "ok" && n.state !== NodeUpdateState.OFFLINE && n.state !== NodeUpdateState.UPDATING;

/** The node runs an older agent than the release: an update is waiting, failed, was rolled back or has to be done by hand. */
export const isOlder = (s: NodeUpdateState) =>
  s === NodeUpdateState.OUTDATED || s === NodeUpdateState.UNSUPPORTED || s === NodeUpdateState.ROLLED_BACK || s === NodeUpdateState.FAILED;

/** Ids of the nodes with an older agent, for the "outdated agent" mark of the Nodes screens (empty until the answer arrives). */
export function useOlderNodes(): ReadonlySet<string> {
  const data = useUpdates().data;
  return useMemo(() => new Set((data?.nodes ?? []).filter((n) => isOlder(n.state)).map((n) => n.nodeId)), [data]);
}

/** Nodes the "update all" button would take: the server's own rule for an empty node list. */
export const outdatedNodes = (nodes: readonly NodeUpdate[]) => nodes.filter((n) => n.state === NodeUpdateState.OUTDATED);

/** The node a rollout starts with: fewest people online, then fewest profiles, then name (the same order the panel uses). */
export function canaryOf<N extends Pick<NodeUpdate, "name" | "onlineUsers" | "inbounds">>(nodes: readonly N[]): N | undefined {
  return [...nodes].sort((a, b) => a.onlineUsers - b.onlineUsers || a.inbounds - b.inbounds || a.name.localeCompare(b.name))[0];
}

/** Nodes updated together after the canary: the panel's default for a batch size of 0. */
export const defaultBatch = (toUpdate: number) => (toUpdate < 5 ? 1 : 2);

// ---------------------------------------------------------------------------------------------------
// Wording of keys from the server

const hasKey = (k: string): k is MessageKey => Object.hasOwn(en, k);

/** The text of a key when the dictionary has it, else null. */
function known(t: T, key: string, params: Params = {}): string | null {
  return hasKey(key) ? t(key, params) : null;
}

/** "rolled_back_by_agent" and friends carry codes in `reason` and `gate`: turn them into sentences first. */
function withSentences(t: T, params: Params): Params {
  const out = { ...params };
  if (out.reason) out.reason = reasonText(t, out.reason);
  if (out.gate) out.gate = known(t, `updates.step.err.${out.gate}`) ?? out.gate;
  return out;
}

/**
 * What the agent (or the gate) gave as the reason of a rollback: a code from updates.last.reason.*, a step error code, or
 * free text kept as it is. "gate_<code>" is the panel's own rollback after a failed check (the agent says "manual" for
 * any rollback it is sent; the panel names who sent it), "command" a rollback the panel has no record of.
 */
export function reasonText(t: T, code: string): string {
  const c = code.replace(/^failed: ?/, "");
  const own = known(t, `updates.last.reason.${c}`);
  if (own) return own;
  if (c.startsWith("gate_")) return t("updates.last.reason.gate", { code: c.slice("gate_".length) });
  return known(t, `updates.step.err.${c}`) ?? code;
}

/** A pause's reason as the end of its sentence ("de2 did not pass the check: <this>"): the short form when there is one, so nothing is said twice. */
export function pauseReasonText(t: T, code: string): string {
  return known(t, `updates.pause.why.${code}`) ?? reasonText(t, code);
}

const last = (key: string) => key.slice(key.lastIndexOf(".") + 1);

/** Why a step failed, was rolled back or skipped. `key` arrives whole ("updates.step.err.<code>"). */
export function stepErrorText(t: T, key: string, params: Params = {}): string {
  if (!key) return "";
  return known(t, key, withSentences(t, params)) ?? t("up.err.other", { code: last(key) });
}

/** Why the rollout waits ("updates.pause.<code>"). */
export function pauseText(t: T, key: string, params: Params = {}): string {
  if (!key) return "";
  return known(t, key, params.reason ? { ...params, reason: pauseReasonText(t, params.reason) } : params) ?? last(key);
}

/** Pauses that a failing node caused (not the owner, not a changed bundle): the owner looks at that node before going on. */
export const isFailurePause = (pauseKey: string) => ["gate_failed", "step_failed", "panel_restart_ambiguous"].includes(last(pauseKey));

/** The step of the node a pause names (its params carry the name; the link to the node needs the id). */
export const pausedStep = (r: Pick<Rollout, "pauseParams" | "steps">): Step | undefined => r.steps.find((s) => s.nodeName === r.pauseParams.node);

/** What is wrong with the bundle ("updates.bundle.err.<code>"). */
export function bundleErrorText(t: T, key: string, params: Params = {}): string {
  if (!key) return "";
  return known(t, key, params) ?? last(key);
}

/** The "last update" cell: a headline, the reason under it (rollbacks and failures) and how it looks. */
export function lastUpdateView(t: T, lu: LastUpdate | undefined): { head: string; detail: string; kind: StatusKind } {
  if (!lu) return { head: t("up.last.none"), detail: "", kind: "off" };
  const to = lu.toVersion || t("up.unknownVersion");
  const reason = lu.reason ? reasonText(t, lu.reason) : "";
  switch (lu.outcome) {
    case "ok":
      return { head: lu.fromVersion ? t("up.last.ok", { from: lu.fromVersion, to }) : t("up.last.okNoFrom", { to }), detail: "", kind: "ok" };
    case "rolled_back":
      return { head: t("up.last.rolled_back", { to }), detail: reason, kind: "warn" };
    default:
      return { head: t("up.last.failed", { to }), detail: reason, kind: "bad" };
  }
}

/** A failed call as one sentence: the panel's short English messages are mapped, the rest goes through errorText. */
const preconditions: Record<string, MessageKey> = {
  "no trusted bundle": "up.e.noBundle",
  "no release key in this build": "up.e.noKey",
  "a rollout is already active": "up.e.active",
  "no node to update": "up.e.noNode",
  "rollout is not active": "up.e.notActive",
  "rollout is not paused": "up.e.notPaused",
  "node cannot update itself": "up.e.cantUpdate",
  "node is offline": "up.e.offline",
  "the bundle differs from this rollout: cancel it and start again": "up.e.bundleDiffers",
  "a command is already running on this node": "up.e.commandBusy",
};

export function callErrorText(e: unknown, t: T): string {
  const c = ConnectError.from(e);
  if (c.code === Code.FailedPrecondition) {
    const key = preconditions[c.rawMessage];
    if (key) return t(key);
    const refused = /^agent refused: (.+)$/.exec(c.rawMessage);
    if (refused) return known(t, `up.e.refused.${refused[1]}`) ?? t("up.e.refused.other", { code: refused[1]! });
  }
  return errorText(e, t);
}

// ---------------------------------------------------------------------------------------------------
// The hero: one sentence about what the owner can do now

export type Hero =
  | { id: "running" | "paused"; rollout: Rollout }
  | { id: "none" }
  | { id: "noBundle"; older: number }
  | { id: "untrusted" }
  | { id: "noKey" }
  | { id: "available"; outdated: number }
  | { id: "attention"; retry: number }
  | { id: "manual"; nodes: NodeUpdate[] }
  | { id: "current" };

/** Nodes whose agent cannot update itself and is older than the release: the owner updates them by hand, once. */
export const manualNodes = (nodes: readonly NodeUpdate[]) => nodes.filter((n) => n.state === NodeUpdateState.UNSUPPORTED);

export function heroOf(d: Updates): Hero {
  const ro = d.rollout;
  if (d.nodes.length === 0) return { id: "none" };
  if (ro && isActive(ro)) return { id: ro.status === RolloutStatus.RUNNING ? "running" : "paused", rollout: ro };
  const outdated = outdatedNodes(d.nodes).length;
  if (outdated === 0) {
    // a node that rolled back or failed still runs the old build: the fleet is not current
    const retry = d.nodes.filter((n) => n.state === NodeUpdateState.ROLLED_BACK || n.state === NodeUpdateState.FAILED).length;
    if (retry > 0) return { id: "attention", retry };
    // "all up to date" only when no node runs an older agent at all
    const manual = manualNodes(d.nodes);
    return manual.length > 0 ? { id: "manual", nodes: manual } : { id: "current" };
  }
  switch (d.bundle?.status) {
    case BundleStatus.TRUSTED:
      return { id: "available", outdated };
    case BundleStatus.UNTRUSTED:
      return { id: "untrusted" };
    case BundleStatus.NO_KEY:
      return { id: "noKey" };
    default:
      return { id: "noBundle", older: outdated };
  }
}

export type ManualCommands = {
  /** Run on the panel's server: puts the release binary on the node. Null when the panel holds no file for its architecture. */
  copy: string | null;
  /** Run anywhere with SSH to the node: installs it (the binary, the unit, a restart). */
  install: string;
  /** The binary's file name; the architecture was guessed ("amd64") when the node did not report one. */
  file: string;
  guessedArch: boolean;
};

/**
 * The manual update of a node whose agent cannot update itself, as two commands: copy the release binary from the panel's
 * dist folder to the node, then run its "install" (which replaces the installed binary and the unit, and restarts it).
 */
export function manualCommands(d: Pick<Updates, "distDir" | "bundle">, n: Pick<NodeUpdate, "address" | "arch">): ManualCommands {
  const arch = n.arch || "amd64";
  const file = d.bundle?.files.find((f) => f.os === "linux" && f.arch === arch)?.name;
  const host = n.address.includes(":") ? `[${n.address}]` : n.address; // an IPv6 literal in scp needs brackets
  return {
    copy: file ? `scp ${d.distDir || "<data-dir>/dist"}/${file} root@${host}:/root/mistgate-node` : null,
    install: `ssh root@${n.address} 'chmod +x /root/mistgate-node && /root/mistgate-node install'`,
    file: file ?? `mistgate-node-linux-${arch}`,
    guessedArch: !n.arch,
  };
}

// ---------------------------------------------------------------------------------------------------
// Rollout: stages and progress

export type Stage = { stage: number; kind: "canary" | "batch" | "skipped"; steps: Step[] };

const skipCodes = new Set(["offline", "unsupported", "up_to_date"]);

/** Steps grouped by stage in order; a stage of nodes that were passed over (offline, cannot update, current) is told apart. */
export function stagesOf(steps: readonly Step[]): Stage[] {
  const by = new Map<number, Step[]>();
  for (const s of steps) by.set(s.stage, [...(by.get(s.stage) ?? []), s]);
  return [...by.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([stage, list]) => {
      const passedOver = list.every((s) => s.state === StepState.SKIPPED && skipCodes.has(last(s.errorKey)));
      return { stage, kind: passedOver ? "skipped" : stage === 0 ? "canary" : "batch", steps: list };
    });
}

/** How many of the nodes this rollout goes through are updated. */
export function progressOf(r: Rollout): { done: number; total: number } {
  const real = r.steps.filter((s) => s.state !== StepState.SKIPPED);
  return { done: real.filter((s) => s.state === StepState.PASSED).length, total: real.length };
}

const stepStates: Record<StepState, { kind: StatusKind; key: MessageKey; pct: number }> = {
  [StepState.UNSPECIFIED]: { kind: "off", key: "up.step.pending", pct: 0 },
  [StepState.PENDING]: { kind: "off", key: "up.step.pending", pct: 0 },
  [StepState.SENT]: { kind: "busy", key: "up.step.sent", pct: 35 },
  [StepState.GATING]: { kind: "busy", key: "up.step.gating", pct: 70 },
  [StepState.PASSED]: { kind: "ok", key: "up.step.passed", pct: 100 },
  [StepState.FAILED]: { kind: "bad", key: "up.step.failed", pct: 100 },
  [StepState.ROLLED_BACK]: { kind: "warn", key: "up.step.rolled_back", pct: 100 },
  [StepState.SKIPPED]: { kind: "off", key: "up.step.skipped", pct: 0 },
};
export const stepInfo = (s: StepState) => stepStates[s];

const rolloutStates: Record<RolloutStatus, { kind: StatusKind; key: MessageKey }> = {
  [RolloutStatus.UNSPECIFIED]: { kind: "off", key: "up.ro.status.cancelled" },
  [RolloutStatus.RUNNING]: { kind: "busy", key: "up.ro.status.running" },
  [RolloutStatus.PAUSED]: { kind: "warn", key: "up.ro.status.paused" },
  [RolloutStatus.DONE]: { kind: "ok", key: "up.ro.status.done" },
  [RolloutStatus.CANCELLED]: { kind: "off", key: "up.ro.status.cancelled" },
  [RolloutStatus.FAILED]: { kind: "bad", key: "up.ro.status.failed" },
};
export const rolloutInfo = (s: RolloutStatus) => rolloutStates[s];

// ---------------------------------------------------------------------------------------------------
// Changes. Each asks for a fresh step-up (useStepUp runs the dialog and repeats the call); a cancelled dialog is not an error.

export type UpdateActions = ReturnType<typeof useUpdateActions>;

export function useUpdateActions() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();

  const call = useMutation({
    mutationFn: async (run: () => Promise<unknown>) => guard(run),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: updatesQuery.queryKey });
      void qc.invalidateQueries({ queryKey: ["nodes"] });
      void qc.invalidateQueries({ queryKey: ["health"] });
    },
    onError: (e) => {
      if (!isStepUpCancelled(e)) toast.error(callErrorText(e, t));
    },
  });

  const checkPanel = useMutation({
    mutationFn: () => updates.checkPanelUpdate({}),
    onSuccess: () => toast(t("up.panel.checked")),
    onSettled: () => void qc.invalidateQueries({ queryKey: updatesQuery.queryKey }),
    onError: (e) => toast.error(callErrorText(e, t)),
  });

  /** Runs one call; resolves true when it went through (the dialogs close on it). */
  const run = (fn: () => Promise<unknown>, done?: string) =>
    call.mutateAsync(fn).then(
      () => {
        if (done) toast(done);
        return true;
      },
      () => false,
    );

  return {
    busy: call.isPending || checkPanel.isPending,
    start: (nodeIds: string[]) => run(() => updates.startRollout({ nodeIds, batchSize: 0 }), t("up.start.started")),
    scheduleNode: (input: { nodeId: string; localDatetime: string; timezoneOffsetMinutes: number; expectedVersion: string; expectedBuilt: number }) =>
      run(() => updates.scheduleNodeUpdate({
        nodeId: input.nodeId,
        localDatetime: input.localDatetime,
        timezoneOffsetMinutes: input.timezoneOffsetMinutes,
        expectedVersion: input.expectedVersion,
        expectedBuilt: BigInt(input.expectedBuilt),
      }), t("up.schedule.saved")),
    cancelNodeSchedule: (nodeId: string) => run(() => updates.cancelNodeUpdateSchedule({ nodeId }), t("up.schedule.cancelled")),
    setUpdateTimezone: (timezoneOffsetMinutes: number) => run(() => updates.setUpdateTimezone({ timezoneOffsetMinutes }), t("up.timezone.saved")),
    pause: (id: string) => run(() => updates.pauseRollout({ rolloutId: id }), t("up.toast.paused")),
    resume: (id: string) => run(() => updates.resumeRollout({ rolloutId: id }), t("up.toast.resumed")),
    cancel: (id: string) => run(() => updates.cancelRollout({ rolloutId: id }), t("up.toast.cancelled")),
    rollback: (n: { id: string; name: string }) => run(() => updates.rollbackNode({ nodeId: n.id }), t("up.toast.rollback", { name: n.name })),
    rescan: () => run(() => updates.rescanBundle({}), t("up.bundle.rescanned")),
    checkPanel: () => checkPanel.mutateAsync().then(() => true, () => false),
    installPanel: () => run(() => updates.installPanelUpdate({}), t("up.panel.installStarted")),
  };
}
