import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { StepUpProvider } from "@/components/step-up";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { OsRebootPolicy, OsRolloutMode, OsRolloutStatus, OsStepState, OsUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { isLang, setLang } from "@/i18n";
import { ops, provisioning, updates } from "@/lib/api";
import { osRows, osUpdatesQuery, type OsRow, type OsUpdates } from "@/lib/os-updates";
import { useOsActions } from "@/lib/os-run";
import { meQuery } from "@/lib/session";
import { OsCard } from "@/screens/node/os-card";
import { ServerTab } from "@/screens/node/server";
import { baseNode, facts, inbound, metrics, Mock, nodeFill } from "@/screens/dev-kit-node";
import { OsRowMenu } from "@/screens/updates/os-actions";
import { OsConfirmDialog, type OsDialogMode } from "@/screens/updates/os-confirm";
import { OsSection } from "@/screens/updates/os";

// Development only, /dev-kit?only=os: the OS update of a node (Updates -> Server operating system, and the node's Server tab)
// in each state, on made-up data: updates waiting, a job running (installing, rebooting), a stopped run with its reason and
// Close, an old agent, an offline node, a finished run, nothing to install; a green node with ordinary packages waiting, a reboot
// that is not possible yet, the check that waits for named profiles, a refusal that closed itself. The calls are answered here and nothing goes to
// the network. ?dialog=install|reboot|warn opens the confirmation window in its own block; ?menu=open shows the ⋯ menu of a
// table row open (an open menu holds the page still, so it is not open by default and comes first); ?lang=ru|fa|zh|tr is the page's.

// ?lang=ru|fa|zh|tr|en switches the page's language (kept in this browser, like the picker)
const wantedLang = new URLSearchParams(location.search).get("lang");
if (isLang(wantedLang)) setLang(wantedLang);

export const now = Math.floor(Date.now() / 1000);
const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

export const base = {
  measuredUnix: now - 60, distroId: "ubuntu", distroVersion: "24.04", pending: 0, security: 0, notable: [], kernelRunning: "6.8.0-147-generic", kernelNewest: "6.8.0-147-generic",
  rebootRequired: false, rebootReasons: [], held: [], apt: "ok", halfConfigured: [], listsUnix: now - 3600, dpkgUnix: now - 7200, autoUpdates: "on", autoReboot: false,
  awgModuleNext: "", container: "", bootIdHash: "", error: "",
};
export const node = (over: Record<string, unknown>) => ({
  nodeId: "nod_de1", name: "de1", connected: true, supported: true, onlineUsers: 0, onPanelServer: false, state: OsUpdateState.UP_TO_DATE, status: { ...base },
  updateCapable: true, updateBlock: "", rebootBlock: "", rebootPossibleUnix: 0, ...over,
});
export const step = (nodeId: string, nodeName: string, over: Record<string, unknown>) => ({
  nodeId, nodeName, state: OsStepState.PASSED, jobId: "osj_1", phase: "", phaseUnix: 0, kernelFrom: "", kernelTo: "", sentUnix: 0, rebootUnix: 0, backUnix: 0, finishedUnix: 0, errorKey: "", params: {}, stage: 0, retryAfterUnix: 0, ...over,
});
export const rollout = (steps: unknown[], over: Record<string, unknown>) => ({
  id: "osr_1", status: OsRolloutStatus.RUNNING, mode: OsRolloutMode.SECURITY, reboot: OsRebootPolicy.IF_REQUIRED, createdUnix: now - 300, finishedUnix: 0, pauseKey: "", pauseParams: {}, steps, batchSize: 1, ...over,
});
export const answer = (nodes: unknown[], run?: unknown): OsUpdates => ({ nowUnix: now, scheduleTimezoneOffsetMinutes: 180, panelHost: { ...base }, nodes, rollout: run }) as never;

const waiting = { ...base, pending: 12, security: 4, kernelNewest: "6.8.0-150-generic", rebootRequired: true, rebootReasons: ["kernel"] };
const de1Waiting = node({ onlineUsers: 5, state: OsUpdateState.REBOOT_REQUIRED, status: waiting });
const warnings = node({
  nodeId: "nod_se1", name: "se1", onlineUsers: 5, state: OsUpdateState.REBOOT_REQUIRED,
  status: { ...waiting, pending: 31, security: 9, autoReboot: true, awgModuleNext: "missing", apt: "half_configured", halfConfigured: ["libssl3", "openssh-server"], held: ["linux-generic"] },
});

const kern = (version: string, over: Record<string, unknown> = {}) => ({
  version, running: false, nextBoot: false, candidate: true, image: true, initrd: true, grubEntry: true, headers: "present", module: "built", packages: "ok", ...over,
});
// a node whose agent sends the kernel table: all in order, and the shape of the incident (no initrd, no GRUB entry, no module build, headers half configured)
const kernelsClean = node({
  state: OsUpdateState.REBOOT_REQUIRED,
  status: { ...waiting, kernels: [kern("6.8.0-150-generic", { nextBoot: true }), kern("6.8.0-147-generic", { running: true }), kern("6.8.0-145-generic", { candidate: false })], awgSource: "ok" },
});
const kernelsBlocked = node({
  nodeId: "nod_nl1", name: "nl1", state: OsUpdateState.REBOOT_REQUIRED,
  status: {
    ...waiting,
    kernels: [
      kern("6.8.0-150-generic", { nextBoot: true, initrd: false, grubEntry: false, headers: "missing", module: "missing", packages: "half_configured" }),
      kern("6.8.0-147-generic", { running: true }),
      kern("6.8.0-145-generic", { candidate: false, module: "unknown" }),
    ],
    rebootBlockers: ["initramfs_missing", "boot_entry_missing", "awg_module_missing", "dpkg_interrupted"],
  },
  rebootBlock: "",
});

const scenes = {
  available: answer([de1Waiting]),
  running: answer(
    [node({ nodeId: "nod_nl1", name: "nl1", state: OsUpdateState.BUSY, status: { ...base, pending: 5, security: 2 }, updateBlock: "node_busy", rebootBlock: "node_busy" })],
    rollout([step("nod_nl1", "nl1", { state: OsStepState.SENT, phase: "installing", phaseUnix: now - 180, sentUnix: now - 200 })], {}),
  ),
  rebooting: answer(
    [node({ nodeId: "nod_nl1", name: "nl1", state: OsUpdateState.BUSY, status: { ...base, pending: 0 }, updateBlock: "rollout_active", rebootBlock: "rollout_active" })],
    rollout([step("nod_nl1", "nl1", { state: OsStepState.REBOOTING, kernelFrom: "6.8.0-147-generic", kernelTo: "6.8.0-150-generic", sentUnix: now - 700, rebootUnix: now - 240 })], {}),
  ),
  failed: answer(
    [node({ nodeId: "nod_fi1", name: "fi1", state: OsUpdateState.REBOOT_REQUIRED, status: waiting, updateBlock: "rollout_active", rebootBlock: "rollout_active" })],
    rollout(
      [step("nod_fi1", "fi1", { state: OsStepState.FAILED, errorKey: "reboot_held", params: { code: "boot_default_custom", upgraded: "4", mode: "security" }, sentUnix: now - 900, finishedUnix: now - 420 })],
      { status: OsRolloutStatus.PAUSED, pauseKey: "reboot_held", pauseParams: { node: "fi1", reason: "reboot_held", code: "boot_default_custom", mode: "security" } },
    ),
  ),
  heldOnly: answer(
    [node({ nodeId: "nod_fi1", name: "fi1", state: OsUpdateState.UP_TO_DATE, updateBlock: "rollout_active", rebootBlock: "rollout_active" })],
    rollout(
      [step("nod_fi1", "fi1", { state: OsStepState.FAILED, errorKey: "reboot_held", params: { code: "boot_entry_missing", mode: "reboot_only" }, sentUnix: now - 900, finishedUnix: now - 420 })],
      { mode: OsRolloutMode.REBOOT_ONLY, reboot: OsRebootPolicy.ALWAYS, status: OsRolloutStatus.PAUSED, pauseKey: "reboot_held", pauseParams: { node: "fi1", reason: "reboot_held", code: "boot_entry_missing", mode: "reboot_only" } },
    ),
  ),
  lost: answer(
    [node({ nodeId: "nod_fi1", name: "fi1", state: OsUpdateState.UPDATES, status: { ...base, pending: 6, security: 2 }, updateBlock: "rollout_active", rebootBlock: "rollout_active" })],
    rollout(
      [step("nod_fi1", "fi1", { state: OsStepState.FAILED, errorKey: "node_lost", params: { upgraded: "2", reboot: "not_asked", mode: "security" }, sentUnix: now - 900, finishedUnix: now - 300 })],
      { reboot: OsRebootPolicy.NEVER, status: OsRolloutStatus.PAUSED, pauseKey: "gate_failed", pauseParams: { node: "fi1", reason: "node_lost", mode: "security" } },
    ),
  ),
  gating: answer(
    [node({ nodeId: "nod_nl1", name: "nl1", state: OsUpdateState.BUSY, status: { ...base, pending: 6, security: 2 }, updateBlock: "rollout_active", rebootBlock: "rollout_active" })],
    rollout([step("nod_nl1", "nl1", { state: OsStepState.GATING, sentUnix: now - 400, backUnix: now - 40, params: { mode: "security" } })], { reboot: OsRebootPolicy.NEVER }),
  ),
  refused: answer(
    [node({ nodeId: "nod_nl1", name: "nl1", state: OsUpdateState.UPDATES, status: { ...base, pending: 6, security: 2 }, onlineUsers: 3 })],
    rollout(
      [step("nod_nl1", "nl1", { state: OsStepState.FAILED, errorKey: "agent_refused", params: { code: "disk_full", mode: "security" }, sentUnix: now - 100, finishedUnix: now - 90 })],
      { status: OsRolloutStatus.FAILED, finishedUnix: now - 90 },
    ),
  ),
  notBack: answer(
    [node({ nodeId: "nod_fi1", name: "fi1", state: OsUpdateState.REBOOT_REQUIRED, status: waiting, updateBlock: "rollout_active", rebootBlock: "rollout_active" })],
    rollout(
      [step("nod_fi1", "fi1", { state: OsStepState.FAILED, errorKey: "not_back", sentUnix: now - 2400, rebootUnix: now - 1800, finishedUnix: now - 900 })],
      { status: OsRolloutStatus.PAUSED, pauseKey: "not_back", pauseParams: { node: "fi1" } },
    ),
  ),
  // no security update and no reboot due, but 18 ordinary packages wait: green, the packages are a quiet note beside the count
  allGood: answer([
    node({ name: "de1", onlineUsers: 4, updateBlock: "nothing_to_do", status: { ...base, pending: 18 } }),
    node({ nodeId: "nod_nl1", name: "nl1", onlineUsers: 2, updateBlock: "nothing_to_do" }),
    node({ nodeId: "nod_fi1", name: "fi1", onlineUsers: 0, state: OsUpdateState.REBOOT_REQUIRED, status: waiting }),
  ]),
  // rebooted a short while ago: the Reboot action is off until the node and the panel's own limit allow it
  rebootWait: answer([
    node({ name: "de1", onlineUsers: 5, updateBlock: "nothing_to_do", status: { ...base, pending: 7 }, rebootBlock: "reboot_wait", rebootPossibleUnix: now + 11 * 60 + 20 }),
  ]),
  // the check after the reboot waits for two profiles; 3 minutes of up to 8
  gateWaiting: answer(
    [node({ nodeId: "nod_nl1", name: "nl1", state: OsUpdateState.BUSY, status: { ...base, pending: 6, security: 2 }, updateBlock: "rollout_active", rebootBlock: "rollout_active" })],
    rollout(
      [
        step("nod_nl1", "nl1", {
          state: OsStepState.GATING, kernelFrom: "6.8.0-147-generic", kernelTo: "6.8.0-150-generic", sentUnix: now - 700, rebootUnix: now - 400, backUnix: now - 190,
          params: { mode: "security", gate_for: "profiles", gate_names: JSON.stringify(["Berlin main", "Berlin backup"]), gate_max_s: "480" },
        }),
      ],
      {},
    ),
  ),
  // the node asked to wait (a reboot 20 minutes after the last one): the run closed itself, one neutral note stays on the node
  tryLater: answer(
    [node({ name: "de1", onlineUsers: 5, updateBlock: "nothing_to_do", status: { ...base, pending: 7 } })],
    rollout(
      [step("nod_de1", "de1", { state: OsStepState.SKIPPED, errorKey: "try_later", params: { code: "reboot_limit", mode: "reboot_only" }, sentUnix: now - 100, finishedUnix: now - 90 })],
      { mode: OsRolloutMode.REBOOT_ONLY, reboot: OsRebootPolicy.ALWAYS, status: OsRolloutStatus.DONE, finishedUnix: now - 90 },
    ),
  ),
  kernelsClean: answer([kernelsClean]),
  kernelsBlocked: answer([kernelsBlocked, { ...kernelsClean, nodeId: "nod_de1", name: "de1" }]),
  oldAgent: answer([node({ nodeId: "nod_msk1", name: "msk1", supported: false, state: OsUpdateState.UNSUPPORTED, status: undefined, updateCapable: false, updateBlock: "agent_old", rebootBlock: "agent_old" })]),
  offline: answer([node({ nodeId: "nod_se1", name: "se1", connected: false, state: OsUpdateState.OFFLINE, status: undefined, updateBlock: "offline", rebootBlock: "offline" })]),
  done: answer(
    [node({ status: { ...base, kernelRunning: "6.8.0-150-generic", kernelNewest: "6.8.0-150-generic" } })],
    rollout(
      [step("nod_de1", "de1", { kernelFrom: "6.8.0-147-generic", kernelTo: "6.8.0-150-generic", params: { upgraded: "6", reboot: "done", kernel: "6.8.0-150-generic" }, sentUnix: now - 1500, finishedUnix: now - 600 })],
      { status: OsRolloutStatus.DONE, finishedUnix: now - 600, createdUnix: now - 1600 },
    ),
  ),
  nothing: answer([node({ nodeId: "nod_nl1", name: "nl1", onlineUsers: 2, updateBlock: "nothing_to_do" })]),
  warnings: answer([warnings]),
  menu: answer([node({ nodeId: "nod_nl1", name: "nl1", onlineUsers: 2, state: OsUpdateState.UPDATES, status: { ...base, pending: 6, security: 2 }, updateBlock: "node_busy", rebootBlock: "node_busy" })]),
} satisfies Record<string, OsUpdates>;

function fakeCalls() {
  const api = updates as unknown as Record<string, unknown>;
  api.getOsUpdates = () => new Promise(() => {}); // the seeded answer is the whole story
  api.startOsRollout = async () => {
    await wait(700);
    return { rollout: scenes.running.rollout };
  };
  api.cancelOsRollout = async () => {
    await wait(500);
    return { rollout: scenes.failed.rollout };
  };
  api.refreshOsUpdates = async ({ nodeIds }: { nodeIds: string[] }) => {
    await wait(500);
    return { results: nodeIds.map((nodeId) => ({ nodeId, outcome: "started", code: "" })) };
  };
}
fakeCalls();

export const owner = { admin: { id: "adm_1", login: "owner", role: Role.OWNER }, edition: "vps", unavailable: [] };

export function Shot({ id, title, data, children }: { id: string; title: string; data: OsUpdates; children: ReactNode }) {
  const [qc] = useState(() => {
    const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false } } });
    client.setQueryData(meQuery.queryKey, owner as never);
    client.setQueryData(osUpdatesQuery.queryKey, data);
    return client;
  });
  return (
    <section data-shot={id} className="flex w-full max-w-[1100px] flex-col gap-3">
      <SectionLabel>{title}</SectionLabel>
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <StepUpProvider>
            <div className="flex flex-col gap-3.5">{children}</div>
          </StepUpProvider>
        </ToastProvider>
      </QueryClientProvider>
    </section>
  );
}

/** The card of the first node and the table of the whole answer, as the two places show them. */
function Both({ data }: { data: OsUpdates }) {
  const first = data.nodes[0]!;
  return (
    <>
      <div className="max-w-[452px]">
        <OsCard nodeId={first.nodeId} />
      </div>
      <OsSection />
    </>
  );
}

/** The confirmation window of a node, closed until a button (or ?dialog=) opens it. */
function ConfirmBlock({ data, auto }: { data: OsUpdates; auto: OsDialogMode | null }) {
  const [mode, setMode] = useState<OsDialogMode | null>(auto);
  const actions = useOsActions();
  const row: OsRow = osRows(data)[1]!;
  return (
    <>
      <div className="flex flex-wrap gap-2">
        <Button onClick={() => setMode("install")}>Open install window</Button>
        <Button onClick={() => setMode("reboot")}>Open reboot window</Button>
      </div>
      {mode && <OsConfirmDialog row={row} mode={mode} actions={actions} onClose={() => setMode(null)} />}
    </>
  );
}

export function OsKit() {
  const params = new URLSearchParams(location.search);
  const dialog = params.get("dialog");
  const auto: OsDialogMode | null = dialog === "reboot" ? "reboot" : dialog === "install" || dialog === "warn" ? "install" : null;
  const confirmData = dialog === "warn" ? scenes.warnings : scenes.available;
  return (
    <div className="flex flex-col gap-8">
      {params.get("menu") === "open" && (
        <Shot id="os-menu-open" title="The ⋯ menu of a table row (?menu=open): nl1 is busy, so installing and rebooting are off with the reason" data={scenes.menu}>
          <div className="flex min-h-72 items-start justify-between gap-3 rounded-card border border-line bg-surface p-3.5">
            <b className="text-sm">nl1</b>
            <OsRowMenu row={osRows(scenes.menu)[1]!} defaultOpen />
          </div>
        </Shot>
      )}
      <Shot id="os-available" title="de1: 12 packages (4 security) and a new kernel wait, 5 people online" data={scenes.available}>
        <Both data={scenes.available} />
      </Shot>
      <Shot id="os-running" title="nl1: the job is installing, 3 minutes in" data={scenes.running}>
        <Both data={scenes.running} />
      </Shot>
      <Shot id="os-rebooting" title="nl1: rebooting, waiting for it to come back" data={scenes.rebooting}>
        <Both data={scenes.rebooting} />
      </Shot>
      <Shot id="os-failed" title="fi1: the run stopped, the node refused to reboot (Close)" data={scenes.failed}>
        <Both data={scenes.failed} />
      </Shot>
      <Shot id="os-heldonly" title="fi1: a reboot alone, the node refused (nothing was installed)" data={scenes.heldOnly}>
        <Both data={scenes.heldOnly} />
      </Shot>
      <Shot id="os-notback" title="fi1: did not come back after the reboot" data={scenes.notBack}>
        <Both data={scenes.notBack} />
      </Shot>
      <Shot id="os-lost" title="fi1: dropped off the network during the check, no reboot in the run" data={scenes.lost}>
        <Both data={scenes.lost} />
      </Shot>
      <Shot id="os-gating" title="nl1: updates installed without a reboot, the panel checks the profiles" data={scenes.gating}>
        <Both data={scenes.gating} />
      </Shot>
      <Shot id="os-allgood" title="Security is in order: de1 has 18 ordinary packages waiting and is still green (a quiet note), nl1 has none, fi1 needs a reboot" data={scenes.allGood}>
        <Both data={scenes.allGood} />
      </Shot>
      <Shot id="os-reboot-wait" title="de1: rebooted a short while ago, Reboot is off with &quot;Reboot possible in 12 min&quot;, installing stays on" data={scenes.rebootWait}>
        <Both data={scenes.rebootWait} />
      </Shot>
      <Shot id="os-gate-waiting" title="nl1: back after the reboot, the check waits for two named profiles (3 min of up to 8)" data={scenes.gateWaiting}>
        <Both data={scenes.gateWaiting} />
      </Shot>
      <Shot id="os-try-later" title="de1: the node asked to wait, the run closed itself: a neutral note, no Close, nothing held up" data={scenes.tryLater}>
        <Both data={scenes.tryLater} />
      </Shot>
      <Shot id="os-refused" title="nl1: the node refused (disk full), nothing happened on the server (no Close, no alert, nothing held up)" data={scenes.refused}>
        <Both data={scenes.refused} />
      </Shot>
      <Shot id="os-kernels-clean" title="de1: the kernel table, all in order (Next boot: ready; open it with the title)" data={scenes.kernelsClean}>
        <Both data={scenes.kernelsClean} />
      </Shot>
      <Shot id="os-kernels-blocked" title="nl1: the next kernel has no initrd and no GRUB entry: a reboot would be refused, Reboot is off with the reason" data={scenes.kernelsBlocked}>
        <Both data={scenes.kernelsBlocked} />
      </Shot>
      <Shot id="os-oldagent" title="msk1: the agent is too old to run updates" data={scenes.oldAgent}>
        <Both data={scenes.oldAgent} />
      </Shot>
      <Shot id="os-offline" title="se1: offline" data={scenes.offline}>
        <Both data={scenes.offline} />
      </Shot>
      <Shot id="os-done" title="de1: finished a while ago" data={scenes.done}>
        <Both data={scenes.done} />
      </Shot>
      <Shot id="os-nothing" title="nl1: nothing to install, a reboot is still possible" data={scenes.nothing}>
        <Both data={scenes.nothing} />
      </Shot>
      <Shot id="os-confirm-block" title={`The confirmation window (${dialog === "warn" ? "se1 with every warning" : "de1"}); ?dialog=install|reboot|warn opens it`} data={confirmData}>
        <ConfirmBlock data={confirmData} auto={auto} />
      </Shot>
    </div>
  );
}

// /dev-kit?only=os-page: the node's real Server tab (the component of the node page) for de1, with updates waiting, so the Operating
// system card is seen in its place in the right column, next to the cards around it. Two columns from xl, as the page has them.
const sshAccess = {
  nodeId: "nod_de1", nodeName: "de1", host: "203.0.113.7", port: 22, username: "root", fingerprint: "SHA256:Vq3n0cX9Lr2bT8kM1pZs6YwE4hJ7uGdA5fNiQoRlKe0",
  configuredUnix: now - 86400 * 12, passwordSaved: true, passwordLogin: true, panelKeyFingerprint: "",
};

/** The calls of the other cards of the tab, answered here (the first time the scene is drawn). */
function fakePageCalls() {
  const api = provisioning as unknown as Record<string, unknown>;
  api.listNodeServerAccess = async () => ({ access: [sshAccess] });
  api.listNodeServerKeys = async () => ({ keys: [], otherLines: 0 });
  (ops as unknown as Record<string, unknown>).listDebugCaptures = async () => ({ captures: [] });
}

export function OsPageKit() {
  const [ready] = useState(() => {
    fakePageCalls();
    return true;
  });
  if (!ready) return null;
  const fill: [readonly unknown[], unknown][] = [[meQuery.queryKey, owner], [osUpdatesQuery.queryKey, scenes.available], ...nodeFill("nod_de1")];
  const data = { node: baseNode({ bandwidthMbps: 100 }), inbounds: [inbound({})], onlineUsers: [], topToday: [], metrics, facts, notes: "" };
  return (
    <section data-shot="os-node-page" className="relative left-1/2 flex w-[min(64rem,calc(100vw-2rem))] -translate-x-1/2 flex-col gap-3">
      <SectionLabel>de1: the Server tab with the Operating system card (12 packages, 4 security, a new kernel, 5 people online)</SectionLabel>
      <Mock fill={fill}>
        <ToastProvider>
          <StepUpProvider>
            <ServerTab data={data as never} />
          </StepUpProvider>
        </ToastProvider>
      </Mock>
    </section>
  );
}
