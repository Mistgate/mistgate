import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { GroupEditModal } from "./create-user";
import type { UserN } from "./model";
import { DevicesPanel } from "./user-detail";

const updateGroup = vi.fn();
const listProfiles = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  groups: { listGroups: () => Promise.resolve({ groups: [] }), updateGroup: (...a: unknown[]) => updateGroup(...a), createGroup: vi.fn() },
  profiles: { listProfiles: (...a: unknown[]) => listProfiles(...a), listProtocols: () => Promise.resolve({ protocols: [] }) },
  dns: { listDnsPresets: () => Promise.resolve({ presets: [], providers: [], clientSupport: [] }) },
  subscriptions: { getSubscriptionSettings: () => Promise.resolve({ settings: { apps: [] }, effectiveTitle: "" }) },
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
  updateGroup.mockReset();
  listProfiles.mockReset();
});

async function mount(ui: ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>));
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));

const group = { id: "grp_1", name: "тест", profileIds: ["p1"], userCount: 3, dnsPresetId: "" } as unknown as Group;

describe("editing a group from the user card", () => {
  it("opens prefilled, shows the user count and saves the profile set with UpdateGroup", async () => {
    listProfiles.mockResolvedValue({ profiles: [{ id: "p1", name: "reality" }, { id: "p2", name: "awg-main" }] });
    updateGroup.mockResolvedValue({ group: { ...group, profileIds: ["p1", "p2"] } });
    const onOpenChange = vi.fn();
    await mount(<GroupEditModal group={group} open onOpenChange={onOpenChange} />);

    expect(document.querySelector<HTMLInputElement>("[role=dialog] input")?.value).toBe("тест");
    expect(button("reality")?.getAttribute("aria-pressed")).toBe("true");
    expect(button("awg-main")?.getAttribute("aria-pressed")).toBe("false");
    expect(text()).toContain("3 users are in this group");

    await click(button("awg-main"));
    await click(button("Save"));
    expect(updateGroup).toHaveBeenCalledWith({ groupId: "grp_1", name: "тест", profileIds: { values: ["p1", "p2"] }, dnsPresetId: "" });
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});

describe("the Devices section of a user without an AmneziaWG profile", () => {
  const user = { id: "u1", name: "Marina", groupName: "тест", deviceLimit: 5, devicesUsed: 0, apps: { happ: true, amnezia: true } } as unknown as UserN;
  const panel = (u: UserN, profiles: { protocol: string }[], onEditGroup = vi.fn()) => (
    <DevicesPanel user={u} devices={[]} profiles={profiles as never} actions={{ refresh: vi.fn() } as never} onEditGroup={onEditGroup} />
  );

  it("says so and opens the group editor", async () => {
    const onEditGroup = vi.fn();
    await mount(panel(user, [{ protocol: "vless" }], onEditGroup));
    expect(text()).toContain("The group “тест” has no AmneziaWG profile");
    await click(button("Edit group"));
    expect(onEditGroup).toHaveBeenCalled();
  });

  it("stays quiet when the group has an AWG profile or the user has no Amnezia", async () => {
    await mount(panel(user, [{ protocol: "awg" }]));
    expect(text()).not.toContain("no AmneziaWG profile");
    act(() => root?.unmount());
    await mount(panel({ ...user, apps: { happ: true, amnezia: false } } as UserN, [{ protocol: "vless" }]));
    expect(text()).not.toContain("no AmneziaWG profile");
  });
});
