import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { OsRebootPolicy, OsRolloutMode, OsRolloutStatus, OsStepState, OsUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { setLang } from "@/i18n";
import { meQuery } from "@/lib/session";
import { OsSection } from "./os";

// Updates -> Server operating system: a row for the panel's server and one per node from GetOsUpdates; the owner opens a node's row
// to install security updates, reboot it and check for updates (os-actions.test.tsx has the buttons of the node's card and the
// window in full).

const updates = vi.hoisted(() => ({ getOsUpdates: vi.fn(), startOsRollout: vi.fn(), refreshOsUpdates: vi.fn() }));
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), updates }));
const guard = vi.hoisted(() => vi.fn((call: () => Promise<unknown>) => call()));
vi.mock("@/components/step-up", () => ({ useStepUp: () => guard, isStepUpCancelled: () => false }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className }: { children?: ReactNode; className?: string }) => <a className={className}>{children}</a>,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  setLang("en");
  updates.startOsRollout.mockResolvedValue({});
  updates.refreshOsUpdates.mockResolvedValue({ results: [{ nodeId: "nod_1", outcome: "started", code: "" }] });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of Object.values(updates)) m.mockReset();
  guard.mockReset();
  guard.mockImplementation((call: () => Promise<unknown>) => call());
  setLang("en");
});

const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const click = (el: Element | null | undefined) => {
  expect(el).toBeTruthy();
  return act(async () => void el!.dispatchEvent(new MouseEvent("click", { bubbles: true })));
};
/** The row of a server: the button that opens it. */
const rowBtn = (name: string) => [...host!.querySelectorAll<HTMLButtonElement>("button.uq-r")].find((b) => b.textContent!.startsWith(name));
const open = async (name: string) => {
  await click(rowBtn(name));
  await settle();
};
const bar = () => host!.querySelector<HTMLElement>("[data-shot=os-node-actions]");
const action = (label: string) => [...(bar()?.querySelectorAll("button") ?? [])].find((b) => b.textContent!.trim().startsWith(label));
const chips = () => [...host!.querySelectorAll(".uq-chip")].map((c) => c.textContent);
async function mount(unavailable: string[] = [], role?: Role) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", login: "owner", role }, edition: unavailable.length ? "edge" : "vps", unavailable } as never);
  await act(async () => root!.render(<QueryClientProvider client={qc}><OsSection /></QueryClientProvider>));
  for (let i = 0; i < 3; i++) await settle();
}

const now = Math.floor(Date.now() / 1000);
const base = {
  measuredUnix: now - 60, distroId: "ubuntu", distroVersion: "24.04", pending: 0, security: 0, notable: [], kernelRunning: "6.8.0-147-generic", kernelNewest: "6.8.0-147-generic",
  rebootRequired: false, rebootReasons: [], held: [], apt: "ok", halfConfigured: [], listsUnix: now - 3600, dpkgUnix: now - 7200, autoUpdates: "on", autoReboot: false,
  awgModuleNext: "", container: "", bootIdHash: "", error: "",
};
const answer = {
  nowUnix: now,
  scheduleTimezoneOffsetMinutes: 180,
  panelHost: { ...base },
  nodes: [
    { nodeId: "nod_1", name: "de1", connected: true, supported: true, onlineUsers: 4, onPanelServer: false, state: OsUpdateState.REBOOT_REQUIRED,
      status: { ...base, pending: 12, security: 4, kernelNewest: "6.8.0-150-generic", rebootRequired: true, rebootReasons: ["kernel"], autoUpdates: "off" } },
    { nodeId: "nod_2", name: "nl1", connected: true, supported: true, onlineUsers: 0, onPanelServer: false, state: OsUpdateState.UP_TO_DATE,
      status: { ...base, distroId: "debian", distroVersion: "13", pending: 3 } },
    { nodeId: "nod_3", name: "fi1", connected: true, supported: false, onlineUsers: 1, onPanelServer: false, state: OsUpdateState.UNSUPPORTED },
    { nodeId: "nod_4", name: "msk1", connected: false, supported: true, onlineUsers: 0, onPanelServer: false, state: OsUpdateState.OFFLINE },
  ],
};

describe("Server operating system", () => {
  it("shows the panel's server and every node with what it needs", async () => {
    updates.getOsUpdates.mockResolvedValue(answer);
    await mount();
    const text = host!.textContent!;
    expect(text).toContain("This panel's server");
    expect(chips()).toEqual(["Reboot needed1", "With security updates1"]); // the chips name what asks for attention
    // de1: a newer kernel is installed and waits for a reboot
    expect(text).toContain("6.8.0-147-generic");
    expect(text).toContain("→ 6.8.0-150-generic");
    expect(text).toContain("Reboot needed");
    expect(text).toContain("to start kernel 6.8.0-150-generic");
    expect(text).toContain("12 waiting · 4 security");
    expect(text).toContain("Automatic security updates are off");
    // nl1: ordinary updates are a quiet note beside the count, and do not take the green away
    expect(text).toContain("Debian 13");
    expect(text).toContain("No security updates");
    expect(text).toContain("3 ordinary updates");
    expect(text).toContain("Security up to date");
    expect(text).not.toContain("Updates available");
    // fi1 has an older agent, msk1 is offline
    expect(text).toContain("Update the node agent to see this server's system");
    expect(text).toContain("Offline");
    // what the panel can do (several nodes in one run, ordinary packages) and what stays by hand (its own server); not the owner: no button
    expect(text).toContain("Mistgate installs the security updates (or every package) and reboots servers from the panel. Tick several nodes to update them in one run, the quietest first.");
    expect(text).toContain("Update the panel's own server over SSH for now.");
    expect([...host!.querySelectorAll("button")].filter((b) => !b.classList.contains("uq-r"))).toHaveLength(0);
    expect(host!.querySelector("input[type=checkbox]")).toBeNull();
  });

  const owned = (extra: Record<string, unknown> = {}) => {
    const block = (n: { connected: boolean; supported: boolean }) => (!n.connected ? "offline" : !n.supported ? "agent_old" : "");
    updates.getOsUpdates.mockResolvedValue({
      ...answer,
      nodes: answer.nodes.map((n) => ({ ...n, updateCapable: n.supported && n.connected, updateBlock: block(n), updateBlockAll: block(n), rebootBlock: block(n) })),
      ...extra,
    });
  };

  it("gives the owner a row that opens to the actions, on each node and not on the panel's server", async () => {
    owned();
    await mount([], Role.OWNER);
    expect(host!.querySelectorAll(".uq-w")).toHaveLength(5);
    expect(host!.querySelector(".uq-w.flat")?.textContent).toContain("This panel's server");
    expect(host!.querySelectorAll("button[aria-label^='Operating system actions for']")).toHaveLength(0);
    // an idle node has nothing under its row until it is opened
    expect(bar()).toBeNull();
    await open("de1");
    expect(rowBtn("de1")!.getAttribute("aria-expanded")).toBe("true");
    expect(bar()).not.toBeNull();
    await open("de1");
    expect(bar()).toBeNull();
  });

  it("lists the three actions of a node that can be updated, all on, the install one naming the security updates", async () => {
    owned();
    await mount([], Role.OWNER);
    await open("de1");
    const labels = [...bar()!.querySelectorAll("button")].map((b) => b.textContent!.trim());
    expect(labels).toEqual(["Install security updates · 4", "Reboot…", "Check for updates"]);
    expect([...bar()!.querySelectorAll("button")].filter((b) => b.disabled)).toHaveLength(0);
    // the node's page is one click away
    expect(host!.textContent).toContain("Open node ›");
  });

  it("greys the actions out with the reason under them when the node cannot be updated", async () => {
    owned();
    await mount([], Role.OWNER);
    await open("fi1");
    expect(action("Install")?.disabled).toBe(true);
    expect(action("Reboot")?.disabled).toBe(true);
    // one reason for all three is said once
    expect(bar()!.textContent!.match(/The node agent is too old to run updates/g)).toHaveLength(1);
    // checking only reads, but an old agent cannot do it either
    expect(action("Check for updates")?.disabled).toBe(true);
  });

  it("says the node is offline in the row of an offline node", async () => {
    owned();
    await mount([], Role.OWNER);
    await open("msk1");
    expect(bar()!.textContent).toContain("The node is offline.");
  });

  it("puts the reason next to the action it belongs to when the actions have different ones", async () => {
    owned({ nodes: answer.nodes.map((n) => ({ ...n, updateCapable: true, updateBlock: "nothing_to_do", updateBlockAll: "nothing_to_do", rebootBlock: "", status: n.status && { ...n.status, pending: 0, security: 0 } })) });
    await mount([], Role.OWNER);
    await open("de1");
    expect(action("Install")?.disabled).toBe(true);
    expect(bar()!.textContent).toContain("There are no security updates to install.");
    expect(action("Reboot")?.disabled).toBe(false);
  });

  it("explains that a check for updates that has just started holds the node", async () => {
    owned({ nodes: answer.nodes.map((n) => ({ ...n, updateCapable: true, updateBlock: "node_busy", updateBlockAll: "node_busy", rebootBlock: "node_busy" })) });
    await mount([], Role.OWNER);
    await open("de1");
    expect([...bar()!.querySelectorAll("button")].every((b) => b.disabled)).toBe(true);
    expect(bar()!.textContent).toContain("for example a check for updates you have just started");
  });

  it("opens the window from the row, and starts the update after the step-up", async () => {
    owned();
    await mount([], Role.OWNER);
    await open("de1");
    await click(action("Install"));
    const dialog = document.querySelector<HTMLElement>("[data-shot=os-confirm]")!;
    expect(dialog.textContent).toContain("Install security updates on de1");
    await act(async () => [...dialog.querySelectorAll("button")].find((b) => b.textContent === "Install updates")!.click());
    for (let i = 0; i < 3; i++) await settle();
    expect(guard).toHaveBeenCalledTimes(1);
    expect(updates.startOsRollout).toHaveBeenCalledWith({ nodeIds: ["nod_1"], mode: OsRolloutMode.SECURITY, reboot: OsRebootPolicy.IF_REQUIRED });
  });

  it("opens the reboot window from the row", async () => {
    owned();
    await mount([], Role.OWNER);
    await open("de1");
    await click(action("Reboot"));
    expect(document.querySelector<HTMLElement>("[data-shot=os-confirm]")!.textContent).toContain("Reboot de1");
  });

  it("checks for updates from the row without a window or a step-up", async () => {
    owned();
    await mount([], Role.OWNER);
    await open("de1");
    await click(action("Check for updates"));
    expect(updates.refreshOsUpdates).toHaveBeenCalledWith({ nodeIds: ["nod_1"] });
    expect(guard).not.toHaveBeenCalled();
    expect(document.querySelector("[data-shot=os-confirm]")).toBeNull();
  });

  it("shows a stopped update under its closed row, and keeps install and reboot off while it waits to be closed", async () => {
    const stopped = {
      id: "osr_1", status: OsRolloutStatus.PAUSED, mode: OsRolloutMode.SECURITY, reboot: OsRebootPolicy.IF_REQUIRED, createdUnix: now - 600, finishedUnix: 0, pauseKey: "step_failed", pauseParams: { node: "fi1" },
      steps: [{ nodeId: "nod_3", nodeName: "fi1", state: OsStepState.FAILED, jobId: "osj_1", phase: "", phaseUnix: 0, kernelFrom: "", kernelTo: "", sentUnix: now - 500, rebootUnix: 0, backUnix: 0, finishedUnix: now - 400, errorKey: "job_failed", params: { code: "disk_full", mode: "security" } }],
    };
    owned({ rollout: stopped });
    await mount([], Role.OWNER);
    expect(host!.querySelector("[data-shot=os-node-actions] [role=alert]")?.textContent).toContain("The update of fi1 stopped.");
    await open("de1");
    expect(action("Install")?.disabled).toBe(true);
    expect(bar()!.textContent).toContain("Another OS update is running or waits to be closed.");
    // checking the lists stays possible: the run is on another node
    expect(action("Check for updates")?.disabled).toBe(false);
  });

  it("gives an observer the security names and the reason there are no buttons, and nothing to press", async () => {
    owned();
    await mount([], Role.READONLY);
    await open("de1");
    expect(host!.textContent).toContain("Only the owner can install updates and reboot");
    expect([...host!.querySelectorAll("button")].filter((b) => !b.classList.contains("uq-r"))).toHaveLength(0);
    expect(host!.textContent).toContain("Open node ›");
  });

  it("says why the panel cannot read its own server, and that everything is up to date", async () => {
    updates.getOsUpdates.mockResolvedValue({ ...answer, panelHost: undefined, nodes: [{ nodeId: "nod_1", name: "de1", connected: true, supported: true, onlineUsers: 0, onPanelServer: true, state: OsUpdateState.UP_TO_DATE, status: { ...base } }] });
    await mount();
    const text = host!.textContent!;
    expect(text).toContain("The panel does not run as root");
    expect(text).toContain("Runs on the panel's server");
    expect(text).toContain("The system is up to date on all servers");
  });

  it("is absent on the Cloudflare edition and asks for nothing", async () => {
    await mount(["os_updates"]);
    expect(host!.textContent).toBe("");
    expect(updates.getOsUpdates).not.toHaveBeenCalled();
  });

  it("speaks Russian", async () => {
    setLang("ru");
    updates.getOsUpdates.mockResolvedValue(answer);
    await mount();
    const text = host!.textContent!;
    expect(chips()).toEqual(["Нужна перезагрузка1", "С обновлениями безопасности1"]);
    expect(text).toContain("Ждут: 12 · безопасности: 4");
    expect(text).not.toContain("security: 4");
  });

  it("names the actions in Russian", async () => {
    setLang("ru");
    owned();
    await mount([], Role.OWNER);
    await open("de1");
    expect([...bar()!.querySelectorAll("button")].map((b) => b.textContent!.trim())).toEqual(["Поставить обновления безопасности · 4", "Перезагрузить…", "Проверить обновления"]);
  });
});
