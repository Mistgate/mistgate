import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { OsRebootPolicy, OsRolloutMode, OsRolloutStatus, OsStepState, OsUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { setLang } from "@/i18n";
import { osRows, osUpdatesQuery, type OsRow, type OsUpdates } from "@/lib/os-updates";
import { meQuery } from "@/lib/session";
import { OsCard } from "@/screens/node/os-card";
import { OsNodeActions } from "./os-actions";

// The OS update of one node: the buttons and why they are off, the window before it starts (what happens, the choice, the
// step-up), the run as it goes, and a stopped run with its Close.

const updates = vi.hoisted(() => ({ getOsUpdates: vi.fn(), startOsRollout: vi.fn(), cancelOsRollout: vi.fn(), refreshOsUpdates: vi.fn() }));
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), updates }));
const guard = vi.hoisted(() => vi.fn((call: () => Promise<unknown>) => call()));
vi.mock("@/components/step-up", () => ({ useStepUp: () => guard, isStepUpCancelled: () => false }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className, to }: { children?: ReactNode; className?: string; to?: string }) => (
    <a className={className} href={to}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  setLang("en");
  updates.startOsRollout.mockResolvedValue({});
  updates.cancelOsRollout.mockResolvedValue({});
  updates.refreshOsUpdates.mockResolvedValue({ results: [{ nodeId: "nod_de1", outcome: "started", code: "" }] });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [...Object.values(updates), guard]) m.mockReset();
  guard.mockImplementation((call: () => Promise<unknown>) => call());
  setLang("en");
});

const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const NOW = Math.floor(Date.now() / 1000);
const base = {
  measuredUnix: NOW - 60, distroId: "ubuntu", distroVersion: "24.04", pending: 0, security: 0, notable: [], kernelRunning: "6.8.0-147-generic", kernelNewest: "6.8.0-147-generic",
  rebootRequired: false, rebootReasons: [], held: [], apt: "ok", halfConfigured: [], listsUnix: NOW - 3600, dpkgUnix: NOW - 7200, autoUpdates: "on", autoReboot: false, awgModuleNext: "", container: "", bootIdHash: "", error: "",
};
const waiting = { ...base, pending: 12, security: 4, kernelNewest: "6.8.0-150-generic", rebootRequired: true, rebootReasons: ["kernel"] };
const node = (over: Record<string, unknown> = {}) => ({
  nodeId: "nod_de1", name: "de1", connected: true, supported: true, onlineUsers: 5, onPanelServer: false, state: OsUpdateState.REBOOT_REQUIRED, status: waiting, updateCapable: true, updateBlock: "", rebootBlock: "", ...over,
});
const step = (over: Record<string, unknown> = {}) => ({
  nodeId: "nod_de1", nodeName: "de1", state: OsStepState.SENT, jobId: "osj_1", phase: "", phaseUnix: 0, kernelFrom: "", kernelTo: "", sentUnix: NOW - 200, rebootUnix: 0, backUnix: 0, finishedUnix: 0, errorKey: "", params: {}, ...over,
});
const rollout = (steps: unknown[], over: Record<string, unknown> = {}) => ({
  id: "osr_1", status: OsRolloutStatus.RUNNING, mode: OsRolloutMode.SECURITY, reboot: OsRebootPolicy.IF_REQUIRED, createdUnix: NOW - 300, finishedUnix: 0, pauseKey: "", pauseParams: {}, steps, ...over,
});
const data = (nodes: unknown[], run?: unknown): OsUpdates => ({ nowUnix: NOW, scheduleTimezoneOffsetMinutes: 0, panelHost: base, nodes, rollout: run }) as never;
const rowOf = (d: OsUpdates): OsRow => osRows(d).find((r) => r.nodeId === "nod_de1")!;

async function mount(ui: ReactNode, { owner = true, edition = "vps", unavailable = [] as string[], seed }: { owner?: boolean; edition?: string; unavailable?: string[]; seed?: OsUpdates } = {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", login: "owner", role: owner ? Role.OWNER : Role.READONLY }, edition, unavailable } as never);
  if (seed) qc.setQueryData(osUpdatesQuery.queryKey, seed);
  await act(async () => root!.render(<QueryClientProvider client={qc}><ToastProvider>{ui}</ToastProvider></QueryClientProvider>));
  for (let i = 0; i < 3; i++) await settle();
}
const buttons = () => [...document.querySelectorAll<HTMLButtonElement>("button")];
const button = (label: string, within: ParentNode = document) => [...within.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);
const disabled = (b: HTMLButtonElement | undefined) => b?.hasAttribute("data-disabled") || b?.disabled;
const click = async (el: Element | null | undefined) => {
  await act(async () => (el as HTMLElement).click());
  for (let i = 0; i < 3; i++) await settle();
};
const dialog = () => document.querySelector<HTMLElement>("[data-shot=os-confirm]");
const text = () => document.body.textContent ?? "";
const actions = (d: OsUpdates, compact = false) => <OsNodeActions row={rowOf(d)} compact={compact} />;

describe("the buttons of a node", () => {
  it("are on for a node with updates, and say nothing under them", async () => {
    await mount(actions(data([node()])));
    for (const label of ["Install updates…", "Reboot…", "Check for updates"]) expect(disabled(button(label)), label).toBeFalsy();
    expect(document.querySelector("[data-shot=os-node-actions]")).not.toBeNull();
    expect(text()).not.toContain("is offline");
  });

  for (const key of ["offline", "agent_old", "unsupported", "panel_host", "node_busy", "no_status", "rollout_active", "agent_rollout", "panel_busy", "awg_build"]) {
    it(`are off with the reason under them: ${key}`, async () => {
      await mount(actions(data([node({ updateBlock: key, rebootBlock: key })])));
      expect(disabled(button("Install updates…")), "install").toBe(true);
      expect(disabled(button("Reboot…")), "reboot").toBe(true);
      const why = document.querySelector("[data-shot=os-node-actions] p")?.textContent ?? "";
      expect(why.length, key).toBeGreaterThan(10);
      expect(why).not.toContain("os.block"); // words, not the key
    });
  }

  it("point an old agent to the Updates page", async () => {
    await mount(actions(data([node({ updateBlock: "agent_old", rebootBlock: "agent_old", updateCapable: false })])));
    expect(text()).toContain("too old to run updates");
    const link = document.querySelector<HTMLAnchorElement>("a[href='/updates']");
    expect(link?.textContent).toBe("Open Updates");
  });

  it("offer a reboot when there is nothing to install", async () => {
    await mount(actions(data([node({ updateBlock: "nothing_to_do", status: base, state: OsUpdateState.UP_TO_DATE })])));
    expect(disabled(button("Install updates…"))).toBe(true);
    expect(disabled(button("Reboot…"))).toBeFalsy();
    expect(text()).toContain("There are no security updates to install. You can reboot the node or check for updates.");
  });

  it("stay off while any run is open, even when the server has no objection", async () => {
    await mount(actions(data([node()], rollout([step({ nodeId: "nod_other", nodeName: "nl1", state: OsStepState.FAILED, errorKey: "job_failed" })], { status: OsRolloutStatus.PAUSED }))));
    expect(disabled(button("Install updates…"))).toBe(true);
    expect(disabled(button("Reboot…"))).toBe(true);
    expect(text()).toContain("Another OS update is running or waits to be closed.");
  });

  it("are absent for someone who is not the owner, and so is the whole block when there is nothing to report", async () => {
    await mount(actions(data([node()])), { owner: false });
    expect(document.querySelector("[data-shot=os-node-actions]")).toBeNull();
    expect(buttons()).toHaveLength(0);
  });

  it("show a run to someone who is not the owner, without the buttons", async () => {
    await mount(actions(data([node()], rollout([step({ phase: "installing" })]))), { owner: false });
    expect(text()).toContain("Installing updates…");
    expect(buttons()).toHaveLength(0);
  });

  it("are not drawn for the panel's own server", async () => {
    await mount(<OsNodeActions row={osRows(data([node()]))[0]!} />);
    expect(host!.textContent).toBe("");
  });

  it("check for updates without a window, and say how it went", async () => {
    await mount(actions(data([node()])));
    await click(button("Check for updates"));
    expect(updates.refreshOsUpdates).toHaveBeenCalledWith({ nodeIds: ["nod_de1"] });
    expect(guard).not.toHaveBeenCalled(); // reading the lists needs no step-up
    expect(text()).toContain("de1 is checking for updates");
    updates.refreshOsUpdates.mockResolvedValue({ results: [{ nodeId: "nod_de1", outcome: "recent", code: "" }] });
    await click(button("Check for updates"));
    expect(text()).toContain("refreshed a moment ago");
  });
});

describe("the window before an update", () => {
  it("says what will happen: the packages, the kernel, the reboot, who is online", async () => {
    await mount(actions(data([node()])));
    await click(button("Install updates…"));
    const d = dialog()!;
    expect(d).not.toBeNull();
    const s = d.textContent!;
    expect(s).toContain("Install security updates on de1");
    expect(s).toContain("4 security updates will be installed");
    expect(s).toContain("8 other waiting updates are left for later");
    expect(s).toContain("Kernel 6.8.0-150-generic is already installed, the node runs 6.8.0-147-generic");
    expect(s).toContain("already needs a reboot to start kernel 6.8.0-150-generic");
    expect(s).toContain("5 people are online on de1 now");
    expect(s).toContain("disconnected for about two minutes");
    expect(s).toContain("Old kernels stay installed.");
    expect(s).toContain("Reboot if required (recommended)");
    expect(s).toContain("Do not reboot");
    expect(s).not.toMatch(/anyway/i);
  });

  it("warns of what would stop it: automatic reboot, a missing AmneziaWG module, half-installed packages, held ones", async () => {
    const status = { ...waiting, autoReboot: true, awgModuleNext: "missing", apt: "half_configured", halfConfigured: ["libssl3"], held: ["linux-generic"] };
    await mount(actions(data([node({ status })])));
    await click(button("Install updates…"));
    const s = dialog()!.textContent!;
    expect(s).toContain("Automatic-Reboot in unattended-upgrades");
    expect(s).toContain("The AmneziaWG module is not built for the new kernel");
    expect(s).toContain("Interrupted installs: libssl3");
    expect(s).toContain("Held back packages stay as they are: linux-generic.");
  });

  it("starts it after the step-up, asking to reboot if required, as chosen by default", async () => {
    await mount(actions(data([node()])));
    await click(button("Install updates…"));
    await click(button("Install updates", dialog()!));
    expect(guard).toHaveBeenCalledTimes(1);
    expect(updates.startOsRollout).toHaveBeenCalledWith({ nodeIds: ["nod_de1"], mode: OsRolloutMode.SECURITY, reboot: OsRebootPolicy.IF_REQUIRED });
    expect(dialog()).toBeNull(); // went through: the window closes
    expect(text()).toContain("The update of de1 has started.");
  });

  it("starts it without a reboot when that is chosen, and hides the warning about people when nobody reboots", async () => {
    await mount(actions(data([node()])));
    await click(button("Install updates…"));
    const never = [...dialog()!.querySelectorAll<HTMLElement>("[role=radio]")].find((r) => r.textContent?.includes("Do not reboot"))!;
    await click(never);
    expect(dialog()!.textContent).not.toContain("disconnected for about two minutes");
    await click(button("Install updates", dialog()!));
    expect(updates.startOsRollout).toHaveBeenCalledWith({ nodeIds: ["nod_de1"], mode: OsRolloutMode.SECURITY, reboot: OsRebootPolicy.NEVER });
  });

  it("opens the reboot window with what it does to people as its first line, large, and a red button", async () => {
    await mount(actions(data([node()])));
    await click(button("Reboot…"));
    const d = dialog()!;
    const lead = d.querySelector("p")!;
    expect(lead.textContent).toBe("5 people online on de1 will be disconnected for about two minutes");
    expect(lead.className).toContain("font-extrabold");
    expect(lead.className).not.toContain("text-muted");
    // the first thing in the body, before any warning or list
    expect(d.querySelector("p, ul, [role=status]")).toBe(lead);
    // said once: the list has no second line about people
    expect(d.textContent!.match(/online/g)).toHaveLength(1);
    const go = button("Reboot", d)!;
    expect(go.className).toContain("bg-danger-soft");
    expect(go.className).not.toContain("bg-accent");
    // installing keeps the usual button
    await click(button("Cancel", d));
    await click(button("Install updates…"));
    expect(button("Install updates", dialog()!)!.className).toContain("bg-accent");
  });

  it("says nobody is online in the lead of the reboot window when it is so", async () => {
    await mount(actions(data([node({ onlineUsers: 0 })])));
    await click(button("Reboot…"));
    expect(dialog()!.querySelector("p")!.textContent).toBe("Nobody is online on de1 now.");
  });

  it("takes the plural of the lead in Russian", async () => {
    setLang("ru");
    const lead = async (n: number) => {
      await mount(actions(data([node({ onlineUsers: n })])));
      await click(button("Перезагрузить…"));
      const s = dialog()!.querySelector("p")!.textContent;
      act(() => root?.unmount());
      host?.remove();
      return s;
    };
    expect(await lead(1)).toBe("1 человек онлайн на de1 отключится примерно на две минуты");
    expect(await lead(3)).toBe("3 человека онлайн на de1 отключатся примерно на две минуты");
    expect(await lead(5)).toBe("5 человек онлайн на de1 отключатся примерно на две минуты");
  });

  it("warns of the AmneziaWG module without promising a build fixes it", async () => {
    const status = { ...waiting, awgModuleNext: "missing" };
    await mount(actions(data([node({ status })])));
    await click(button("Install updates…"));
    const s = dialog()!.textContent!;
    expect(s).toContain("The AmneziaWG module is not built for the new kernel. The node refuses to reboot until it is: people on AmneziaWG would lose their connection.");
    expect(s).not.toContain("Build the module");
  });

  it("reboots with its own window: no installing, the node checks it is safe, and no way to force it", async () => {
    await mount(actions(data([node()])));
    await click(button("Reboot…"));
    const d = dialog()!;
    expect(d.textContent).toContain("Reboot de1");
    expect(d.textContent).toContain("checks by itself that a reboot is safe, and refuses if it is not");
    expect(d.textContent).not.toContain("security updates will be installed");
    expect(d.querySelector("[role=radio]")).toBeNull(); // nothing to choose
    expect(d.textContent).not.toMatch(/anyway/i);
    await click(button("Reboot", d));
    expect(updates.startOsRollout).toHaveBeenCalledWith({ nodeIds: ["nod_de1"], mode: OsRolloutMode.REBOOT_ONLY, reboot: OsRebootPolicy.ALWAYS });
  });

  it("closes without doing anything on Cancel, and stays open under the toast when the panel refuses", async () => {
    await mount(actions(data([node()])));
    await click(button("Install updates…"));
    await click(button("Cancel", dialog()!));
    expect(dialog()).toBeNull();
    expect(updates.startOsRollout).not.toHaveBeenCalled();

    const { ConnectError, Code } = await import("@connectrpc/connect");
    updates.startOsRollout.mockRejectedValue(new ConnectError("os_blocked: reason=node_busy", Code.FailedPrecondition));
    await click(button("Install updates…"));
    await click(button("Install updates", dialog()!));
    expect(dialog()).not.toBeNull();
    expect(text()).toContain("The OS update cannot start: Another job is running on the server, for example a check for updates you have just started.");
  });
});

describe("the run as it goes", () => {
  it("shows the phase, the time and an unending bar, and no buttons", async () => {
    await mount(actions(data([node()], rollout([step({ phase: "installing", phaseUnix: NOW - 180 })]))));
    expect(text()).toContain("Installing updates…");
    expect(text()).toContain("3 min so far");
    expect(text()).toContain("Installing security updates");
    expect(document.querySelector("[role=progressbar]")).not.toBeNull();
    expect(buttons()).toHaveLength(0);
  });

  it("says the node is rebooting and how long to wait", async () => {
    await mount(actions(data([node()], rollout([step({ state: OsStepState.REBOOTING, rebootUnix: NOW - 240 })]))));
    expect(text()).toContain("Rebooting… waiting for the node to come back, up to 15 minutes");
    expect(text()).toContain("4 min so far");
  });

  it("says the node is back and its profiles are being checked", async () => {
    await mount(actions(data([node()], rollout([step({ state: OsStepState.GATING, rebootUnix: NOW - 200, backUnix: NOW - 30 })]))));
    expect(text()).toContain("Back, checking profiles…");
  });

  it("does not say \"back\" for a run without a reboot", async () => {
    await mount(actions(data([node()], rollout([step({ state: OsStepState.GATING, rebootUnix: 0, backUnix: NOW - 30 })], { reboot: OsRebootPolicy.NEVER }))));
    expect(text()).toContain("Checking profiles…");
    expect(text()).not.toContain("Back");
  });

  it("shows only the run under the row of the table, and the buttons stay with the card", async () => {
    await mount(actions(data([node()], rollout([step({ phase: "installing" })])), true));
    expect(text()).toContain("Installing updates…");
    expect(document.querySelector("[role=progressbar]")).not.toBeNull();
    const idle = data([node()]);
    await act(async () => root!.render(<QueryClientProvider client={new QueryClient()}><ToastProvider>{actions(idle, true)}</ToastProvider></QueryClientProvider>));
    expect(document.querySelector("[data-shot=os-node-actions]")).toBeNull();
    expect(buttons()).toHaveLength(0);
  });

  it("offers to cancel a step that has not started yet", async () => {
    await mount(actions(data([node()], rollout([step({ state: OsStepState.PENDING })]))));
    await click(button("Cancel the update"));
    expect(updates.cancelOsRollout).toHaveBeenCalledWith({ rolloutId: "osr_1" });
  });

  it("sums up a finished run with a green pill", async () => {
    const done = step({ state: OsStepState.PASSED, kernelFrom: "6.8.0-147-generic", kernelTo: "6.8.0-150-generic", params: { upgraded: "6", reboot: "done" }, finishedUnix: NOW - 600 });
    await mount(actions(data([node()], rollout([done], { status: OsRolloutStatus.DONE, finishedUnix: NOW - 600 }))));
    const pill = document.querySelector(".pill");
    expect(pill?.textContent).toBe("Done");
    expect(pill?.className).toContain("tone-ok");
    expect(text()).toContain("kernel 6.8.0-147-generic → 6.8.0-150-generic · 6 packages installed · rebooted");
    expect(button("Install updates…")).toBeTruthy(); // the node may be updated again
  });

  it("says a reboot was not needed", async () => {
    const done = step({ state: OsStepState.PASSED, params: { upgraded: "2", reboot: "not_required" }, finishedUnix: NOW - 60 });
    await mount(actions(data([node()], rollout([done], { status: OsRolloutStatus.DONE, finishedUnix: NOW - 60 }))));
    expect(text()).toContain("no reboot needed");
  });

  it("forgets a run that ended more than a day ago", async () => {
    const old = step({ state: OsStepState.PASSED, finishedUnix: NOW - 2 * 86_400 });
    await mount(actions(data([node()], rollout([old], { status: OsRolloutStatus.DONE, finishedUnix: NOW - 2 * 86_400 }))));
    expect(document.querySelector(".pill")).toBeNull();
  });
});

describe("a stopped run", () => {
  const failed = (over: Record<string, unknown> = {}, ro: Record<string, unknown> = {}) =>
    data(
      [node({ updateBlock: "rollout_active", rebootBlock: "rollout_active" })],
      rollout([step({ state: OsStepState.FAILED, errorKey: "reboot_held", params: { code: "boot_default_custom" }, finishedUnix: NOW - 420, ...over })], { status: OsRolloutStatus.PAUSED, pauseKey: "reboot_held", pauseParams: { node: "de1" }, ...ro }),
    );

  it("says what stopped, why in the node's words, and offers Close", async () => {
    await mount(actions(failed()));
    const pill = document.querySelector(".pill");
    expect(pill?.textContent).toContain("Stopped");
    expect(pill?.className).toContain("tone-bad");
    const alert = document.querySelector("[role=alert]")!;
    expect(alert.textContent).toContain("de1: updates are installed, but the server was not rebooted.");
    expect(alert.textContent).toContain("set to start a chosen or saved entry");
    expect(alert.textContent).toContain("GRUB_DEFAULT=0");
    expect(alert.textContent).toContain("Closing the update removes the alert and frees the next OS update");
    expect(alert.textContent).toContain("A stopped update does not hold up agent or panel updates.");
    // the banner already says "installed, not rebooted": the step's own headline does not repeat it
    expect(alert.textContent).not.toContain("refused to reboot");
    expect(button("Close")).toBeTruthy();
  });

  it("says why for a node that did not come back, with the way out", async () => {
    await mount(actions(failed({ errorKey: "not_back", params: {} }, { pauseKey: "not_back" })));
    expect(document.querySelector("[role=alert]")!.textContent).toContain("choose the previous kernel in the GRUB menu");
  });

  it("names the package a failed install stopped on", async () => {
    await mount(actions(failed({ errorKey: "job_failed", params: { code: "install_failed", package: "nginx" } }, { pauseKey: "step_failed" })));
    const s = document.querySelector("[role=alert]")!.textContent!;
    expect(s).toContain("The update of de1 stopped.");
    expect(s).toContain("The update failed on the node.");
    expect(s).toContain("failed on nginx");
  });

  it("does not claim updates were installed after a reboot alone that the node refused", async () => {
    const only = failed({ params: { code: "too_soon", mode: "reboot_only" } }, { pauseParams: { node: "de1", reason: "reboot_held", code: "too_soon", mode: "reboot_only" }, mode: OsRolloutMode.REBOOT_ONLY, reboot: OsRebootPolicy.ALWAYS });
    await mount(actions(only));
    const alert = document.querySelector("[role=alert]")!;
    expect(alert.textContent).toContain("de1: the server was not rebooted, the node refused.");
    expect(alert.textContent).toContain("restarted less than 10 minutes ago");
    expect(alert.textContent).not.toMatch(/installed/);
    expect(button("Close")).toBeTruthy();
  });

  it("reads the mode from the step when the pause does not carry it", async () => {
    await mount(actions(failed({ params: { code: "too_soon", mode: "reboot_only" } }, { pauseParams: { node: "de1" } })));
    expect(document.querySelector("[role=alert]")!.textContent).toContain("de1: the server was not rebooted, the node refused.");
  });

  it("says in Russian that a reboot alone was refused, not that updates are installed", async () => {
    setLang("ru");
    await mount(actions(failed({ params: { code: "too_soon", mode: "reboot_only" } }, { pauseParams: { node: "de1", reason: "reboot_held", mode: "reboot_only" } })));
    const s = document.querySelector("[role=alert]")!.textContent!;
    expect(s).toContain("de1: сервер не перезагружен, нода отказалась.");
    expect(s).not.toContain("обновления установлены");
  });

  it("says a node dropped off the network during the check, without claiming it came back", async () => {
    await mount(actions(failed({ errorKey: "node_lost", params: { upgraded: "2", mode: "security" } }, { pauseKey: "gate_failed", pauseParams: { node: "de1", reason: "node_lost", mode: "security" } })));
    const alert = document.querySelector("[role=alert]")!;
    expect(alert.textContent).toContain("de1 dropped off the network while the panel was checking it after the update.");
    expect(alert.textContent).toContain("open your provider's console");
    expect(alert.textContent).not.toContain("came back");
    // the banner carries the sentence: the step's own headline is not printed a second time
    expect(alert.textContent!.match(/dropped off the network/g)).toHaveLength(1);
    expect(button("Close")).toBeTruthy();
  });

  it("closes the run after the step-up, and nothing else is offered meanwhile", async () => {
    await mount(actions(failed()));
    expect(disabled(button("Install updates…"))).toBe(true);
    await click(button("Close"));
    expect(guard).toHaveBeenCalledTimes(1);
    expect(updates.cancelOsRollout).toHaveBeenCalledWith({ rolloutId: "osr_1" });
    expect(text()).toContain("The update is closed.");
  });

  it("keeps the red pill for a day after the run was closed, without the banner or Close", async () => {
    await mount(actions(failed({ finishedUnix: NOW - 600 }, { status: OsRolloutStatus.CANCELLED, finishedUnix: NOW - 300 })));
    expect(document.querySelector(".pill")?.className).toContain("tone-bad");
    expect(document.querySelector("[role=alert]")).toBeNull();
    expect(button("Close")).toBeUndefined();
    expect(text()).toContain("The updates are installed, but the node refused to reboot.");
  });

  it("speaks the owner's language", async () => {
    setLang("ru");
    await mount(actions(failed()));
    expect(document.querySelector("[role=alert]")!.textContent).toContain("de1: обновления установлены, но сервер не перезагрузили.");
    expect(button("Закрыть")).toBeTruthy();
  });
});

describe("a node that refused to start the update", () => {
  const refused = (code: string) =>
    data(
      [node()],
      rollout([step({ state: OsStepState.FAILED, errorKey: "agent_refused", params: { code, mode: "security" }, finishedUnix: NOW - 60 })], { status: OsRolloutStatus.FAILED, finishedUnix: NOW - 60 }),
    );

  it("ends the run with the usual result line and says nothing on the server changed: no alert, no Close", async () => {
    await mount(actions(refused("busy")));
    const pill = document.querySelector(".pill");
    expect(pill?.className).toContain("tone-bad");
    expect(document.querySelector("[role=alert]")).toBeNull();
    expect(button("Close")).toBeUndefined();
    expect(text()).not.toContain("Closing the update");
    expect(text()).toContain("The node did not start the update. Nothing on the server was changed.");
    expect(text()).toContain("Another job is running on this server");
  });

  it("does not hold the node or any other: the next update may start at once", async () => {
    await mount(actions(refused("unsupported_os")));
    expect(disabled(button("Install updates…"))).toBeFalsy();
    expect(disabled(button("Reboot…"))).toBeFalsy();
    expect(text()).not.toContain("Another OS update is running");
  });

  it("says it in Russian too", async () => {
    setLang("ru");
    await mount(actions(refused("busy")));
    expect(text()).toContain("Нода не начала обновление. На сервере ничего не менялось.");
    expect(button("Закрыть")).toBeUndefined();
  });
});

describe("the card on the node's Server tab", () => {
  it("shows what the node reads and the same buttons, from the page's own list", async () => {
    await mount(<OsCard nodeId="nod_de1" />, { seed: data([node()]) });
    const s = text();
    expect(s).toContain("Operating system");
    expect(s).toContain("Ubuntu 24.04");
    expect(s).toContain("6.8.0-147-generic");
    expect(s).toContain("→ 6.8.0-150-generic");
    expect(s).toContain("12 waiting · 4 security");
    expect(s).toContain("Needed · to start kernel 6.8.0-150-generic");
    expect(disabled(button("Install updates…"))).toBeFalsy();
    expect(updates.getOsUpdates).not.toHaveBeenCalled(); // seeded and fresh: no second request of its own
  });

  it("shows nothing on the Cloudflare edition, and asks for nothing", async () => {
    await mount(<OsCard nodeId="nod_de1" />, { edition: "edge", unavailable: ["os_updates"], seed: data([node()]) });
    expect(host!.textContent).toBe("");
    expect(updates.getOsUpdates).not.toHaveBeenCalled();
  });

  it("shows nothing for a node the list does not have (a retired one)", async () => {
    await mount(<OsCard nodeId="nod_gone" />, { seed: data([node()]) });
    expect(host!.textContent).toBe("");
  });
});
