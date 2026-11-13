import { useState } from "react";
import { Button } from "@/components/ui/button";
import { OsRebootPolicy, OsRolloutMode, OsRolloutStatus, OsStepState, OsUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { updates } from "@/lib/api";
import type { OsScope } from "@/lib/os-fleet";
import { useOsActions } from "@/lib/os-run";
import { osRows, type OsRow, type OsUpdates } from "@/lib/os-updates";
import { OsCard } from "@/screens/node/os-card";
import { answer, base, node, now, rollout, Shot, step } from "@/screens/dev-kit-os";
import { OsConfirmDialog } from "@/screens/updates/os-confirm";
import { OsFleetDialog } from "@/screens/updates/os-fleet-dialog";
import { OsFleetRun } from "@/screens/updates/os-fleet-run";
import { OsSection } from "@/screens/updates/os";

// Development only, /dev-kit?only=os-fleet: the OS update of several nodes on made-up data. The table with ticks and the bar above
// it; the window before the run (?dialog=fleet|fleet-5|fleet-all|fleet-reboot|fleet-none, ?dialog=single-security|single-all|cancel opens
// the window of one node and the cancel window); the queue in each state (under way with a node waiting for the package lock,
// rebooting and checking, paused by the owner, stopped by a failed node, the same after a retry, cancelling, finished); the
// names of the security packages and the package lock. ?lang=ru|fa|zh|tr and ?theme come from the dev kit itself.

const min = 60;

// five nodes: nl1 is the quietest (the canary), de1 and fi1 have security updates, sg1 only ordinary packages, msk1 an old agent
const pkg = { securityPending: ["libfoo1", "libbar-common"], securityStuck: [] as string[] };
const nl1 = (over: Record<string, unknown> = {}, status: Record<string, unknown> = {}) =>
  node({ nodeId: "nod_nl1", name: "nl1", onlineUsers: 0, state: OsUpdateState.REBOOT_REQUIRED, status: { ...base, pending: 7, security: 2, kernelNewest: "6.8.0-150-generic", rebootRequired: true, rebootReasons: ["kernel"], ...pkg, ...status }, ...over });
const de1 = (over: Record<string, unknown> = {}, status: Record<string, unknown> = {}) =>
  node({ nodeId: "nod_de1", name: "de1", onlineUsers: 3, state: OsUpdateState.UPDATES, status: { ...base, pending: 12, security: 4, securityPending: ["libfoo1", "libbar-common", "libbaz2", "libqux0"], securityStuck: [], ...status }, ...over });
const fi1 = (over: Record<string, unknown> = {}, status: Record<string, unknown> = {}) =>
  node({ nodeId: "nod_fi1", name: "fi1", onlineUsers: 5, state: OsUpdateState.UPDATES, status: { ...base, pending: 3, security: 1, securityPending: ["libqux0"], securityStuck: [], ...status }, ...over });
const sg1 = (over: Record<string, unknown> = {}) =>
  node({ nodeId: "nod_sg1", name: "sg1", onlineUsers: 2, state: OsUpdateState.UP_TO_DATE, updateBlock: "nothing_to_do", status: { ...base, pending: 18 }, ...over });
const msk1 = (over: Record<string, unknown> = {}) =>
  node({ nodeId: "nod_msk1", name: "msk1", supported: false, state: OsUpdateState.UNSUPPORTED, status: undefined, updateCapable: false, updateBlock: "agent_old", rebootBlock: "agent_old", ...over });

/** The nodes of a run in progress: every one is held by it. */
const busy = { updateBlock: "rollout_active", rebootBlock: "rollout_active" };
const held = <T,>(f: (o: Record<string, unknown>) => T) => f(busy);

const idle = answer([nl1(), de1(), fi1(), sg1(), msk1()]);
// five nodes that can all go (the biggest window the dev kit shows)
const five = answer([nl1(), de1(), fi1(), sg1({ updateBlock: "", state: OsUpdateState.UPDATES, status: { ...base, pending: 6, security: 2 } }), msk1({ supported: true, updateCapable: true, updateBlock: "", rebootBlock: "", state: OsUpdateState.UPDATES, onlineUsers: 1, status: { ...base, pending: 4, security: 1 } })]);

const noGate = { mode: "security" };
const passedNl1 = step("nod_nl1", "nl1", { state: OsStepState.PASSED, stage: 0, kernelFrom: "6.8.0-147-generic", kernelTo: "6.8.0-150-generic", sentUnix: now - 1300, rebootUnix: now - 1000, backUnix: now - 800, finishedUnix: now - 600, params: { upgraded: "7", reboot: "done", kernel: "6.8.0-150-generic", mode: "security" } });
const queued = (id: string, name: string, stage: number, over: Record<string, unknown> = {}) => step(id, name, { state: OsStepState.PENDING, stage, params: noGate, ...over });
const skippedOld = step("nod_msk1", "msk1", { state: OsStepState.SKIPPED, stage: 2, errorKey: "unsupported", finishedUnix: now - 1400, params: noGate });
const run = (steps: unknown[], over: Record<string, unknown> = {}) => rollout(steps, { batchSize: 2, createdUnix: now - 1500, ...over });

const lockedStatus = { apt: "locked", aptLockHolder: "unattended-upgr", aptLockSinceUnix: now - 14 * min };

const scenes = {
  // under way: nl1 (the canary) is done, de1 installs, fi1 waits for the package lock held by an automatic update, sg1 is queued
  running: answer(
    [nl1({ state: OsUpdateState.UP_TO_DATE }, { pending: 0, security: 0, securityPending: [], rebootRequired: false, rebootReasons: [], kernelNewest: "6.8.0-150-generic", kernelRunning: "6.8.0-150-generic" }), held(de1), held((o) => fi1(o, lockedStatus)), held(sg1), msk1()],
    run([
      passedNl1,
      step("nod_de1", "de1", { state: OsStepState.SENT, stage: 1, phase: "installing", phaseUnix: now - 120, sentUnix: now - 150, params: noGate }),
      queued("nod_fi1", "fi1", 1, { phase: "waiting_retry", phaseUnix: now - 200, retryAfterUnix: now + 11 * min, retryCode: "apt_lock", params: { ...noGate, retry_code: "apt_lock", retry_after_unix: String(now + 11 * min) } }),
      queued("nod_sg1", "sg1", 2),
      skippedOld,
    ]),
  ),
  // the same, later: de1 reboots, fi1 is back and the panel checks its profiles, sg1 and msk1 wait for the second batch
  rebooting: answer(
    [nl1({ state: OsUpdateState.UP_TO_DATE }, { pending: 0, security: 0, securityPending: [], rebootRequired: false, rebootReasons: [], kernelRunning: "6.8.0-150-generic", kernelNewest: "6.8.0-150-generic" }), held(de1), held(fi1), held(sg1), msk1()],
    run([
      passedNl1,
      step("nod_de1", "de1", { state: OsStepState.REBOOTING, stage: 1, kernelFrom: "6.8.0-147-generic", kernelTo: "6.8.0-150-generic", sentUnix: now - 500, rebootUnix: now - 200, params: noGate }),
      step("nod_fi1", "fi1", { state: OsStepState.GATING, stage: 1, kernelFrom: "6.8.0-147-generic", kernelTo: "6.8.0-150-generic", sentUnix: now - 700, rebootUnix: now - 400, backUnix: now - 90, params: { ...noGate, gate_for: "profiles", gate_names: JSON.stringify(["Berlin main"]), gate_max_s: "480" } }),
      queued("nod_sg1", "sg1", 2),
      skippedOld,
    ]),
  ),
  // paused by the owner: nothing new starts, what runs goes on
  paused: answer(
    [nl1({ state: OsUpdateState.UP_TO_DATE }, { pending: 0, security: 0, securityPending: [], rebootRequired: false, rebootReasons: [], kernelRunning: "6.8.0-150-generic", kernelNewest: "6.8.0-150-generic" }), held(de1), held(fi1), held(sg1), msk1()],
    run([passedNl1, queued("nod_de1", "de1", 1), queued("nod_fi1", "fi1", 1), queued("nod_sg1", "sg1", 2), skippedOld], { status: OsRolloutStatus.PAUSED, pauseKey: "owner" }),
  ),
  // stopped by a node that refused its reboot: retry, skip and continue, close
  failed: answer(
    [nl1({ state: OsUpdateState.UP_TO_DATE }, { pending: 0, security: 0, securityPending: [], rebootRequired: false, rebootReasons: [], kernelRunning: "6.8.0-150-generic", kernelNewest: "6.8.0-150-generic" }), held(de1), held(fi1), held(sg1), msk1()],
    run(
      [
        passedNl1,
        step("nod_de1", "de1", { state: OsStepState.FAILED, stage: 1, errorKey: "reboot_held", sentUnix: now - 700, finishedUnix: now - 300, params: { code: "boot_default_custom", upgraded: "4", reboot: "held", mode: "security" } }),
        queued("nod_fi1", "fi1", 1),
        queued("nod_sg1", "sg1", 2),
        skippedOld,
      ],
      { status: OsRolloutStatus.PAUSED, pauseKey: "reboot_held", pauseParams: { node: "de1", reason: "reboot_held", code: "boot_default_custom", mode: "security" } },
    ),
  ),
  // the same node, retried once already: only skip or close are left
  retried: answer(
    [nl1({ state: OsUpdateState.UP_TO_DATE }, { pending: 0, security: 0, securityPending: [], rebootRequired: false, rebootReasons: [], kernelRunning: "6.8.0-150-generic", kernelNewest: "6.8.0-150-generic" }), held(de1), held(fi1), held(sg1), msk1()],
    run(
      [
        passedNl1,
        step("nod_de1", "de1", { state: OsStepState.FAILED, stage: 1, errorKey: "not_back", sentUnix: now - 2000, rebootUnix: now - 1500, finishedUnix: now - 300, retryCount: 1, params: { retry_count: "1", mode: "security" } }),
        queued("nod_fi1", "fi1", 1),
        queued("nod_sg1", "sg1", 2),
        skippedOld,
      ],
      { status: OsRolloutStatus.PAUSED, pauseKey: "not_back", pauseParams: { node: "de1", reason: "not_back", mode: "security" } },
    ),
  ),
  // cancelled while de1 still installs: that job is never cut short, the rest is skipped
  cancelling: answer(
    [nl1({ state: OsUpdateState.UP_TO_DATE }, { pending: 0, security: 0, securityPending: [], rebootRequired: false, rebootReasons: [], kernelRunning: "6.8.0-150-generic", kernelNewest: "6.8.0-150-generic" }), held(de1), held(fi1), held(sg1), msk1()],
    run(
      [
        passedNl1,
        step("nod_de1", "de1", { state: OsStepState.SENT, stage: 1, phase: "installing", phaseUnix: now - 100, sentUnix: now - 130, params: noGate }),
        step("nod_fi1", "fi1", { state: OsStepState.SKIPPED, stage: 1, errorKey: "cancelled", finishedUnix: now - 40, params: noGate }),
        step("nod_sg1", "sg1", { state: OsStepState.SKIPPED, stage: 2, errorKey: "cancelled", finishedUnix: now - 40, params: noGate }),
        skippedOld,
      ],
      { status: OsRolloutStatus.PAUSED, pauseKey: "cancel_wait", pauseParams: { cancel_requested: "true" } },
    ),
  ),
  done: answer(
    [nl1({ state: OsUpdateState.UP_TO_DATE }, { pending: 0, security: 0, securityPending: [], rebootRequired: false, rebootReasons: [], kernelRunning: "6.8.0-150-generic", kernelNewest: "6.8.0-150-generic" }), de1({ state: OsUpdateState.UP_TO_DATE }, { pending: 0, security: 0, securityPending: [] }), fi1({ state: OsUpdateState.UP_TO_DATE }, { pending: 0, security: 0, securityPending: [] }), sg1({ updateBlock: "nothing_to_do", status: { ...base } }), msk1()],
    run(
      [
        passedNl1,
        step("nod_de1", "de1", { state: OsStepState.PASSED, stage: 1, sentUnix: now - 900, finishedUnix: now - 500, params: { upgraded: "12", reboot: "not_required", mode: "all" } }),
        step("nod_fi1", "fi1", { state: OsStepState.PASSED, stage: 1, sentUnix: now - 900, finishedUnix: now - 450, params: { upgraded: "3", reboot: "not_required", mode: "all" } }),
        step("nod_sg1", "sg1", { state: OsStepState.PASSED, stage: 2, sentUnix: now - 400, finishedUnix: now - 200, params: { upgraded: "18", reboot: "not_asked", mode: "all" } }),
        skippedOld,
      ],
      { status: OsRolloutStatus.DONE, mode: OsRolloutMode.ALL, reboot: OsRebootPolicy.NEVER, finishedUnix: now - 200, createdUnix: now - 1500 },
    ),
  ),
  // the names of the security packages: two of the four are stuck (the automatic updates do not take them)
  security: answer([nl1(), de1({}, { securityStuck: ["libfoo1", "libbar-common"] }), fi1(), sg1(), msk1()]),
  // an automatic update holds the package lock: said calmly, with how long
  lock: answer([nl1(), de1({}, lockedStatus), fi1(), sg1(), msk1()]),
  // the node refused because of the lock for the whole wait; the run closed itself, nothing was changed
  lockRefused: answer(
    [nl1(), de1(), fi1(), sg1(), msk1()],
    rollout(
      [step("nod_de1", "de1", { state: OsStepState.SKIPPED, errorKey: "try_later", finishedUnix: now - 200, params: { code: "apt_lock", mode: "security", apt_lock_holder: "unattended-upgr", apt_lock_minutes: "30" } })],
      { status: OsRolloutStatus.DONE, finishedUnix: now - 200 },
    ),
  ),
  // one node, waiting for another try after "not now": the same calm block as in a run of several
  waitOne: answer(
    [nl1(), de1({ ...busy }, lockedStatus), fi1(), sg1(), msk1()],
    rollout([step("nod_de1", "de1", { state: OsStepState.PENDING, phase: "waiting_retry", phaseUnix: now - 300, retryAfterUnix: now + 8 * min, retryCode: "apt_lock", params: { retry_code: "apt_lock", retry_after_unix: String(now + 8 * min), mode: "security" } })], { createdUnix: now - 300 }),
  ),
  // an automatic update left part of the security packages behind
  leftover: answer(
    [nl1(), de1(), fi1(), sg1(), msk1()],
    rollout(
      [step("nod_de1", "de1", { state: OsStepState.PASSED, finishedUnix: now - 100, sentUnix: now - 700, params: { upgraded: "2", reboot: "not_asked", mode: "security", note_code: "security_leftover_failed", note: "2 packages still pending after unattended-upgrade" } })],
      { status: OsRolloutStatus.DONE, finishedUnix: now - 100, reboot: OsRebootPolicy.NEVER },
    ),
  ),
} satisfies Record<string, OsUpdates>;

function fakeCalls() {
  const api = updates as unknown as Record<string, unknown>;
  const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));
  for (const name of ["pauseOsRollout", "resumeOsRollout"]) {
    api[name] = async () => {
      await wait(500);
      return { rollout: scenes.running.rollout };
    };
  }
}
fakeCalls();

const rowsOf = (d: OsUpdates, ids: string[]): OsRow[] => osRows(d).filter((r) => !!r.nodeId && ids.includes(r.nodeId));
const ticked = ["nod_nl1", "nod_de1", "nod_fi1", "nod_sg1", "nod_msk1"];

/** The window before a run, closed until a button (or ?dialog=) opens it. */
function DialogBlock({ auto }: { auto: string | null }) {
  const actions = useOsActions();
  const [open, setOpen] = useState<string | null>(auto);
  const nodeRow = osRows(scenes.security)[2]!; // de1
  const onlyOrdinary: OsRow = osRows(idle)[4]!; // sg1
  const pick = (ids: string[]) => rowsOf(idle, ids);
  const close = () => setOpen(null);
  const scope: OsScope | undefined = open === "fleet-all" ? "all" : open === "fleet-reboot" ? "reboot" : undefined;
  return (
    <>
      <div className="flex flex-wrap gap-2">
        <Button onClick={() => setOpen("fleet")}>Fleet window: security</Button>
        <Button onClick={() => setOpen("fleet-5")}>Five nodes</Button>
        <Button onClick={() => setOpen("fleet-all")}>All packages</Button>
        <Button onClick={() => setOpen("fleet-reboot")}>Only a reboot</Button>
        <Button onClick={() => setOpen("fleet-none")}>Nothing can go</Button>
        <Button onClick={() => setOpen("single-security")}>One node</Button>
        <Button onClick={() => setOpen("single-all")}>One node: all packages</Button>
        <Button onClick={() => setOpen("single-ordinary")}>One node: only ordinary packages</Button>
      </div>
      {open?.startsWith("fleet") && <OsFleetDialog rows={open === "fleet-none" ? pick(["nod_msk1"]) : open === "fleet-5" ? rowsOf(five, ticked) : pick(ticked)} actions={actions} initialScope={scope} onClose={close} />}
      {open === "single-security" && <OsConfirmDialog row={nodeRow} mode="install" actions={actions} onClose={close} />}
      {open === "single-all" && <OsConfirmDialog row={nodeRow} mode="install" actions={actions} initialPkgs="all" onClose={close} />}
      {open === "single-ordinary" && <OsConfirmDialog row={onlyOrdinary} mode="install" actions={actions} onClose={close} />}
    </>
  );
}

/** The queue card alone, for the cancel window. */
function RunBlock({ data, asking }: { data: OsUpdates; asking: boolean }) {
  const actions = useOsActions();
  return <OsFleetRun run={data.rollout!} owner actions={actions} nowSec={now} askingCancel={asking} />;
}

export function OsFleetKit() {
  const params = new URLSearchParams(location.search);
  const dialog = params.get("dialog");
  return (
    <div className="flex flex-col gap-8">
      <Shot id="os-fleet-select" title="Server systems: nl1, de1, fi1 and sg1 ticked (sg1 has only ordinary packages), the bar above the table, the window one click away" data={idle}>
        <OsSection defaultPicked={["nod_nl1", "nod_de1", "nod_fi1", "nod_sg1"]} />
      </Shot>
      <Shot id="os-fleet-nothing-ticked" title="Server systems: nothing ticked yet, one button ticks every node that has something to install" data={idle}>
        <OsSection />
      </Shot>
      <Shot id="os-fleet-dialogs" title="The window before the run (?dialog=fleet|fleet-all|fleet-reboot|fleet-none|single-security|single-all|single-ordinary)" data={idle}>
        <DialogBlock auto={dialog && dialog !== "cancel" ? dialog : null} />
      </Shot>
      <Shot id="os-fleet-running" title="Under way: nl1 (canary) done, de1 installs, fi1 waits for the package lock an automatic update holds (can retry in 11 min), sg1 is queued, msk1 skipped" data={scenes.running}>
        <OsSection />
      </Shot>
      <Shot id="os-fleet-rebooting" title="Later: de1 reboots, fi1 is back and its profiles are checked; the second batch waits" data={scenes.rebooting}>
        <OsSection />
      </Shot>
      <Shot id="os-fleet-paused" title="Paused by the owner: Continue / Cancel, nothing new starts" data={scenes.paused}>
        <OsSection />
      </Shot>
      <Shot id="os-fleet-failed" title="Stopped by de1 (it refused its reboot): Retry, Skip and continue, Close" data={scenes.failed}>
        <OsSection />
      </Shot>
      <Shot id="os-fleet-retried" title="Stopped by de1 again after a retry: Retry is off, only skip or close are left" data={scenes.retried}>
        <OsSection />
      </Shot>
      <Shot id="os-fleet-cancelling" title="Cancelling: de1's job runs to the end, the rest is skipped" data={scenes.cancelling}>
        <OsSection />
      </Shot>
      <Shot id="os-fleet-done" title="Finished: all packages on four nodes, folded to its header" data={scenes.done}>
        <OsSection />
      </Shot>
      <Shot id="os-fleet-cancel" title="The cancel window (?dialog=cancel)" data={scenes.running}>
        <RunBlock data={scenes.running} asking={dialog === "cancel"} />
      </Shot>
      <Shot id="os-security-names" title="de1: Security: 4 opened to the names; libfoo1 and libbar-common are stuck for the automatic updates (a calm note, no alarm). Server tab card below" data={scenes.security}>
        <OsSection defaultOpenNames={["nod_de1"]} />
        <div className="max-w-[452px]">
          <OsCard nodeId="nod_de1" namesOpen />
        </div>
      </Shot>
      <Shot id="os-apt-lock" title="de1: unattended-upgr has been installing packages for 14 min (a quiet line, not a fault)" data={scenes.lock}>
        <OsSection />
      </Shot>
      <Shot id="os-apt-lock-refused" title="de1: the node refused for the whole wait because of the package lock; the run closed itself, nothing changed" data={scenes.lockRefused}>
        <OsSection />
      </Shot>
      <Shot id="os-wait-one" title="de1 alone: waiting for another try (can retry in 8 min), no endless bar" data={scenes.waitOne}>
        <OsSection />
      </Shot>
      <Shot id="os-note-leftover" title="de1: after an automatic update part of the security packages still waits" data={scenes.leftover}>
        <OsSection />
      </Shot>
    </div>
  );
}
