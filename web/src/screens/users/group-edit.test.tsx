import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { GroupEditModal } from "./groups";
import type { UserN } from "./model";
import { DevicesPanel } from "./user-devices";

const updateGroup = vi.fn();
const listGroups = vi.fn().mockResolvedValue({ groups: [] });
const listProfiles = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, className }: { children?: ReactNode; to: string; params?: object; className?: string }) => (
    <a href={to} data-params={JSON.stringify(params ?? {})} className={className}>
      {children}
    </a>
  ),
}));
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  groups: { listGroups: (...a: unknown[]) => listGroups(...a), updateGroup: (...a: unknown[]) => updateGroup(...a), createGroup: vi.fn() },
  profiles: { listProfiles: (...a: unknown[]) => listProfiles(...a), listProtocols: () => Promise.resolve({ protocols: [] }) },
  nodes: { listNodes: () => Promise.resolve({ nodes: [{ id: "nod_1", name: "de1", countryCode: "DE" }] }) },
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
  listGroups.mockReset();
  listGroups.mockResolvedValue({ groups: [] });
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

const group = { id: "grp_1", name: "тест", profileIds: ["p1"], disabledNodeIds: [], userCount: 3, dnsPresetId: "", color: "rose" } as unknown as Group;

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
    // the window of a new group, with the group in it: no "take settings from"
    expect(document.querySelector("[data-shot=edit-group]")).not.toBeNull();
    expect(text()).not.toContain("Take settings from");

    await click(button("awg-main"));
    await click(button("Save"));
    expect(updateGroup).toHaveBeenCalledWith({ groupId: "grp_1", name: "тест", profileIds: { values: ["p1", "p2"] }, dnsPresetId: "" });
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("shows the group's colour among the swatches and saves a changed one", async () => {
    listProfiles.mockResolvedValue({ profiles: [{ id: "p1", name: "reality" }] });
    updateGroup.mockResolvedValue({ group });
    await mount(<GroupEditModal group={group} open onOpenChange={vi.fn()} />);
    const swatches = [...document.querySelectorAll<HTMLElement>("[role=group][aria-labelledby=ug-color] button")];
    expect(swatches.map((r) => r.getAttribute("aria-label"))).toEqual(["Lavender", "Sand", "Sage", "Rose", "Sky", "Mint"]);
    expect(swatches.map((r) => r.getAttribute("aria-pressed"))).toEqual(["false", "false", "false", "true", "false", "false"]);

    await click(swatches[4]);
    expect(swatches[4]!.getAttribute("aria-pressed")).toBe("true");
    await click(button("Save"));
    expect(updateGroup).toHaveBeenCalledWith(expect.objectContaining({ groupId: "grp_1", color: "sky" }));
  });

  it("saves a node disabled for the group", async () => {
    listProfiles.mockResolvedValue({ profiles: [{ id: "p1", name: "reality" }] });
    updateGroup.mockResolvedValue({ group });
    await mount(<GroupEditModal group={group} open onOpenChange={vi.fn()} />);
    // every node is on: the nodes can be told apart once "All nodes" is off
    await click(document.querySelector("[role=switch][aria-label='All nodes']"));
    expect(button("de1")?.getAttribute("aria-pressed")).toBe("true");
    await click(button("de1"));
    await click(button("Save"));
    expect(updateGroup).toHaveBeenCalledWith(expect.objectContaining({ disabledNodeIds: { values: ["nod_1"] } }));
  });

  it("refuses a name another group has, and lets the group keep its own", async () => {
    listProfiles.mockResolvedValue({ profiles: [{ id: "p1", name: "reality" }] });
    listGroups.mockResolvedValue({ groups: [group, { id: "grp_2", name: "друзья" }] });
    await mount(<GroupEditModal group={group} open onOpenChange={vi.fn()} />);
    expect(text()).not.toContain("Such a group already exists");
    const input = document.querySelector<HTMLInputElement>("[role=dialog] input")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "Друзья");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(text()).toContain("Such a group already exists");
  });

  it("says where to go when the panel refuses to switch on a node the owner holds back (node_held)", async () => {
    listProfiles.mockResolvedValue({ profiles: [{ id: "p1", name: "reality" }] });
    updateGroup.mockRejectedValue(new ConnectError("node_held: node=nod_1", Code.FailedPrecondition));
    const onOpenChange = vi.fn();
    await mount(<GroupEditModal group={{ ...group, disabledNodeIds: ["nod_1"] } as Group} open onOpenChange={onOpenChange} />);
    // the node is off for this group now; the owner switches it on and saves
    await click(button("de1"));
    await click(button("Save"));
    expect(updateGroup).toHaveBeenCalledWith(expect.objectContaining({ disabledNodeIds: { values: [] } }));
    expect(text()).toContain("“de1” is not handed out to this group");
    expect(text()).toContain("Open the node and give it to this group");
    expect(document.querySelector<HTMLAnchorElement>("[role=dialog] a")?.dataset.params).toBe('{"id":"nod_1"}');
    expect(onOpenChange).not.toHaveBeenCalled(); // the window stays: nothing was saved
  });
});

describe("the Devices section of a user without an AmneziaWG profile", () => {
  const user = { id: "u1", name: "alice", groupName: "тест", deviceLimit: 5, devicesUsed: 0, apps: { happ: true, amnezia: true } } as unknown as UserN;
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
