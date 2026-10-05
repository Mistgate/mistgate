import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { BundleStatus, NodeUpdateState, RolloutStatus, StepState } from "@/gen/mistgate/admin/v1/update_pb";
import { fill, pickForm, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import {
  callErrorText,
  canRollbackNode,
  canUpdateNode,
  canaryOf,
  defaultBatch,
  fastPollMs,
  heroOf,
  isFailurePause,
  lastUpdateView,
  manualCommands,
  nodeStateKind,
  pausedStep,
  pauseText,
  progressOf,
  reasonText,
  slowPollMs,
  stagesOf,
  stepErrorText,
  updateDateTimeAtOffset,
  updateDateTimeInputAtOffset,
  updateTimezoneName,
  updatesPollMs,
  type NodeUpdate,
  type Rollout,
  type Step,
  type Updates,
} from "./updates";

const t = Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(en[key], vars), {
  n: (key: keyof typeof en, n: number, vars?: Record<string, string | number>) => fill(pickForm(en[key], n, "en"), { n, ...vars }),
}) as T;

const node = (over: Partial<NodeUpdate> = {}): NodeUpdate => ({
  nodeId: "nod_1",
  name: "de1",
  version: "0.1.0-aaa",
  built: 100,
  supportsUpdate: true,
  crashGuard: true,
  state: NodeUpdateState.UP_TO_DATE,
  scheduledUnix: 0,
  scheduledVersion: "",
  scheduledBuilt: 0,
  scheduledTimezoneOffsetMinutes: 0,
  scheduledMissed: false,
  inbounds: 1,
  onlineUsers: 0,
  address: "de1.example.com",
  arch: "amd64",
  ...over,
});
const step = (over: Partial<Step> = {}): Step => ({
  nodeId: "nod_1",
  nodeName: "de1",
  stage: 0,
  state: StepState.PENDING,
  fromVersion: "",
  fromBuilt: 0,
  startedUnix: 0,
  finishedUnix: 0,
  errorKey: "",
  params: {},
  ...over,
});
const rollout = (over: Partial<Rollout> = {}): Rollout => ({
  id: "rol_1",
  status: RolloutStatus.RUNNING,
  toVersion: "0.2.0-bbb",
  toBuilt: 200,
  batchSize: 1,
  createdUnix: 10,
  finishedUnix: 0,
  pauseKey: "",
  pauseParams: {},
  steps: [],
  ...over,
});
const bundle = (status: BundleStatus, over: Partial<NonNullable<Updates["bundle"]>> = {}): NonNullable<Updates["bundle"]> => ({
  status,
  version: "0.2.0-bbb",
  built: 200,
  expiresUnix: 0,
  files: [],
  errorKey: "",
  params: {},
  scannedUnix: 0,
  ...over,
});
const updates = (over: Partial<Updates> = {}): Updates => ({
  nowUnix: 1000,
  scheduleTimezoneOffsetMinutes: 180,
  panel: { version: "0.2.0-bbb", built: 200, hasReleaseKey: true, releaseKeyFingerprint: "abcd" },
  bundle: bundle(BundleStatus.TRUSTED),
  nodes: [node({ state: NodeUpdateState.OUTDATED })],
  distDir: "/var/lib/mistgate/dist",
  ...over,
});

describe("updatesPollMs", () => {
  it("polls fast while a rollout runs or a node updates, slowly otherwise", () => {
    expect(updatesPollMs(undefined)).toBe(slowPollMs);
    expect(updatesPollMs(updates())).toBe(slowPollMs);
    expect(updatesPollMs(updates({ rollout: rollout({ status: RolloutStatus.RUNNING }) }))).toBe(fastPollMs);
    expect(updatesPollMs(updates({ nodes: [node({ state: NodeUpdateState.UPDATING })] }))).toBe(fastPollMs);
    // a paused rollout with nothing in flight is not worth 5 s
    expect(updatesPollMs(updates({ rollout: rollout({ status: RolloutStatus.PAUSED }) }))).toBe(slowPollMs);
    expect(updatesPollMs(updates({ rollout: rollout({ status: RolloutStatus.DONE }) }))).toBe(slowPollMs);
  });
});

describe("node states", () => {
  it("maps each state to a pill tone", () => {
    expect(nodeStateKind(NodeUpdateState.UP_TO_DATE)).toBe("ok");
    expect(nodeStateKind(NodeUpdateState.OUTDATED)).toBe("warn");
    expect(nodeStateKind(NodeUpdateState.UPDATING)).toBe("busy");
    expect(nodeStateKind(NodeUpdateState.ROLLED_BACK)).toBe("warn");
    expect(nodeStateKind(NodeUpdateState.FAILED)).toBe("bad");
    // the one step the panel cannot take itself is amber, not the grey of "offline"
    expect(nodeStateKind(NodeUpdateState.UNSUPPORTED)).toBe("warn");
    expect(nodeStateKind(NodeUpdateState.OFFLINE)).toBe("off");
  });

  it("offers updates only for supported old agents and keeps rollback separate", () => {
    const old = { built: 100, supportsUpdate: true };
    expect(canUpdateNode({ ...old, state: NodeUpdateState.OUTDATED }, 200)).toBe(true);
    expect(canUpdateNode({ ...old, state: NodeUpdateState.OFFLINE }, 200)).toBe(true);
    expect(canUpdateNode({ ...old, state: NodeUpdateState.ROLLED_BACK }, 200)).toBe(true);
    expect(canUpdateNode({ ...old, state: NodeUpdateState.FAILED }, 200)).toBe(true);
    expect(canUpdateNode({ ...old, state: NodeUpdateState.UNSUPPORTED }, 200)).toBe(false);
    expect(canUpdateNode({ ...old, state: NodeUpdateState.UP_TO_DATE }, 200)).toBe(false);
    expect(canUpdateNode({ ...old, supportsUpdate: false, state: NodeUpdateState.OUTDATED }, 200)).toBe(false);
    expect(canUpdateNode({ ...old, state: NodeUpdateState.OUTDATED }, 100)).toBe(false);

    const ok = { supportsUpdate: true, lastUpdate: { outcome: "ok" } } as NodeUpdate;
    expect(canRollbackNode({ ...ok, state: NodeUpdateState.UP_TO_DATE })).toBe(true);
    expect(canRollbackNode({ ...ok, state: NodeUpdateState.OFFLINE })).toBe(false);
    expect(canRollbackNode({ ...ok, state: NodeUpdateState.UPDATING })).toBe(false);
    expect(canRollbackNode({ ...ok, state: NodeUpdateState.UP_TO_DATE, lastUpdate: { outcome: "rolled_back" } as NodeUpdate["lastUpdate"] })).toBe(false);
    expect(canRollbackNode({ ...ok, state: NodeUpdateState.UP_TO_DATE, supportsUpdate: false })).toBe(false);
  });

  it("picks the canary the way the panel does: fewest online, then fewest profiles, then name", () => {
    const a = node({ nodeId: "a", name: "nl1", onlineUsers: 3, inbounds: 1 });
    const b = node({ nodeId: "b", name: "de1", onlineUsers: 1, inbounds: 4 });
    const c = node({ nodeId: "c", name: "fi1", onlineUsers: 1, inbounds: 2 });
    const d = node({ nodeId: "d", name: "ee0", onlineUsers: 1, inbounds: 2 });
    expect(canaryOf([a, b, c, d])?.nodeId).toBe("d");
    expect(canaryOf([])).toBeUndefined();
  });

  it("uses the panel's default batch size", () => {
    expect(defaultBatch(1)).toBe(1);
    expect(defaultBatch(4)).toBe(1);
    expect(defaultBatch(5)).toBe(2);
  });
});

describe("heroOf", () => {
  it("leads with a running or paused rollout", () => {
    expect(heroOf(updates({ rollout: rollout({ status: RolloutStatus.RUNNING }) })).id).toBe("running");
    expect(heroOf(updates({ rollout: rollout({ status: RolloutStatus.PAUSED }) })).id).toBe("paused");
    // a finished one is history, not the hero
    expect(heroOf(updates({ rollout: rollout({ status: RolloutStatus.DONE }) })).id).toBe("available");
  });

  it("follows the bundle when nodes wait for an update", () => {
    expect(heroOf(updates()).id).toBe("available");
    expect(heroOf(updates({ bundle: bundle(BundleStatus.MISSING) }))).toEqual({ id: "noBundle", older: 1 });
    expect(heroOf(updates({ bundle: bundle(BundleStatus.UNTRUSTED) })).id).toBe("untrusted");
    expect(heroOf(updates({ bundle: bundle(BundleStatus.NO_KEY) })).id).toBe("noKey");
  });

  it("says everything is current only when no node runs an older agent, whatever the bundle", () => {
    const current = node({ state: NodeUpdateState.UP_TO_DATE });
    const manual = node({ nodeId: "n2", name: "de2", state: NodeUpdateState.UNSUPPORTED });
    expect(heroOf(updates({ nodes: [current], bundle: bundle(BundleStatus.MISSING) }))).toEqual({ id: "current" });
    // a node that waits for a manual update is the one thing to do: the hero says so instead of "all up to date"
    expect(heroOf(updates({ nodes: [current, manual] }))).toEqual({ id: "manual", nodes: [manual] });
    expect(heroOf(updates({ nodes: [current, manual], bundle: bundle(BundleStatus.MISSING) })).id).toBe("manual");
    expect(heroOf(updates({ nodes: [] })).id).toBe("none");
  });

  it("does not call the fleet current while a node that rolled back or failed still runs the old build", () => {
    const rolled = node({ nodeId: "n3", state: NodeUpdateState.ROLLED_BACK });
    const failed = node({ nodeId: "n4", state: NodeUpdateState.FAILED });
    expect(heroOf(updates({ nodes: [node(), rolled] }))).toEqual({ id: "attention", retry: 1 });
    expect(heroOf(updates({ nodes: [rolled, failed] }))).toEqual({ id: "attention", retry: 2 });
    // something still waiting for an update comes first
    expect(heroOf(updates()).id).toBe("available");
  });

  it("formats timestamps with the saved fixed UTC offset, not the browser timezone", () => {
    expect(updateTimezoneName(180)).toBe("UTC+03:00");
    expect(updateTimezoneName(-210)).toBe("UTC−03:30");
    expect(updateDateTimeAtOffset(Date.UTC(2026, 9, 4, 12, 30) / 1000, 180)).toBe("2026-10-04 15:30");
    expect(updateDateTimeInputAtOffset(Date.UTC(2026, 9, 4, 12, 30) / 1000, 180)).toBe("2026-10-04T15:30");
  });
});

describe("stages and progress", () => {
  const steps = [
    step({ nodeId: "a", nodeName: "nl1", stage: 0, state: StepState.PASSED }),
    step({ nodeId: "b", nodeName: "de1", stage: 1, state: StepState.GATING }),
    step({ nodeId: "c", nodeName: "fi1", stage: 1, state: StepState.PENDING }),
    step({ nodeId: "d", nodeName: "de2", stage: 2, state: StepState.SKIPPED, errorKey: "updates.step.err.offline" }),
  ];

  it("groups steps into the canary, batches and a stage of skipped nodes", () => {
    const st = stagesOf(steps);
    expect(st.map((s) => [s.stage, s.kind, s.steps.length])).toEqual([
      [0, "canary", 1],
      [1, "batch", 2],
      [2, "skipped", 1],
    ]);
  });

  it("does not call a cancelled batch skipped: it is still the batch the owner cancelled", () => {
    const cancelled = [step({ stage: 1, state: StepState.SKIPPED, errorKey: "updates.step.err.cancelled" })];
    expect(stagesOf(cancelled)[0]?.kind).toBe("batch");
  });

  it("counts updated nodes against those the rollout goes through, leaving skipped ones out", () => {
    expect(progressOf(rollout({ steps }))).toEqual({ done: 1, total: 3 });
    expect(progressOf(rollout({ steps: [] }))).toEqual({ done: 0, total: 0 });
  });
});

describe("the words of server keys", () => {
  it("fills a step error with its params, turning a reason code into a sentence", () => {
    expect(stepErrorText(t, "updates.step.err.rolled_back_by_agent", { reason: "built_mismatch" })).toBe(
      "The node rolled itself back. The program is not the release the manifest describes.",
    );
    expect(stepErrorText(t, "updates.step.err.rollback_failed", { reason: "no_previous", gate: "probe_failed" })).toContain("The node has no previous version to return to.");
    expect(stepErrorText(t, "updates.step.err.failed", { detail: "disk full" })).toBe("The update failed on the node: disk full");
  });

  it("shows the code of a step error it does not know instead of a raw key", () => {
    expect(stepErrorText(t, "updates.step.err.some_new_code")).toBe("The step ended with an error (some_new_code).");
    expect(stepErrorText(t, "")).toBe("");
  });

  it("names the failing node in a pause reason, says the reason once, and keeps a free-text reason as it is", () => {
    const text = pauseText(t, "updates.pause.gate_failed", { node: "de1", reason: "probe_failed" });
    expect(text).toMatch(/^de1 did not pass the check after updating: the client-eye check failed\. Where possible/);
    expect(text.match(/check/g)).toHaveLength(2); // not "...the check after updating: The client-eye check failed after the update."
    // a code without a short form keeps its full sentence
    expect(pauseText(t, "updates.pause.step_failed", { node: "de1", reason: "bad_signature" })).toContain(en["updates.step.err.bad_signature"]);
    expect(reasonText(t, "failed: exec")).toBe(en["updates.last.reason.exec"]);
    expect(reasonText(t, "something odd")).toBe("something odd");
  });

  it("does not call a rollback the gate made 'yours'", () => {
    expect(reasonText(t, "gate_probe_failed")).toBe("The panel put the previous version back: the client-eye check failed after the update.");
    expect(reasonText(t, "gate_some_new_check")).toBe("The panel put the previous version back: the check after the update failed (some_new_check).");
    expect(reasonText(t, "command")).toBe(en["updates.last.reason.command"]);
    expect(reasonText(t, "manual")).toBe("Rolled back by you.");
  });

  it("finds the node a failure pause names, for the link to it", () => {
    const r = rollout({ status: RolloutStatus.PAUSED, pauseKey: "updates.pause.gate_failed", pauseParams: { node: "de2" }, steps: [step(), step({ nodeId: "nod_2", nodeName: "de2" })] });
    expect(isFailurePause(r.pauseKey)).toBe(true);
    expect(pausedStep(r)?.nodeId).toBe("nod_2");
    expect(isFailurePause("updates.pause.owner")).toBe(false);
    expect(isFailurePause("updates.pause.bundle_changed")).toBe(false);
  });
});

describe("manualCommands", () => {
  const files = [
    { os: "linux", arch: "amd64", name: "mistgate-node-linux-amd64", size: 1, sha256: "a" },
    { os: "linux", arch: "arm64", name: "mistgate-node-linux-arm64", size: 1, sha256: "b" },
  ];
  it("copies the binary of the node's architecture from the panel's dist folder, then installs it", () => {
    const c = manualCommands(updates({ bundle: bundle(BundleStatus.TRUSTED, { files }) }), node({ address: "de2.example.com", arch: "arm64" }));
    expect(c.copy).toBe("scp /var/lib/mistgate/dist/mistgate-node-linux-arm64 root@de2.example.com:/root/mistgate-node");
    expect(c.install).toBe("ssh root@de2.example.com 'chmod +x /root/mistgate-node && /root/mistgate-node install'");
    expect(c.guessedArch).toBe(false);
  });
  it("puts an IPv6 address in brackets for scp, and guesses amd64 when the node did not say", () => {
    const c = manualCommands(updates({ bundle: bundle(BundleStatus.TRUSTED, { files }) }), node({ address: "2001:db8::7", arch: "" }));
    expect(c.copy).toBe("scp /var/lib/mistgate/dist/mistgate-node-linux-amd64 root@[2001:db8::7]:/root/mistgate-node");
    expect(c.install).toContain("ssh root@2001:db8::7 ");
    expect(c.guessedArch).toBe(true);
  });
  it("has no copy line when the panel holds no binary for the node", () => {
    const c = manualCommands(updates({ bundle: bundle(BundleStatus.MISSING) }), node({ arch: "riscv64" }));
    expect(c.copy).toBeNull();
    expect(c.file).toBe("mistgate-node-linux-riscv64");
  });

  it("describes the last update of a node", () => {
    expect(lastUpdateView(t, undefined)).toMatchObject({ head: "No update yet", kind: "off" });
    expect(lastUpdateView(t, { outcome: "ok", fromVersion: "1", toVersion: "2", fromBuilt: 0, toBuilt: 0, reason: "", atUnix: 5 })).toMatchObject({ head: "1 → 2", kind: "ok" });
    expect(lastUpdateView(t, { outcome: "rolled_back", fromVersion: "1", toVersion: "2", fromBuilt: 0, toBuilt: 0, reason: "crash_loop", atUnix: 5 })).toEqual({
      head: "Rolled back from 2",
      detail: "The new version crashed three times in a row.",
      kind: "warn",
    });
    expect(lastUpdateView(t, { outcome: "failed", fromVersion: "", toVersion: "2", fromBuilt: 0, toBuilt: 0, reason: "", atUnix: 0 })).toMatchObject({ head: "Update to 2 failed", kind: "bad" });
  });

  it("maps the panel's short precondition messages, and the agent's refusal, to sentences", () => {
    const failed = (msg: string) => callErrorText(new ConnectError(msg, Code.FailedPrecondition), t);
    expect(failed("a rollout is already active")).toBe(en["up.e.active"]);
    expect(failed("no trusted bundle")).toBe(en["up.e.noBundle"]);
    expect(failed("agent refused: no_previous")).toBe(en["up.e.refused.no_previous"]);
    expect(failed("agent refused: something_new")).toBe("The node refused: something_new");
    expect(failed("an unmapped message")).toBe("an unmapped message");
    expect(callErrorText(new ConnectError("x", Code.Unavailable), t)).toBe(en["err.network"]);
  });
});

describe("dictionary", () => {
  // the codes the panel and the agents produce
  const stepCodes = ["bad_signature", "unsigned_build", "bad_manifest", "expired", "downgrade", "no_file_for_platform", "download_failed", "size_mismatch", "hash_mismatch", "not_writable", "busy", "no_answer", "not_reconnected", "state_not_applied", "inbound_failed", "probe_failed", "rolled_back_by_agent", "manual", "offline", "unsupported", "up_to_date", "cancelled", "agent_failed", "rollback_failed", "failed"];
  const bundleCodes = ["no_manifest", "no_signature", "bad_manifest", "unsupported_schema", "bad_signature", "expired", "file_missing", "file_mismatch"];
  const pause = ["owner", "step_failed", "gate_failed", "bundle_changed", "panel_restart_ambiguous"];
  // the agent's own codes, and the panel's names for who sent a rollback (update/reason.go)
  const reason = ["not_committed", "built_mismatch", "crash_loop", "apply_failed", "manual", "interrupted", "gate", "gate_probe_failed", "gate_inbound_failed", "gate_state_not_applied", "command"];
  // the gate's codes, which a pause names in a short form
  const pauseWhy = ["probe_failed", "inbound_failed", "state_not_applied", "not_reconnected", "no_answer", "rolled_back_by_agent", "agent_failed"];

  it("has a sentence in both languages for every code the server can send", () => {
    const keys = [
      ...stepCodes.map((c) => `updates.step.err.${c}`),
      ...bundleCodes.map((c) => `updates.bundle.err.${c}`),
      ...pause.map((c) => `updates.pause.${c}`),
      ...reason.map((c) => `updates.last.reason.${c}`),
      ...pauseWhy.map((c) => `updates.pause.why.${c}`),
    ];
    for (const k of keys) {
      expect(en, k).toHaveProperty([k]);
      expect(ru, k).toHaveProperty([k]);
    }
  });

  it("explains the update_failed alert for each pause it raises", () => {
    for (const c of ["step_failed", "gate_failed", "panel_restart_ambiguous", "unknown"]) expect(ru).toHaveProperty([`health.alert.update_failed.why.${c}`]);
    expect(ru["health.alert.update_failed.title"]).toBeTruthy();
  });
});
