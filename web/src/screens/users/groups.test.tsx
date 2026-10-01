import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { defaultGroup, GroupsTab, wayOf } from "./groups";

const listGroups = vi.fn();
const createGroup = vi.fn();
const updateGroup = vi.fn();
const deleteGroup = vi.fn();
let role = Role.OWNER;
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  groups: {
    listGroups: (...a: unknown[]) => listGroups(...a),
    createGroup: (...a: unknown[]) => createGroup(...a),
    updateGroup: (...a: unknown[]) => updateGroup(...a),
    deleteGroup: (...a: unknown[]) => deleteGroup(...a),
  },
  profiles: {
    listProfiles: () =>
      Promise.resolve({
        profiles: [
          { id: "p_hy", name: "hy2 · 443", protocol: "hysteria2" },
          { id: "p_awg", name: "AWG 3.1", protocol: "awg" },
        ],
      }),
    listProtocols: () => Promise.resolve({ protocols: [{ id: "hysteria2", apps: [App.HAPP] }, { id: "awg", apps: [App.AMNEZIA] }] }),
  },
  dns: { listDnsPresets: () => Promise.resolve({ presets: [], providers: [], clientSupport: [] }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role } }) },
  subscriptions: { getSubscriptionSettings: () => Promise.resolve({ settings: { apps: [{ kind: App.HAPP, name: "Happ" }] }, effectiveTitle: "" }) },
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, search, className, ...rest }: { children?: ReactNode; to: string; params?: object; search?: object; className?: string }) => (
    <a href={to} data-params={JSON.stringify(params ?? {})} data-search={JSON.stringify(search ?? {})} className={className} aria-label={(rest as { "aria-label"?: string })["aria-label"]}>
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
  role = Role.OWNER;
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [listGroups, createGroup, updateGroup, deleteGroup]) m.mockReset();
});

const group = (over: Record<string, unknown> = {}) =>
  ({ id: "grp_all", name: "Everyone", profileIds: ["p_hy", "p_awg"], userCount: 5, dnsPresetId: "", happNodes: 2, amneziaNodes: 0, ...over }) as unknown as Group;

async function mount(groups: Group[], props: { creating?: boolean; focus?: string } = {}) {
  listGroups.mockResolvedValue({ groups });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <GroupsTab creating={props.creating ?? false} onCreatingChange={() => {}} focus={props.focus} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async () => {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label || b.getAttribute("aria-label") === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const dialog = () => [...document.querySelectorAll("[role=dialog]")].at(-1)!;

describe("the Groups tab", () => {
  it("shows each group with its people, and its profiles in the two ways, with where each works now; every name is a link", async () => {
    await mount([group(), group({ id: "grp_t", name: "test", profileIds: [], userCount: 0, happNodes: 0 })]);
    const people = document.querySelector<HTMLAnchorElement>('a[aria-label="Users of group “Everyone”"]')!;
    expect(people.textContent).toContain("5 people");
    expect(people.dataset.search).toBe(JSON.stringify({ group: "grp_all" }));
    expect(text()).toContain("Subscription (Happ) · 2 nodes");
    // the AmneziaWG profile is in the group but runs on no node: said in the row, in warning colour
    expect(text()).toContain("AmneziaVPN keys · no AmneziaWG of the group runs on a node");
    const profile = [...document.querySelectorAll<HTMLAnchorElement>("a")].find((a) => a.textContent === "AWG 3.1")!;
    expect(profile.getAttribute("href")).toBe("/profiles/$id");
    expect(profile.dataset.params).toBe(JSON.stringify({ id: "p_awg" }));
    expect(text()).toContain("No profiles: the group gives nothing");
  });

  it("starts an empty panel with “Everyone” and all the profiles in one click", async () => {
    createGroup.mockResolvedValue({ group: group() });
    await mount([]);
    expect(text()).toContain("No groups yet");
    await click(button("Create “Everyone” with all profiles"));
    await settle();
    expect(createGroup).toHaveBeenCalledWith({ name: "Everyone", profileIds: ["p_hy", "p_awg"] });
  });

  it("shows no actions to a read-only admin", async () => {
    role = Role.READONLY;
    await mount([group()]);
    expect(button("Edit")).toBeUndefined();
    expect(button("Delete")).toBeUndefined();
  });
});

describe("deleting a group", () => {
  it("asks and deletes an empty one", async () => {
    deleteGroup.mockResolvedValue({});
    await mount([group({ id: "grp_t", name: "test", userCount: 0 }), group()]);
    await click([...document.querySelectorAll("button")].find((b) => b.textContent === "Delete")); // the first row's
    await settle();
    expect(dialog().textContent).toContain("Delete the group “test”?");
    expect(dialog().textContent).toContain("Nobody is in it");
    await click(button("Delete group"));
    await settle();
    expect(deleteGroup).toHaveBeenCalledWith({ groupId: "grp_t", moveUsersTo: "" });
  });

  it("asks where the people of a group go, “Everyone” first, and moves them in the same call", async () => {
    deleteGroup.mockResolvedValue({});
    await mount([group(), group({ id: "grp_f", name: "friends", userCount: 3 }), group({ id: "grp_x", name: "work", userCount: 0 })]);
    const rows = [...document.querySelectorAll("button")].filter((b) => b.textContent === "Delete");
    await click(rows[1]); // friends
    await settle();
    expect(dialog().textContent).toContain("3 people are in the group. Where to move them?");
    await click(button("Move 3 people and delete"));
    await settle();
    expect(deleteGroup).toHaveBeenCalledWith({ groupId: "grp_f", moveUsersTo: "grp_all" });
  });

  it("says there is nowhere to move them when it is the only group", async () => {
    await mount([group()]);
    await click(button("Delete"));
    await settle();
    expect(dialog().textContent).toContain("Make another group first");
    expect((button("Move 5 people and delete") as HTMLButtonElement).disabled).toBe(true);
  });
});

describe("editing a group", () => {
  it("asks before taking a profile away from its people, with what they lose, and then saves", async () => {
    updateGroup.mockImplementation(async (r: { dryRun?: boolean }) =>
      r.dryRun
        ? { group: group(), impact: { users: 5, lost: [{ id: "p_awg", name: "AWG 3.1", protocol: "awg", awgDevices: 2 }], gained: [] } }
        : { group: group({ profileIds: ["p_hy"] }) },
    );
    await mount([group()], { focus: "grp_all" }); // "?group=" opens the editor
    expect(dialog().textContent).toContain("Edit group");
    await click(button("AWG 3.1"));
    await click(button("Save"));
    await settle();
    expect(updateGroup).toHaveBeenCalledTimes(1);
    expect(updateGroup).toHaveBeenLastCalledWith(expect.objectContaining({ dryRun: true, profileIds: { values: ["p_hy"] } }));
    expect(dialog().textContent).toContain("5 people lose: «AWG 3.1» (2 AmneziaVPN keys stop working)");
    await click(button("Take it away from 5 people"));
    await settle();
    expect(updateGroup).toHaveBeenCalledTimes(2);
    expect(updateGroup.mock.calls[1]![0]).not.toHaveProperty("dryRun");
  });
});

describe("the default group and the two ways", () => {
  it("lands a new user in “Everyone” (in either language), else in the first group", () => {
    expect(defaultGroup([group({ id: "a", name: "friends" }), group({ id: "b", name: "Все" })])?.id).toBe("b");
    expect(defaultGroup([group({ id: "a", name: "friends" }), group({ id: "b", name: "everyone" })])?.id).toBe("b");
    expect(defaultGroup([group({ id: "a", name: "friends" })])?.id).toBe("a");
    expect(defaultGroup([])).toBeUndefined();
  });

  it("puts a protocol only AmneziaVPN takes on the keys side and the rest on the link", () => {
    const ps = [{ id: "awg", apps: [App.AMNEZIA] }, { id: "hysteria2", apps: [App.HAPP] }];
    expect(wayOf("awg", ps)).toBe("awg");
    expect(wayOf("hysteria2", ps)).toBe("sub");
    expect(wayOf("awg", undefined)).toBe("awg");
    expect(wayOf("vless", undefined)).toBe("sub");
  });
});
