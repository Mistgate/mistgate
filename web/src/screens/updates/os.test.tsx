import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { OsRebootPolicy, OsRolloutMode, OsRolloutStatus, OsStepState, OsUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { setLang } from "@/i18n";
import { meQuery } from "@/lib/session";
import { OsSection } from "./os";

// Updates -> Server operating system: a row for the panel's server and one per node from GetOsUpdates; the owner also gets the
// ⋯ menu of each node row that installs security updates, reboots it and checks for updates (os-actions.test.tsx has the
// buttons of the node's card and the window in full).

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
/** Base UI opens a menu on the press, not on the click. */
async function press(el: Element | null | undefined) {
  expect(el).toBeTruthy();
  await act(async () => {
    for (const type of ["pointerdown", "mousedown"]) el!.dispatchEvent(new MouseEvent(type, { bubbles: true, button: 0 }));
    for (const type of ["pointerup", "mouseup", "click"]) el!.dispatchEvent(new MouseEvent(type, { bubbles: true, button: 0 }));
  });
  for (let i = 0; i < 3; i++) await settle();
}
const trigger = (node: string, label = "Operating system actions for") => host!.querySelector(`button[aria-label="${label} ${node}"]`);
const items = () => [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')];
const item = (label: string) => items().find((i) => i.textContent?.startsWith(label));
const popup = () => document.querySelector("[role=menu]")?.textContent ?? "";
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
    expect(text).toContain("Need a reboot: 1"); // the summary names what asks for attention
    expect(text).toContain("With security updates: 1");
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
    expect(text).toContain("The panel's own server is updated over SSH.");
    expect(host!.querySelector("button")).toBeNull();
  });

  const owned = (extra: Record<string, unknown> = {}) => {
    const block = (n: { connected: boolean; supported: boolean }) => (!n.connected ? "offline" : !n.supported ? "agent_old" : "");
    updates.getOsUpdates.mockResolvedValue({
      ...answer,
      nodes: answer.nodes.map((n) => ({ ...n, updateCapable: n.supported && n.connected, updateBlock: block(n), rebootBlock: block(n) })),
      ...extra,
    });
  };

  it("gives the owner a ⋯ menu on each node row, not three buttons, and none for the panel's server", async () => {
    owned();
    await mount([], Role.OWNER);
    // four nodes, each in the table and in the phone's card
    expect(host!.querySelectorAll("button[aria-label^='Operating system actions for']")).toHaveLength(8);
    expect([...host!.querySelectorAll("button")].filter((b) => b.textContent === "Install updates…")).toHaveLength(0);
    // an idle node has nothing under its row, and the menu is closed until it is pressed
    expect(host!.querySelectorAll("[data-shot=os-node-actions]")).toHaveLength(0);
    expect(items()).toHaveLength(0);
  });

  it("lists the three actions in the menu of a node that can be updated, all on", async () => {
    owned();
    await mount([], Role.OWNER);
    await press(trigger("de1"));
    expect(items().map((i) => i.textContent)).toEqual(["Install updates…", "Reboot…", "Check for updates"]);
    expect(items().filter((i) => i.hasAttribute("data-disabled"))).toHaveLength(0);
  });

  it("greys the actions out with the reason in the item when the node cannot be updated", async () => {
    owned();
    await mount([], Role.OWNER);
    await press(trigger("fi1"));
    expect(item("Install updates…")?.hasAttribute("data-disabled")).toBe(true);
    expect(item("Reboot…")?.hasAttribute("data-disabled")).toBe(true);
    // one reason for all three is said once, on top of the menu
    expect(popup().match(/The node agent is too old to run updates/g)).toHaveLength(1);
    // checking only reads, but an old agent cannot do it either
    expect(item("Check for updates")?.hasAttribute("data-disabled")).toBe(true);
  });

  it("says the node is offline in the menu of an offline node", async () => {
    owned();
    await mount([], Role.OWNER);
    await press(trigger("msk1"));
    expect(popup()).toContain("The node is offline.");
  });

  it("puts a reason under the item it belongs to when the items have different ones", async () => {
    owned({ nodes: answer.nodes.map((n) => ({ ...n, updateCapable: true, updateBlock: "nothing_to_do", rebootBlock: "", status: n.status && { ...n.status, pending: 0, security: 0 } })) });
    await mount([], Role.OWNER);
    await press(trigger("de1"));
    expect(item("Install updates…")?.hasAttribute("data-disabled")).toBe(true);
    expect(item("Install updates…")?.textContent).toContain("There are no security updates to install.");
    expect(item("Reboot…")?.hasAttribute("data-disabled")).toBe(false);
    expect(item("Reboot…")?.textContent).toBe("Reboot…");
  });

  it("explains that a check for updates that has just started holds the node", async () => {
    owned({ nodes: answer.nodes.map((n) => ({ ...n, updateCapable: true, updateBlock: "node_busy", rebootBlock: "node_busy" })) });
    await mount([], Role.OWNER);
    await press(trigger("de1"));
    expect(items().every((i) => i.hasAttribute("data-disabled"))).toBe(true);
    expect(popup()).toContain("for example a check for updates you have just started");
  });

  it("opens the window from the menu, and starts the update after the step-up", async () => {
    owned();
    await mount([], Role.OWNER);
    await press(trigger("de1"));
    await press(item("Install updates…"));
    const dialog = document.querySelector<HTMLElement>("[data-shot=os-confirm]")!;
    expect(dialog.textContent).toContain("Install security updates on de1");
    await act(async () => [...dialog.querySelectorAll("button")].find((b) => b.textContent === "Install updates")!.click());
    for (let i = 0; i < 3; i++) await settle();
    expect(guard).toHaveBeenCalledTimes(1);
    expect(updates.startOsRollout).toHaveBeenCalledWith({ nodeIds: ["nod_1"], mode: OsRolloutMode.SECURITY, reboot: OsRebootPolicy.IF_REQUIRED });
  });

  it("opens the reboot window from the menu", async () => {
    owned();
    await mount([], Role.OWNER);
    await press(trigger("de1"));
    await press(item("Reboot…"));
    expect(document.querySelector<HTMLElement>("[data-shot=os-confirm]")!.textContent).toContain("Reboot de1");
  });

  it("checks for updates from the menu without a window or a step-up", async () => {
    owned();
    await mount([], Role.OWNER);
    await press(trigger("de1"));
    await press(item("Check for updates"));
    expect(updates.refreshOsUpdates).toHaveBeenCalledWith({ nodeIds: ["nod_1"] });
    expect(guard).not.toHaveBeenCalled();
    expect(document.querySelector("[data-shot=os-confirm]")).toBeNull();
  });

  it("keeps install and reboot off while a stopped update waits to be closed, and shows that run under its row", async () => {
    const stopped = {
      id: "osr_1", status: OsRolloutStatus.PAUSED, mode: OsRolloutMode.SECURITY, reboot: OsRebootPolicy.IF_REQUIRED, createdUnix: now - 600, finishedUnix: 0, pauseKey: "step_failed", pauseParams: { node: "fi1" },
      steps: [{ nodeId: "nod_3", nodeName: "fi1", state: OsStepState.FAILED, jobId: "osj_1", phase: "", phaseUnix: 0, kernelFrom: "", kernelTo: "", sentUnix: now - 500, rebootUnix: 0, backUnix: 0, finishedUnix: now - 400, errorKey: "job_failed", params: { code: "disk_full", mode: "security" } }],
    };
    owned({ rollout: stopped });
    await mount([], Role.OWNER);
    await press(trigger("de1"));
    expect(item("Install updates…")?.hasAttribute("data-disabled")).toBe(true);
    expect(popup()).toContain("Another OS update is running or waits to be closed.");
    // checking the lists stays possible: the run is on another node
    expect(item("Check for updates")?.hasAttribute("data-disabled")).toBe(false);
    expect(host!.querySelector("[data-shot=os-node-actions] [role=alert]")?.textContent).toContain("The update of fi1 stopped.");
  });

  it("says why the panel cannot read its own server, and that everything is up to date", async () => {
    updates.getOsUpdates.mockResolvedValue({ ...answer, panelHost: undefined, nodes: [{ nodeId: "nod_1", name: "de1", connected: true, supported: true, onlineUsers: 0, onPanelServer: true, state: OsUpdateState.UP_TO_DATE, status: { ...base } }] });
    await mount();
    const text = host!.textContent!;
    expect(text).toContain("The panel does not run as root");
    expect(text).toContain("Runs on the panel's server");
    expect(text).toContain("Every server is up to date");
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
    expect(text).toContain("Нужна перезагрузка: 1");
    expect(text).toContain("Ждут: 12 · безопасности: 4");
    expect(text).not.toContain("security: 4");
  });

  it("names the menu in Russian", async () => {
    setLang("ru");
    owned();
    await mount([], Role.OWNER);
    await press(trigger("de1", "Действия с ОС:"));
    expect(items().map((i) => i.textContent)).toEqual(["Установить обновления…", "Перезагрузить…", "Проверить обновления"]);
  });
});
