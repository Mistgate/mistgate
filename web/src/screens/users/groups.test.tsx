import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { defaultGroup, GroupEditModal, GroupsTab, wayOf } from "./groups";

const listGroups = vi.fn();
const createGroup = vi.fn();
const updateGroup = vi.fn();
const deleteGroup = vi.fn();
const navigate = vi.fn();
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
  nodes: { listNodes: () => Promise.resolve({ nodes: [] }) },
  dns: { listDnsPresets: () => Promise.resolve({ presets: [], providers: [], clientSupport: [] }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role } }) },
  subscriptions: { getSubscriptionSettings: () => Promise.resolve({ settings: { apps: [{ kind: App.HAPP, name: "Happ" }] }, effectiveTitle: "" }) },
}));
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
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
  for (const m of [listGroups, createGroup, updateGroup, deleteGroup, navigate]) m.mockReset();
});

const group = (over: Record<string, unknown> = {}) =>
  ({ id: "grp_all", name: "Everyone", profileIds: ["p_hy", "p_awg"], disabledNodeIds: [], userCount: 5, dnsPresetId: "", happNodes: 2, amneziaNodes: 0, ...over }) as unknown as Group;

async function mount(groups: Group[], props: { creating?: boolean; focus?: string; query?: string; children?: ReactNode } = {}) {
  listGroups.mockResolvedValue({ groups });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>{props.children ?? <GroupsTab creating={props.creating ?? false} onCreatingChange={() => {}} focus={props.focus} query={props.query} />}</ToastProvider>
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
const type = (el: HTMLInputElement, value: string) =>
  act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
const dialog = () => [...document.querySelectorAll("[role=dialog]")].at(-1)!;
const tile = () => [...document.querySelectorAll("button")].find((b) => b.textContent?.startsWith("New group"));
const card = (name: string) => [...document.querySelectorAll("h3")].find((h) => h.textContent === name)!.closest<HTMLElement>("div.relative")!;

describe("the Groups tab", () => {
  it("shows each group as a card: its people, what it gives with the protocols and where it works, four facts, Open and Delete", async () => {
    await mount([group(), group({ id: "grp_t", name: "test", profileIds: [], userCount: 0, happNodes: 0 })]);
    const c = card("Everyone");
    expect(c.textContent).toContain("5 people");
    const open = [...c.querySelectorAll<HTMLAnchorElement>("a")].filter((a) => a.getAttribute("href") === "/users/groups/$id");
    expect(open.length).toBe(2); // the head and "Open"
    expect(JSON.parse(open[0]!.dataset.params!)).toEqual({ id: "grp_all" });
    expect(open.some((a) => a.textContent === "Open")).toBe(true);
    // the subscription works on two nodes; the AmneziaWG profile is in the group but runs on no node: said in warning colour
    expect(c.textContent).toContain("Happ · 2 nodes");
    expect(c.textContent).toContain("AmneziaVPN keys");
    expect(c.textContent).toContain("no AmneziaWG of the group runs on an enabled node");
    expect(c.textContent).toContain("Hysteria2");
    expect(c.textContent).toContain("AmneziaWG");
    // the facts: people, profiles, nodes, DNS
    expect([...c.querySelectorAll("dt")].map((d) => d.textContent)).toEqual(["People", "Profiles", "Nodes", "DNS"]);
    expect([...c.querySelectorAll("dd")].map((d) => d.textContent)[0]).toBe("5");
    expect(card("test").textContent).toContain("No profiles: the group gives nothing");
  });

  it("offers a dashed tile for a new group, and a click on it opens the window", async () => {
    const opened = vi.fn();
    await mount([group()], { children: <GroupsTab creating={false} onCreatingChange={opened} /> });
    await click(tile());
    expect(opened).toHaveBeenCalledWith(true);
    expect(text()).toContain("Its own set of profiles and nodes for several people at once");
  });

  it("filters by the name in the bar and says so when no group matches, with a way to make it", async () => {
    await mount([group(), group({ id: "grp_f", name: "friends", userCount: 3 })], { query: "fri" });
    expect(document.querySelectorAll("h3")).toHaveLength(1);
    expect(text()).not.toContain("Everyone");
    act(() => root?.unmount());
    host?.remove();
    await mount([group()], { query: "zzz" });
    expect(text()).toContain("No such group");
    expect(button("Create group")).toBeDefined();
    expect(tile()).toBeUndefined(); // the tile is for the whole list, not for a search
  });

  it("starts an empty panel with “Everyone” and all the profiles in one click", async () => {
    createGroup.mockResolvedValue({ group: group() });
    await mount([]);
    expect(text()).toContain("No groups yet");
    await click(button("Create “Everyone” with all profiles"));
    await settle();
    expect(createGroup).toHaveBeenCalledWith({ name: "Everyone", profileIds: ["p_hy", "p_awg"] });
  });

  it("shows no actions to a read-only admin: no Delete, no tile", async () => {
    role = Role.READONLY;
    await mount([group()]);
    expect(button("Delete")).toBeUndefined();
    expect(tile()).toBeUndefined();
    expect(text()).toContain("Open");
  });

  it("takes an old link to a group (?group=<id>) to its page, once", async () => {
    await mount([group()], { focus: "grp_all" });
    expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ to: "/users/groups/$id", params: { id: "grp_all" }, replace: true }));
    expect(navigate).toHaveBeenCalledTimes(1);
  });
});

describe("deleting a group from the tab", () => {
  it("asks once in the link itself, then deletes an empty group", async () => {
    deleteGroup.mockResolvedValue({});
    await mount([group({ id: "grp_t", name: "test", userCount: 0 }), group()]);
    const del = () => [...card("test").querySelectorAll("button")].find((b) => b.textContent === "Delete" || b.textContent === "Really delete?")!;
    await click(del());
    expect(del().textContent).toBe("Really delete?");
    expect(deleteGroup).not.toHaveBeenCalled();
    await click(del());
    await settle();
    expect(deleteGroup).toHaveBeenCalledWith({ groupId: "grp_t" });
  });

  it("says the people must be moved first for a group that has people, and deletes nothing", async () => {
    await mount([group(), group({ id: "grp_f", name: "friends", userCount: 3 })]);
    const del = () => [...card("friends").querySelectorAll("button")].at(-1)!;
    await click(del());
    expect(del().textContent).toBe("Move the people first");
    await click(del());
    expect(deleteGroup).not.toHaveBeenCalled();
  });
});

describe("the window of a new group", () => {
  it("names the group, takes the profiles by the ways, and sends the profiles and the colour", async () => {
    createGroup.mockResolvedValue({ group: group({ id: "grp_n", name: "colleagues" }) });
    await mount([group()], { creating: true });
    const d = dialog();
    expect(d.textContent).toContain("New group");
    expect(d.textContent).toContain("What people get");
    // the board's "Limits for each" is not here: a group has no limits
    expect(d.textContent).not.toContain("Limits");
    const create = () => [...d.querySelectorAll("button")].find((b) => b.textContent === "Create group")!;
    expect(create().getAttribute("aria-disabled")).toBe("true");
    await type(d.querySelector<HTMLInputElement>("input")!, "colleagues");
    expect(create().getAttribute("aria-disabled")).toBe("false");
    // both ways start on; switching the keys off takes the AmneziaWG profile out
    const awg = [...d.querySelectorAll<HTMLElement>("[role=switch]")].find((s) => s.getAttribute("aria-label") === "AmneziaVPN")!;
    expect(awg.getAttribute("aria-checked")).toBe("true");
    await click(awg);
    expect(awg.getAttribute("aria-checked")).toBe("false");
    await click([...d.querySelectorAll("button")].find((b) => b.getAttribute("aria-label") === "Rose"));
    await click(create());
    await settle();
    expect(createGroup).toHaveBeenCalledWith({ name: "colleagues", profileIds: ["p_hy"], dnsPresetId: "", color: "rose" });
  });

  it("refuses a name that is taken, and a group with no way on", async () => {
    await mount([group()], { creating: true });
    const d = dialog();
    const create = () => [...d.querySelectorAll("button")].find((b) => b.textContent === "Create group")!;
    await type(d.querySelector<HTMLInputElement>("input")!, "everyone");
    expect(d.textContent).toContain("Such a group already exists");
    expect(create().getAttribute("aria-disabled")).toBe("true");
    await type(d.querySelector<HTMLInputElement>("input")!, "colleagues");
    for (const s of d.querySelectorAll<HTMLElement>("[role=switch]")) if (s.getAttribute("aria-checked") === "true" && s.getAttribute("aria-label") !== "All nodes") await click(s);
    expect(d.textContent).toContain("Turn on at least one method");
    expect(create().getAttribute("aria-disabled")).toBe("true");
  });

  it("takes the settings of another group when asked, and the nodes it leaves off", async () => {
    createGroup.mockResolvedValue({ group: group({ id: "grp_n", name: "copy" }) });
    await mount([group({ profileIds: ["p_hy"], disabledNodeIds: ["nod_x"] })], { creating: true });
    const d = dialog();
    await type(d.querySelector<HTMLInputElement>("input")!, "copy");
    await click([...d.querySelectorAll("[role=radio]")].find((r) => r.textContent === "Everyone"));
    await click([...d.querySelectorAll("button")].find((b) => b.textContent === "Create group"));
    await settle();
    expect(createGroup).toHaveBeenCalledWith(expect.objectContaining({ name: "copy", profileIds: ["p_hy"], disabledNodeIds: ["nod_x"] }));
  });
});

describe("editing a group in its window", () => {
  it("asks before taking a profile away from its people, with what they lose, and then saves", async () => {
    updateGroup.mockImplementation(async (r: { dryRun?: boolean }) =>
      r.dryRun
        ? { group: group(), impact: { users: 5, lost: [{ id: "p_awg", name: "AWG 3.1", protocol: "awg", awgDevices: 2 }], gained: [] } }
        : { group: group({ profileIds: ["p_hy"] }) },
    );
    await mount([group()], { children: <GroupEditModal group={group()} open onOpenChange={() => {}} /> });
    expect(dialog().textContent).toContain("Edit group");
    await click(button("AWG 3.1"));
    await click(button("Save"));
    await settle();
    expect(updateGroup).toHaveBeenCalledTimes(1);
    expect(updateGroup).toHaveBeenLastCalledWith(expect.objectContaining({ dryRun: true, profileIds: { values: ["p_hy"] } }));
    expect(dialog().textContent).toContain("5 people lose: “AWG 3.1” (2 AmneziaVPN keys stop working)");
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
    const ps = [{ id: "awg", apps: [App.AMNEZIA] }, { id: "hysteria2", apps: [App.HAPP] }, { id: "vless", apps: [App.HAPP] }];
    expect(wayOf("awg", ps)).toBe("awg");
    expect(wayOf("hysteria2", ps)).toBe("sub");
    expect(wayOf("vless", ps)).toBe("sub"); // VLESS rides the link next to Hysteria2
    expect(wayOf("awg", undefined)).toBe("awg");
    expect(wayOf("vless", undefined)).toBe("sub");
  });
});
