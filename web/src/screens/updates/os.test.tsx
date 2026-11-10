import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { OsUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { setLang } from "@/i18n";
import { meQuery } from "@/lib/session";
import { OsSection } from "./os";

// Updates -> Server operating system: a row for the panel's server and one per node from GetOsUpdates, read-only.

const updates = vi.hoisted(() => ({ getOsUpdates: vi.fn() }));
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), updates }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className }: { children?: ReactNode; className?: string }) => <a className={className}>{children}</a>,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => setLang("en"));
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  updates.getOsUpdates.mockReset();
  setLang("en");
});

const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
async function mount(unavailable: string[] = []) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", login: "owner" }, edition: unavailable.length ? "edge" : "vps", unavailable } as never);
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
    { nodeId: "nod_2", name: "nl1", connected: true, supported: true, onlineUsers: 0, onPanelServer: false, state: OsUpdateState.UPDATES,
      status: { ...base, distroId: "debian", distroVersion: "13", pending: 3, apt: "locked" } },
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
    // nl1: plain updates stay quiet, the busy package manager is called out
    expect(text).toContain("Debian 13");
    expect(text).toContain("3 waiting");
    expect(text).toContain("Updates available");
    expect(text).toContain("Another package manager is running there now");
    // fi1 has an older agent, msk1 is offline
    expect(text).toContain("Update the node agent to see this server's system");
    expect(text).toContain("Offline");
    // read-only: the section says so and offers no button
    expect(text).toContain("Mistgate only shows this for now");
    expect(host!.querySelector("button")).toBeNull();
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
    expect(text).toContain("Ждут: 12 · security: 4");
  });
});
