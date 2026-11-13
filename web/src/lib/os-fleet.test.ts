import { describe, expect, it } from "vitest";
import { OsRebootPolicy, OsRolloutMode, OsRolloutStatus, OsStepState, OsUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { dictionaries, fill, langs, pickForm, type Lang, type T } from "@/i18n";
import type { en } from "@/i18n/en";
import {
  canaryFirst, currentStage, defaultBatchSize, defaultScope, fleetCounts, fleetPhase, fleetStages, installable, isFleetRun, leftoverText, lockNowText, lockRefusedText, maxBatchSize,
  planStages, rebootOf, retriedBefore, scopeBlock, securityNames, stepChip, stoppedBy, waitText,
} from "./os-fleet";
import { reasonLine, stepReason } from "./os-run";
import type { Os, OsRow, OsRun, OsStepRow } from "./os-updates";

// The OS update of several nodes: which nodes a kind of run takes, the order and stages the window shows, how a run in progress reads,
// and the names of the security packages and the package lock.

const tOf = (lang: Lang): T => {
  const dict = dictionaries[lang];
  return Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(dict[key], vars), {
    n: (key: keyof typeof en, count: number, vars?: Record<string, string | number>) => fill(pickForm(dict[key], count, lang), { n: count, ...vars }),
  }) as unknown as T;
};
const t = tOf("en");

const NOW = 1_800_000_000;
const status = (over: Partial<Os> = {}): Os => ({ pending: 0, security: 0, apt: "ok", securityPending: [], securityStuck: [], aptLockHolder: "", aptLockSinceUnix: 0, ...over }) as unknown as Os;
const row = (name: string, online: number, over: Partial<OsRow> = {}): OsRow => ({
  id: `nod_${name}`, name, nodeId: `nod_${name}`, online, onPanelServer: false, state: OsUpdateState.UPDATES, status: status({ pending: 4, security: 2 }), updateCapable: true, updateBlock: "", rebootBlock: "", nowUnix: NOW, ...over,
});
const step = (over: Partial<OsStepRow> = {}): OsStepRow =>
  ({ nodeId: "nod_de1", nodeName: "de1", state: OsStepState.PENDING, jobId: "", phase: "", phaseUnix: 0, kernelFrom: "", kernelTo: "", sentUnix: 0, rebootUnix: 0, backUnix: 0, finishedUnix: 0, errorKey: "", params: {}, stage: 0, retryAfterUnix: 0, ...over }) as OsStepRow;
const run = (steps: OsStepRow[], over: Partial<OsRun> = {}): OsRun =>
  ({ id: "osr_1", status: OsRolloutStatus.RUNNING, mode: OsRolloutMode.SECURITY, reboot: OsRebootPolicy.IF_REQUIRED, createdUnix: NOW - 300, finishedUnix: 0, pauseKey: "", pauseParams: {}, steps, batchSize: 2, ...over }) as OsRun;

describe("which nodes a kind of run takes", () => {
  const ordinary = row("sg1", 2, { updateBlock: "nothing_to_do", status: status({ pending: 18 }) });
  const nothing = row("nl1", 0, { updateBlock: "nothing_to_do", status: status() });

  it("takes a node with security updates for every kind of install", () => {
    expect(scopeBlock(row("de1", 3), "security")).toBe("");
    expect(scopeBlock(row("de1", 3), "all")).toBe("");
  });

  it("takes a node with only ordinary packages for All packages, and says there is no security update for the other kind", () => {
    expect(scopeBlock(ordinary, "all")).toBe("");
    expect(scopeBlock(ordinary, "security")).toBe("nothing_to_do");
  });

  it("leaves a node with nothing to install out of both, and still offers it a reboot", () => {
    expect(scopeBlock(nothing, "all")).toBe("nothing_to_do");
    expect(scopeBlock(nothing, "security")).toBe("nothing_to_do");
    expect(scopeBlock(nothing, "reboot")).toBe("");
  });

  it("gives the server's reason for a node that cannot, and a held-up reboot its own", () => {
    expect(scopeBlock(row("msk1", 0, { updateBlock: "agent_old", rebootBlock: "agent_old" }), "security")).toBe("agent_old");
    expect(scopeBlock(row("fi1", 1, { rebootBlock: "reboot_wait", rebootPossibleUnix: NOW + 600 }), "reboot")).toBe("reboot_wait:10");
    // an open run holds every node
    expect(scopeBlock(row("de1", 3, { rollout: run([step()]) }), "all")).toBe("rollout_active");
    // the panel's own row is never a node of a run
    expect(scopeBlock({ ...row("panel", 0), nodeId: undefined }, "all")).toBe("panel_host");
  });

  it("ticks every node with something to install, and opens on the kind that fits the ticked ones", () => {
    const rows = [row("de1", 3), ordinary, nothing];
    expect(installable(rows).map((r) => r.name)).toEqual(["de1", "sg1"]);
    expect(defaultScope(rows)).toBe("security");
    expect(defaultScope([ordinary, nothing])).toBe("all");
    expect(defaultScope([nothing])).toBe("security");
  });

  it("asks for the reboot a kind implies: a reboot-only run always reboots", () => {
    expect(rebootOf("reboot", "never")).toBe(OsRebootPolicy.ALWAYS);
    expect(rebootOf("security", "never")).toBe(OsRebootPolicy.NEVER);
    expect(rebootOf("all", "if_required")).toBe(OsRebootPolicy.IF_REQUIRED);
  });
});

describe("the order and the stages", () => {
  const rows = [row("fi1", 5), row("de1", 3), row("nl1", 0), row("sg1", 2), row("se1", 0)];

  it("puts the node with the fewest people online first, ties by name", () => {
    expect(canaryFirst(rows).map((r) => r.name)).toEqual(["nl1", "se1", "sg1", "de1", "fi1"]);
  });

  it("makes the canary a stage of its own and fills the next ones by the batch size", () => {
    const stages = planStages(rows, 2);
    expect(stages.map((s) => [s.stage, s.rows.map((r) => r.name)])).toEqual([[0, ["nl1"]], [1, ["se1", "sg1"]], [2, ["de1", "fi1"]]]);
    expect(planStages(rows, 1).map((s) => s.rows.length)).toEqual([1, 1, 1, 1, 1]);
    expect(planStages([row("de1", 1)], 3).map((s) => s.rows.length)).toEqual([1]);
  });

  it("offers a batch the way the agent rollout does, never above 5 and never more than are left", () => {
    expect(defaultBatchSize(3)).toBe(1);
    expect(defaultBatchSize(8)).toBe(2);
    expect(maxBatchSize(2)).toBe(1);
    expect(maxBatchSize(4)).toBe(3);
    expect(maxBatchSize(20)).toBe(5);
  });
});

describe("a run in progress", () => {
  const passed = step({ nodeId: "nod_nl1", nodeName: "nl1", state: OsStepState.PASSED, stage: 0 });
  const failed = step({ state: OsStepState.FAILED, stage: 1, errorKey: "reboot_held", params: { code: "boot_default_custom" } });

  it("is one run of several nodes only when it has more than one step", () => {
    expect(isFleetRun(run([passed]))).toBe(false);
    expect(isFleetRun(run([passed, failed]))).toBe(true);
    expect(isFleetRun(undefined)).toBe(false);
  });

  it("tells a pause by the owner from a stop by a failed step: both are PAUSED on the server", () => {
    expect(fleetPhase(run([passed, step({ stage: 1 })]))).toBe("running");
    expect(fleetPhase(run([passed, step({ stage: 1 })], { status: OsRolloutStatus.PAUSED, pauseKey: "owner" }))).toBe("paused");
    expect(fleetPhase(run([passed, failed], { status: OsRolloutStatus.PAUSED, pauseKey: "reboot_held" }))).toBe("stopped");
    expect(fleetPhase(run([passed, failed], { status: OsRolloutStatus.PAUSED, pauseKey: "cancel_wait" }))).toBe("cancelling");
    expect(fleetPhase(run([passed], { status: OsRolloutStatus.DONE }))).toBe("done");
    expect(fleetPhase(run([passed], { status: OsRolloutStatus.CANCELLED }))).toBe("cancelled");
    expect(fleetPhase(run([passed, failed], { status: OsRolloutStatus.FAILED }))).toBe("failed");
  });

  it("counts steps by where they stand: a skipped node counts as done, a failed one does not", () => {
    const r = run([
      passed,
      step({ state: OsStepState.SKIPPED, stage: 1 }),
      failed,
      step({ state: OsStepState.SENT, stage: 1 }),
      step({ state: OsStepState.GATING, stage: 1 }),
      step({ state: OsStepState.PENDING, stage: 2 }),
    ]);
    expect(fleetCounts(r)).toEqual({ total: 6, done: 2, passed: 1, working: 2, queued: 1, skipped: 1, failed: 1 });
  });

  it("groups by stage and marks the stage that is being worked on", () => {
    const r = run([passed, step({ nodeId: "nod_a", state: OsStepState.SENT, stage: 1 }), step({ nodeId: "nod_b", stage: 2 })]);
    const stages = fleetStages(r);
    expect(stages.map((s) => [s.stage, s.steps.length])).toEqual([[0, 1], [1, 1], [2, 1]]);
    expect(currentStage(stages)).toBe(1);
    expect(currentStage(fleetStages(run([passed, step({ stage: 1 })])))).toBe(1);
  });

  it("finds the failed step and whether it was retried already", () => {
    expect(stoppedBy(run([passed, failed]))?.nodeName).toBe("de1");
    expect(retriedBefore(failed)).toBe(false);
    expect(retriedBefore({ ...failed, params: { retry_count: "1" } })).toBe(true);
    expect(retriedBefore(undefined)).toBe(false);
  });
});

describe("the state of a node as a chip", () => {
  it("is a quiet grey one while it waits its turn or another try, never red", () => {
    expect(stepChip(t, step(), NOW)).toEqual({ kind: "off", label: "In the queue" });
    const waiting = step({ phase: "waiting_retry", retryAfterUnix: NOW + 11 * 60 - 5, params: { retry_code: "apt_lock" } });
    expect(stepChip(t, waiting, NOW)).toEqual({ kind: "off", label: "Can retry in 11 min" });
    // the time has come: the panel is trying again
    expect(stepChip(t, { ...waiting, retryAfterUnix: NOW - 1 }, NOW)).toEqual({ kind: "busy", label: "Trying again" });
  });

  it("says what a running node does: the phase of its job, the reboot, the check", () => {
    expect(stepChip(t, step({ state: OsStepState.SENT, phase: "installing" }), NOW).label).toBe("Installing");
    expect(stepChip(t, step({ state: OsStepState.SENT, phase: "waiting_lock" }), NOW).label).toBe("Waiting for the package lock");
    expect(stepChip(t, step({ state: OsStepState.SENT, phase: "" }), NOW).label).toBe("Starting");
    expect(stepChip(t, step({ state: OsStepState.REBOOTING }), NOW)).toEqual({ kind: "busy", label: "Rebooting" });
    expect(stepChip(t, step({ state: OsStepState.GATING }), NOW)).toEqual({ kind: "busy", label: "Checking" });
  });

  it("has a word for every end: done, stopped, skipped, and not now", () => {
    expect(stepChip(t, step({ state: OsStepState.PASSED }), NOW)).toEqual({ kind: "ok", label: "Done" });
    expect(stepChip(t, step({ state: OsStepState.FAILED }), NOW)).toEqual({ kind: "bad", label: "Stopped" });
    expect(stepChip(t, step({ state: OsStepState.SKIPPED, errorKey: "offline" }), NOW)).toEqual({ kind: "off", label: "Skipped" });
    expect(stepChip(t, step({ state: OsStepState.SKIPPED, errorKey: "try_later" }), NOW)).toEqual({ kind: "off", label: "Not now" });
  });

  it("explains a wait with its reason, its time and the holder of the lock", () => {
    const s = step({ phase: "waiting_retry", retryAfterUnix: NOW + 600, params: { retry_code: "apt_lock" } });
    const holder = status({ apt: "locked", aptLockHolder: "unattended-upgr", aptLockSinceUnix: NOW - 14 * 60 });
    const line = waitText(t, s, holder, NOW);
    expect(line).toContain("another package manager holds the lock");
    expect(line).toContain("next in 10 min");
    expect(line).toContain("unattended-upgr has been installing packages on the node for 14 min");
    // another reason, nothing known of the lock
    expect(waitText(t, { ...s, params: { retry_code: "too_soon" } }, undefined, NOW)).toContain("rebooted only a few minutes ago");
    // a code nobody knows reads as "another job is running"
    expect(waitText(t, { ...s, params: { retry_code: "whatever" } }, undefined, NOW)).toContain("another job is running");
    expect(waitText(t, { ...s, retryAfterUnix: NOW - 1 }, undefined, NOW)).toContain("trying again");
  });
});

describe("the names of the security packages and the package lock", () => {
  it("lists the names, marks the stuck ones and counts what the list does not name", () => {
    const n = securityNames(status({ security: 40, securityPending: ["libfoo1", "libbar-common"], securityStuck: ["libfoo1"] }));
    expect(n.names).toEqual(["libfoo1", "libbar-common"]);
    expect(n.stuck.has("libfoo1")).toBe(true);
    expect(n.stuck.has("libbar-common")).toBe(false);
    expect(n.more).toBe(38);
    // an agent that names nothing: no list
    expect(securityNames(status({ security: 2 })).names).toEqual([]);
    expect(securityNames(undefined).more).toBe(0);
  });

  it("names the process that holds the lock and for how long, only when the node says both", () => {
    expect(lockNowText(t, status({ aptLockHolder: "unattended-upgr", aptLockSinceUnix: NOW - 14 * 60 }), NOW)).toBe("unattended-upgr has been installing packages on the node for 14 min.");
    expect(lockNowText(t, status({ aptLockHolder: "unattended-upgr" }), NOW)).toBe("");
    expect(lockNowText(t, status(), NOW)).toBe("");
  });

  it("says who held the lock for the whole wait of a refusal, from the node's job", () => {
    const s = status({ job: { aptLockHolder: "unattended-upgr", aptLockMinutes: 30, code: "apt_lock" } } as Partial<Os>);
    expect(lockRefusedText(t, s, "apt_lock")).toContain("unattended-upgr held the package lock for the whole 30-minute wait");
    expect(lockRefusedText(t, s, "disk_full")).toBe("");
    expect(lockRefusedText(t, status(), "apt_lock")).toBe("");
  });

  it("words only the note it knows, in its own words, not the node's", () => {
    expect(leftoverText(t, status({ job: { noteCode: "security_leftover_failed", note: "some English from the node" } } as Partial<Os>))).toBe("After the automatic update some security packages are still waiting.");
    expect(leftoverText(t, status({ job: { noteCode: "something_new", note: "x" } } as Partial<Os>))).toBe("");
  });
});

describe("why a step was left alone", () => {
  it("words the keys the fleet adds: a skip after a failure, a refused reboot check, the owner's pause", () => {
    expect(reasonLine(t, "skipped_after_failure")).toBe("This node had failed. You chose to skip it and carry on.");
    const r = stepReason(t, "initramfs_missing", { code: "initramfs_missing" });
    expect(r.title).toContain("a reboot would be refused");
    expect(r.detail).toContain("files that start the new kernel");
  });
});

describe("the words exist in every language", () => {
  const keys = ["osf.go", "osf.run.title", "osf.sel.count", "osf.sec.toggle", "osf.wait.line", "subs.idle.on", "users.devIdle", "osf.btn.skip"] as const;
  it("has each key, filled, in all five", () => {
    for (const lang of langs) {
      const tt = tOf(lang);
      for (const key of keys) expect(tt.n(key, 3, { first: "de1", reason: "x", time: "5", done: 1, n: 3 }), `${lang} ${key}`).not.toMatch(/\{\w+\}/);
    }
  });

  it("keeps the Russian plural forms of the buttons and the progress", () => {
    const ru = tOf("ru");
    expect(ru.n("osf.sel.go", 1)).toBe("Обновить 1 ноду…");
    expect(ru.n("osf.sel.go", 3)).toBe("Обновить 3 ноды…");
    expect(ru.n("osf.sel.go", 5)).toBe("Обновить 5 нод…");
    expect(ru.n("subs.idle.on", 21)).toContain("не подключалось 21 день");
    expect(ru.n("subs.idle.on", 30)).toContain("30 дней");
  });
});
