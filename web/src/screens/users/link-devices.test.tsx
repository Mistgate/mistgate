import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { DeviceKind } from "@/gen/mistgate/admin/v1/user_pb";
import type { DeviceN } from "./model";
import { DevicesPanel } from "./user-detail";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  subscriptions: { getSubscriptionSettings: () => Promise.resolve({ settings: { apps: [] }, effectiveTitle: "" }) },
  profiles: { listProtocols: () => Promise.resolve({ protocols: [] }) },
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children?: ReactNode; to: string }) => <a href={to}>{children}</a>,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(devices: DeviceN[], userOver: Record<string, unknown> = {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const user = { id: "usr_alice", name: "alice", groupName: "all", deviceLimit: 5, devicesUsed: devices.length, devicesIdle: 0, apps: { happ: true, amnezia: false }, ...userOver } as never;
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <DevicesPanel user={user} devices={devices} profiles={[]} actions={{ refresh: vi.fn() } as never} onEditGroup={vi.fn()} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const device = (id: string, model: string, over: object = {}) =>
  ({ id, kind: DeviceKind.LINK, model, platform: "windows", client: "", osVersion: "", awgProfileId: "", onlineNodeIds: [], firstSeenUnix: 0, lastSeenUnix: 0, lastHandshakeUnix: 0, usedBytes: 0, enrolled: false, enrolLinkStale: false, idle: false, idleSinceUnix: 0, ...over }) as unknown as DeviceN;
const row = (name: string) => [...document.querySelectorAll("li")].find((li) => li.textContent?.includes(name));
const badge = (name: string) => [...(row(name)?.querySelectorAll("span[tabindex]") ?? [])].find((s) => /^(By key|Link changed)$/.test(s.textContent ?? ""));

describe("the app-key labels on a device row", () => {
  it("marks a device connected by its own key and one whose link has changed, and leaves a plain link device alone", async () => {
    await mount([device("d1", "Plain PC"), device("d2", "Keyed PC", { enrolled: true }), device("d3", "Old Link PC", { enrolled: true, enrolLinkStale: true })]);
    expect(row("Plain PC")?.textContent).not.toMatch(/By key|Link changed/);
    expect(badge("Plain PC")).toBeUndefined();
    expect(badge("Keyed PC")?.textContent).toBe("By key");
    expect(badge("Keyed PC")?.getAttribute("aria-label")).toContain("servers come to the app, not through the link");
    expect(badge("Old Link PC")?.textContent).toBe("Link changed");
    expect(badge("Old Link PC")?.getAttribute("aria-label")).toContain("reconnects on its own at the next sync");
    // the stale one is amber, the keyed one quiet grey
    expect(badge("Old Link PC")?.className).toContain("text-warn-text");
    expect(badge("Keyed PC")?.className).not.toContain("text-warn-text");
  });
});

describe("an inactive device", () => {
  const IDLE_SINCE = Math.floor(Date.UTC(2026, 9, 12, 12) / 1000);
  const counter = () => [...document.querySelectorAll("span")].find((s) => /^\d+ of \d+$/.test(s.textContent ?? ""))?.parentElement?.textContent ?? "";

  it("gets a calm plaque with the date and a line that it takes no place, and a live device gets neither", async () => {
    await mount([device("d1", "Live PC"), device("d2", "Old Tablet", { idle: true, idleSinceUnix: IDLE_SINCE })], { devicesIdle: 1 });
    expect(row("Live PC")?.textContent).not.toMatch(/Inactive|Takes no place/);
    const old = row("Old Tablet")!;
    expect(old.textContent).toContain("Inactive since");
    expect(old.textContent).toContain("Oct 12");
    expect(old.textContent).toContain("Takes no place in the limit");
    const plaque = [...old.querySelectorAll("span[tabindex]")].find((s) => s.textContent?.startsWith("Inactive since"))!;
    expect(plaque.getAttribute("aria-label")).toContain("its keys keep working, but it does not count toward the limit");
    // quiet, not an alarm: the grey-blue pill, nothing red or amber on the row
    expect(old.innerHTML).not.toContain("danger");
    expect(old.innerHTML).not.toContain("warn");
  });

  it("is said beside the count: '1 of 5' and '+1 inactive'", async () => {
    await mount([device("d1", "Live PC"), device("d2", "Old Tablet", { idle: true, idleSinceUnix: IDLE_SINCE })], { devicesUsed: 1, devicesIdle: 1 });
    expect(counter()).toContain("1 of 5");
    expect(counter()).toContain("+1 inactive");
  });

  it("leaves the count alone when no device is inactive", async () => {
    await mount([device("d1", "Live PC")]);
    expect(counter()).toContain("1 of 5");
    expect(counter()).not.toContain("inactive");
  });

  it("marks an AmneziaWG key the same way", async () => {
    await mount(
      [device("k1", "Pixel", { kind: DeviceKind.KEY, awgProfileId: "p1", awgProfileName: "AWG 3.1", platform: "android", idle: true, idleSinceUnix: IDLE_SINCE })],
      { devicesIdle: 1, devicesUsed: 0, apps: { happ: false, amnezia: true } },
    );
    expect(row("Pixel")?.textContent).toContain("Inactive since");
    expect(row("Pixel")?.textContent).toContain("Takes no place in the limit");
  });
});
