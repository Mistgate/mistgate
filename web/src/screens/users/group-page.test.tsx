import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App, NodeShareMode, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { GroupScreen } from "./group-page";
import { lostProfileIds, moveUsers } from "./group-move";

const listGroups = vi.fn();
const updateGroup = vi.fn();
const deleteGroup = vi.fn();
const listUsers = vi.fn();
const updateUser = vi.fn();
let role = Role.OWNER;
let search: Record<string, unknown> = {};
const navigate = vi.fn();
// what the owner holds back: the share of each node by id (the panel sends Node.sharing)
let shares: Record<string, { mode: NodeShareMode }> = {};

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  groups: { listGroups: (...a: unknown[]) => listGroups(...a), updateGroup: (...a: unknown[]) => updateGroup(...a), deleteGroup: (...a: unknown[]) => deleteGroup(...a), createGroup: vi.fn() },
  users: { listUsers: (...a: unknown[]) => listUsers(...a), updateUser: (...a: unknown[]) => updateUser(...a) },
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
  nodes: {
    listNodes: () =>
      Promise.resolve({
        nodes: [
          { id: "nod_de1", name: "de1", countryCode: "DE", location: "Frankfurt", provider: "Example Cloud", status: NodeStatus.ONLINE, protocols: ["hysteria2", "awg"], sharing: shares.nod_de1 },
          { id: "nod_nl1", name: "nl1", countryCode: "NL", location: "Amsterdam", provider: "Example Cloud", status: NodeStatus.ONLINE, protocols: ["hysteria2"], sharing: shares.nod_nl1 },
        ],
      }),
  },
  dns: { listDnsPresets: () => Promise.resolve({ presets: [{ id: "dns_cf", name: "Cloudflare", description: "", builtin: true, servers: [], split: [], splitDirect: false, ipv4Only: false, users: 0, isDefault: true }], providers: [], clientSupport: [] }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role } }) },
  subscriptions: { getSubscriptionSettings: () => Promise.resolve({ settings: { apps: [{ kind: App.HAPP, name: "Happ" }] }, effectiveTitle: "" }) },
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, search: s, className, ...rest }: { children?: ReactNode; to: string; params?: object; search?: object; className?: string }) => (
    <a {...rest} href={to} data-params={JSON.stringify(params ?? {})} data-search={JSON.stringify(s ?? {})} className={className}>
      {children}
    </a>
  ),
  useParams: () => ({}),
  useSearch: () => search,
  useNavigate: () => navigate,
  useLocation: () => ({ pathname: "/users/groups/grp_friends", search: "" }),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

const now = Math.floor(Date.now() / 1000);
const person = (name: string, groupId: string, over: Record<string, unknown> = {}) => ({
  id: `usr_${name}`,
  name,
  groupId,
  groupName: groupId === "grp_friends" ? "friends" : "family",
  groupColor: "",
  status: UserStatus.ACTIVE,
  devicesUsed: 1,
  deviceLimit: 5,
  usedBytes: 12e9,
  quotaBytes: 100e9,
  expiresUnix: now + 20 * 86400,
  lastSeenUnix: now - 3600,
  online: false,
  currentNodeName: "",
  nextResetUnix: 0,
  speedLimitBps: 0,
  deviceSpeedLimitBps: 0,
  createdUnix: now - 86400,
  ...over,
});
let everyone: ReturnType<typeof person>[] = [];
const friends = () => ({ id: "grp_friends", name: "friends", profileIds: ["p_hy"], userCount: 2, dnsPresetId: "", happNodes: 2, amneziaNodes: 0, telegramNodes: 0, color: "sand", disabledNodeIds: [] });
const family = () => ({ id: "grp_family", name: "family", profileIds: ["p_hy", "p_awg"], userCount: 2, dnsPresetId: "", happNodes: 2, amneziaNodes: 1, telegramNodes: 0, color: "lavender", disabledNodeIds: [] });

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  role = Role.OWNER;
  search = {};
  shares = {};
  everyone = [person("bob", "grp_friends", { online: true, currentNodeName: "nl1" }), person("carol", "grp_friends"), person("alice", "grp_family"), person("erin", "grp_family")];
  listGroups.mockResolvedValue({ groups: [friends(), family()] });
  listUsers.mockImplementation(async (req: { groupId?: string; query?: string; pageSize: number; offset?: number }) => {
    let list = req.groupId ? everyone.filter((u) => u.groupId === req.groupId) : everyone;
    if (req.query) list = list.filter((u) => u.name.includes(req.query!));
    const counts = { all: list.length, online: list.filter((u) => u.online).length, expiring: 0, overQuota: 0, devicesUsed: list.length, devicesOnline: 1, overDeviceLimit: 0 };
    const from = req.offset ?? 0;
    return { users: list.slice(from, from + req.pageSize), nextPageToken: "", counts };
  });
  updateGroup.mockResolvedValue({ group: friends() });
  updateUser.mockResolvedValue({});
  deleteGroup.mockResolvedValue({});
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [listGroups, updateGroup, deleteGroup, listUsers, updateUser, navigate]) m.mockReset();
});

const settle = async () => {
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
async function mount(ui: ReactNode = <GroupScreen id="grp_friends" />) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>{ui}</ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const text = () => document.body.textContent ?? "";
// the buttons of a shut save bar are not there to press (inert): they are found only when asked for
const button = (label: string, shut = false) => [...document.querySelectorAll("button")].find((b) => (b.textContent?.trim() === label || b.getAttribute("aria-label") === label) && (shut || !b.closest("[inert]")));
const click = async (el: Element | undefined | null) => {
  await act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  await settle();
};
const by = (kind: string, name: string) => [...document.querySelectorAll(`[role=${kind}]`)].find((e) => e.getAttribute("aria-label") === name || e.textContent?.includes(name));

describe("the page of a group", () => {
  it("opens with the group's count, who is online, its profiles and the people of the group, asked by the group's id", async () => {
    await mount();
    expect(document.querySelector("h1")?.textContent).toBe("friends");
    expect(text()).toContain("2 people");
    expect(text()).toContain("1 online");
    expect(text()).toContain("1 profile");
    for (const n of ["bob", "carol"]) expect(text()).toContain(n);
    expect(text()).not.toContain("alice");
    expect(listUsers.mock.calls.some(([r]) => r.groupId === "grp_friends")).toBe(true);
    // the figures come from the counts of the group
    expect(text()).toContain("Online now");
    expect(text()).toContain("Over quota");
    const create = [...document.querySelectorAll<HTMLAnchorElement>("a")].find((a) => a.textContent?.includes("New person"))!;
    expect(create.dataset.search).toBe(JSON.stringify({ create: true, group: "grp_friends" }));
  });

  it("goes back to the users, and opens a person's page from a row", async () => {
    await mount();
    const links = [...document.querySelectorAll<HTMLAnchorElement>("a")];
    expect(links.find((a) => a.textContent === "Users")?.getAttribute("href")).toBe("/users");
    const bob = links.find((a) => a.textContent?.includes("bob"))!;
    expect(bob.getAttribute("href")).toBe("/users/$id");
    expect(bob.dataset.params).toBe(JSON.stringify({ id: "usr_bob" }));
  });

  it("finds a person in the group, or says nobody was found", async () => {
    await mount();
    const input = document.querySelector<HTMLInputElement>('input[aria-label="Find a person"]')!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "zz");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => void (await new Promise((r) => setTimeout(r, 320))));
    await settle();
    expect(text()).toContain("Nobody found");
    expect(listUsers.mock.calls.some(([r]) => r.query === "zz" && r.groupId === "grp_friends")).toBe(true);
  });

  it("an empty group says so and offers a new person or moving people in", async () => {
    everyone = [person("alice", "grp_family")];
    listGroups.mockResolvedValue({ groups: [{ ...friends(), userCount: 0 }, family()] });
    await mount();
    expect(text()).toContain("Nobody in the group yet");
    expect(button("Move people here")).toBeTruthy();
  });

  it("a group that does not exist says so", async () => {
    await mount(<GroupScreen id="grp_gone" />);
    expect(text()).toContain("No such group");
  });

  it("a click on a tab asks the router for it; the page opens on the tab of the address", async () => {
    await mount();
    await click(by("tab", "Access"));
    expect(navigate).toHaveBeenCalled();
    const arg = navigate.mock.calls.at(-1)![0] as { search: (p: object) => object };
    expect(arg.search({ page: 3 })).toMatchObject({ tab: "access" });
  });
});

describe("moving people in", () => {
  it("lists the people of the other groups, ticks them and moves them with UpdateUser", async () => {
    everyone = [person("bob", "grp_friends"), person("alice", "grp_family"), person("erin", "grp_family")];
    // family gives AmneziaWG too, friends do not: the move takes it away, and the panel asks once more
    await mount();
    await click(button("Move people here"));
    const box = by("group", "Who to move to “friends”")!;
    expect(box.textContent).toContain("alice");
    expect(box.textContent).toContain("erin");
    expect(box.textContent).not.toContain("bob");
    expect(button("Move")?.className).toContain("opacity-45");
    await click(by("checkbox", "alice"));
    expect(text()).toContain("Their set and limits become those of this group");
    await click(button("Move 1"));
    expect(updateUser).not.toHaveBeenCalled();
    expect(text()).toContain("AWG 3.1");
    await click(button("Move 1 anyway"));
    expect(updateUser).toHaveBeenCalledWith({ userId: "usr_alice", groupId: "grp_friends" });
    expect(text()).toContain("1 person moved to “friends”");
  });

  it("moves a person with nothing to lose at once, from the row's menu", async () => {
    await mount();
    const trigger = document.querySelector<HTMLElement>('button[aria-label="Actions: bob"]')!;
    await act(async () => {
      trigger.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, pointerType: "mouse" }));
      trigger.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0 }));
      trigger.click();
    });
    // the menu mounts through a portal after its trigger settles: under a loaded run six ticks are not always enough
    for (let i = 0; i < 40 && !document.querySelector("[role=menuitem]"); i++) await settle();
    const item = [...document.querySelectorAll("[role=menuitem]")].find((i) => i.textContent?.includes("Move to “family”"))!;
    expect(item).toBeTruthy();
    expect([...document.querySelectorAll("[role=menuitem]")].some((i) => i.textContent === "Open page")).toBe(true);
    await click(item);
    // friends has one profile, family has it too: nothing is taken away
    expect(updateUser).toHaveBeenCalledWith({ userId: "usr_bob", groupId: "grp_family" });
  });

  it("stops after the first failure and says how far it got", async () => {
    updateUser.mockResolvedValueOnce({}).mockRejectedValue(new Error("boom"));
    const r = await moveUsers(["a", "b", "c", "d", "e", "f"], "grp_x");
    expect(r.error).toBeInstanceOf(Error);
    expect(r.done).toBeLessThan(6);
    expect(updateUser.mock.calls.length).toBeLessThan(6 + 4);
  });

  it("knows what a move takes away from the two sets of profiles alone", () => {
    expect(lostProfileIds([{ profileIds: ["a", "b"] }], { profileIds: ["b", "c"] })).toEqual(["a"]);
    expect(lostProfileIds([{ profileIds: ["a"] }, { profileIds: ["a", "d"] }], { profileIds: ["a"] })).toEqual(["d"]);
    expect(lostProfileIds([{ profileIds: ["a"] }], { profileIds: ["a", "b"] })).toEqual([]);
  });
});

describe("the Access tab", () => {
  beforeEach(() => {
    search = { tab: "access" };
  });

  it("shows the profiles with a switch each and where they work now", async () => {
    await mount();
    const hy = document.querySelector<HTMLElement>('[role=switch][aria-label="hy2 · 443"]')!;
    const awg = document.querySelector<HTMLElement>('[role=switch][aria-label="AWG 3.1"]')!;
    expect(hy.getAttribute("aria-checked")).toBe("true");
    expect(awg.getAttribute("aria-checked")).toBe("false");
    expect(text()).toContain("Subscription (Happ) · 2 nodes");
    expect(text()).toContain("AmneziaVPN keys · no AmneziaWG of the group runs on an enabled node");
    // nothing changed yet: the bar is shut
    expect(button("Save", true)?.closest("[inert]")).toBeTruthy();
    expect(button("Save")).toBeUndefined();
  });

  it("opens the bar when a profile is added and saves the set, naming only the profiles", async () => {
    await mount();
    await click(document.querySelector('[role=switch][aria-label="AWG 3.1"]'));
    expect(text()).toContain("Changes not saved");
    expect(button("Save")).toBeTruthy();
    await click(button("Save"));
    expect(updateGroup).toHaveBeenCalledTimes(1);
    expect(updateGroup.mock.calls[0]![0]).toEqual({ groupId: "grp_friends", profileIds: { values: ["p_hy", "p_awg"] } });
    expect(text()).toContain("Saved");
  });

  it("asks the server what a profile taken away does to the people, and takes it once the red button is pressed", async () => {
    updateGroup.mockImplementation(async (req: { dryRun?: boolean }) =>
      req.dryRun ? { group: friends(), impact: { users: 2, lost: [{ id: "p_hy", name: "hy2 · 443", protocol: "hysteria2", awgDevices: 0 }], gained: [] } } : { group: friends() },
    );
    await mount();
    await click(document.querySelector('[role=switch][aria-label="hy2 · 443"]'));
    await click(button("Save"));
    expect(updateGroup).toHaveBeenCalledTimes(1);
    expect(updateGroup.mock.calls[0]![0]).toMatchObject({ dryRun: true });
    expect(text()).toContain("2 people lose:");
    await click(button("Take it away from 2 people"));
    expect(updateGroup).toHaveBeenCalledTimes(2);
    expect(updateGroup.mock.calls[1]![0]).toEqual({ groupId: "grp_friends", profileIds: { values: [] } });
  });

  it("cancel puts the switches back", async () => {
    await mount();
    await click(document.querySelector('[role=switch][aria-label="AWG 3.1"]'));
    await click(button("Cancel"));
    expect(document.querySelector('[role=switch][aria-label="AWG 3.1"]')?.getAttribute("aria-checked")).toBe("false");
    expect(updateGroup).not.toHaveBeenCalled();
  });

  it("turns nodes off one by one once “All nodes” is off, and saves the nodes that are off", async () => {
    await mount();
    expect(document.querySelector('[role=switch][aria-label="All nodes"]')?.getAttribute("aria-checked")).toBe("true");
    // the nodes are locked while "All nodes" is on
    await click(by("checkbox", "de1"));
    expect(updateGroup).not.toHaveBeenCalled();
    await click(document.querySelector('[role=switch][aria-label="All nodes"]'));
    await click(by("checkbox", "de1"));
    expect(by("checkbox", "de1")?.getAttribute("aria-checked")).toBe("false");
    await click(button("Save"));
    expect(updateGroup).toHaveBeenCalledWith({ groupId: "grp_friends", disabledNodeIds: { values: ["nod_de1"] } });
  });

  it("says where to give a node the owner holds back when the panel refuses to switch it on (node_held)", async () => {
    // nl1 is off for this group; the node's own page is where it is given, not this one
    listGroups.mockResolvedValue({ groups: [{ ...friends(), disabledNodeIds: ["nod_nl1"] }, family()] });
    updateGroup.mockRejectedValue(new ConnectError("node_held: node=nod_nl1", Code.FailedPrecondition));
    await mount();
    await click(by("checkbox", "nl1"));
    await click(button("Save"));
    expect(text()).toContain("“nl1” is not handed out to this group");
    const open = [...document.querySelectorAll<HTMLAnchorElement>("a")].find((a) => a.textContent === "Open the node");
    expect(open?.getAttribute("href")).toBe("/nodes/$id");
    expect(open?.dataset.params).toBe('{"id":"nod_nl1"}');
    // a new try starts clean
    await click(button("Cancel"));
    expect(text()).not.toContain("is not handed out to this group");
  });

  it("locks a node the owner holds back and this group was not given, before any click (node_held)", async () => {
    // nl1 is marked "nobody gets it": it is on this group's disabled list, and the panel would refuse to switch it on
    shares = { nod_nl1: { mode: NodeShareMode.NONE } };
    listGroups.mockResolvedValue({ groups: [{ ...friends(), disabledNodeIds: ["nod_nl1"] }, family()] });
    await mount();
    const row = by("checkbox", "nl1");
    expect(row?.getAttribute("aria-disabled")).toBe("true");
    expect(row?.getAttribute("title")).toContain("Give it on the node’s page");
    expect(row?.textContent).toContain("not handed out to this group");
    await click(row);
    expect(row?.getAttribute("aria-checked")).toBe("false");
    expect(button("Save")).toBeUndefined();
    expect(updateGroup).not.toHaveBeenCalled();
    // "All nodes" would switch it on too: that switch stays off and says why
    const all = document.querySelector<HTMLElement>('[role=switch][aria-label="All nodes"]');
    expect(all?.hasAttribute("data-disabled") || all?.getAttribute("aria-disabled") === "true").toBe(true);
    expect(text()).toContain("Held back by the owner: 1");
    // another node still turns off as usual
    await click(by("checkbox", "de1"));
    expect(by("checkbox", "de1")?.getAttribute("aria-checked")).toBe("false");
  });

  it("a held-back node the group was given turns off and back on (only a node it was not given is locked)", async () => {
    shares = { nod_nl1: { mode: NodeShareMode.LISTED }, nod_de1: { mode: NodeShareMode.ALL } };
    listGroups.mockResolvedValue({ groups: [{ ...friends(), disabledNodeIds: ["nod_de1"] }, family()] });
    await mount();
    expect(by("checkbox", "de1")?.getAttribute("aria-disabled")).toBeNull();
    expect(by("checkbox", "nl1")?.getAttribute("aria-disabled")).toBeNull();
    await click(by("checkbox", "nl1"));
    expect(by("checkbox", "nl1")?.getAttribute("aria-checked")).toBe("false");
    await click(by("checkbox", "nl1"));
    expect(by("checkbox", "nl1")?.getAttribute("aria-checked")).toBe("true");
  });

  it("saves the DNS preset the moment it is picked", async () => {
    await mount();
    await click([...document.querySelectorAll("button")].find((b) => b.getAttribute("aria-expanded") === "false" && b.textContent?.includes("Default")));
    await click([...document.querySelectorAll("[role=radio]")].find((r) => r.querySelector("b")?.textContent === "Cloudflare"));
    expect(updateGroup).toHaveBeenCalledWith({ groupId: "grp_friends", dnsPresetId: "dns_cf" });
  });

  it("a group with no profile says it gives nothing", async () => {
    listGroups.mockResolvedValue({ groups: [{ ...friends(), profileIds: [], happNodes: 0 }, family()] });
    await mount();
    expect(text()).toContain("gets nothing yet");
  });
});

describe("the Settings tab", () => {
  beforeEach(() => {
    search = { tab: "settings" };
  });

  const type = async (input: HTMLInputElement, value: string) => {
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  };

  it("renames the group and saves the name alone", async () => {
    await mount();
    const input = document.querySelector<HTMLInputElement>("input")!;
    expect(input.value).toBe("friends");
    await type(input, "friends and family");
    expect(text()).toContain("Changes not saved");
    await click(button("Save"));
    expect(updateGroup).toHaveBeenCalledWith({ groupId: "grp_friends", name: "friends and family" });
  });

  it("changes the colour of the tag", async () => {
    await mount();
    await click(button("Rose") ?? [...document.querySelectorAll("[aria-pressed]")].find((b) => b.getAttribute("title")?.toLowerCase().includes("rose")));
    await click(button("Save"));
    expect(updateGroup.mock.calls[0]![0]).toMatchObject({ groupId: "grp_friends", color: "rose" });
  });

  it("keeps a group with people from being deleted, and says to move them first", async () => {
    await mount();
    expect(text()).toContain("Move 2 people to another group first");
    await click(button("Delete group"));
    await click(button("Delete group"));
    expect(deleteGroup).not.toHaveBeenCalled();
  });

  it("moves everybody to the other group after a second press", async () => {
    await mount();
    await click(button("Move to “family”"));
    expect(updateUser).not.toHaveBeenCalled();
    expect(button("Really move 2?")).toBeTruthy();
    await click(button("Really move 2?"));
    expect(updateUser.mock.calls.map(([r]) => r.userId).sort()).toEqual(["usr_bob", "usr_carol"]);
    expect(updateUser.mock.calls.every(([r]) => r.groupId === "grp_family")).toBe(true);
  });

  it("deletes an empty group after a second press and says it is gone", async () => {
    listGroups.mockResolvedValue({ groups: [{ ...friends(), userCount: 0 }, family()] });
    await mount();
    expect(text()).toContain("deleting it breaks nothing");
    await click(button("Delete group"));
    expect(deleteGroup).not.toHaveBeenCalled();
    await click(button("Really delete?"));
    expect(deleteGroup).toHaveBeenCalledWith({ groupId: "grp_friends", moveUsersTo: "" });
    expect(text()).toContain("Group deleted");
    expect(text()).toContain("To users");
  });
});

describe("an observer", () => {
  it("sees the group and nothing that changes it", async () => {
    role = Role.READONLY;
    await mount();
    expect(text()).toContain("only the owner and a helper change it");
    expect(button("Move people here")).toBeFalsy();
    expect([...document.querySelectorAll<HTMLAnchorElement>("a")].some((a) => a.textContent?.includes("New person"))).toBe(false);
  });

  it("finds the switches of Access shut and no danger zone in Settings", async () => {
    role = Role.READONLY;
    search = { tab: "access" };
    await mount();
    expect(document.querySelector('[role=switch][aria-label="AWG 3.1"]')?.hasAttribute("data-disabled")).toBe(true);
    await act(async () => root?.unmount());
    host?.remove();
    search = { tab: "settings" };
    await mount();
    expect(text()).not.toContain("Danger zone");
  });
});
