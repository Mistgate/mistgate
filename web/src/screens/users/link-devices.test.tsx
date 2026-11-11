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

async function mount(devices: DeviceN[]) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const user = { id: "usr_alice", name: "alice", groupName: "all", deviceLimit: 5, devicesUsed: devices.length, apps: { happ: true, amnezia: false } } as never;
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
  ({ id, kind: DeviceKind.LINK, model, platform: "windows", client: "", osVersion: "", awgProfileId: "", onlineNodeIds: [], firstSeenUnix: 0, lastSeenUnix: 0, lastHandshakeUnix: 0, usedBytes: 0, enrolled: false, enrolLinkStale: false, ...over }) as unknown as DeviceN;
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
