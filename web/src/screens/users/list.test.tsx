import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { UserFilter, UserSort, UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { setLang } from "@/i18n";
import { groupTone } from "./format";
import { UsersScreen } from "./list";

const listUsers = vi.fn();
const createUser = vi.fn();
const getUser = vi.fn();
const getSubscriptionLink = vi.fn();
const extendUsers = vi.fn();
const resetUserTraffic = vi.fn();
const setUsersEnabled = vi.fn();
const navigate = vi.fn();
let search: Record<string, unknown> = {};
let role = Role.OWNER;
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  users: {
    listUsers: (...a: unknown[]) => listUsers(...a),
    createUser: (...a: unknown[]) => createUser(...a),
    getUser: (...a: unknown[]) => getUser(...a),
    getSubscriptionLink: (...a: unknown[]) => getSubscriptionLink(...a),
    extendUsers: (...a: unknown[]) => extendUsers(...a),
    resetUserTraffic: (...a: unknown[]) => resetUserTraffic(...a),
    setUsersEnabled: (...a: unknown[]) => setUsersEnabled(...a),
  },
  groups: {
    listGroups: () =>
      Promise.resolve({
        groups: [
          { id: "grp_empty", name: "empty", profileIds: [], userCount: 2, dnsPresetId: "", happNodes: 0, amneziaNodes: 0 },
          { id: "grp_ok", name: "ok", profileIds: ["p_hy"], userCount: 1, dnsPresetId: "", happNodes: 1, amneziaNodes: 0 },
        ],
      }),
  },
  profiles: { listProfiles: () => Promise.resolve({ profiles: [{ id: "p_hy", name: "hy2", protocol: "hysteria2" }] }), listProtocols: () => Promise.resolve({ protocols: [] }) },
  nodes: { listNodes: () => Promise.resolve({ nodes: [] }) },
  dns: { listDnsPresets: () => Promise.resolve({ presets: [], providers: [], clientSupport: [] }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role } }) },
}));
vi.mock("@tanstack/react-router", () => ({
  useSearch: () => search,
  useNavigate: () => navigate,
  useBlocker: () => ({ status: "idle" }),
  Link: ({ children, to, params, search: s, className, onClick, ...rest }: { children?: ReactNode; to: string; params?: object; search?: object; className?: string; onClick?: () => void }) => (
    <a href={to} data-params={JSON.stringify(params ?? {})} data-search={JSON.stringify(s ?? {})} className={className} onClick={onClick} aria-label={(rest as { "aria-label"?: string })["aria-label"]} data-tone={(rest as { "data-tone"?: string })["data-tone"]}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

const DAY = 86400;
const now = () => Math.floor(Date.now() / 1000);
const person = (over: Record<string, unknown>) => ({
  id: "usr_x",
  name: "x",
  groupId: "grp_empty",
  groupName: "empty",
  status: UserStatus.ACTIVE,
  apps: { happ: true, amnezia: false },
  via: [],
  devicesUsed: 0,
  deviceLimit: 5,
  usedBytes: 0n,
  quotaBytes: 0n,
  expiresUnix: 0n,
  lastSeenUnix: 0n,
  nextResetUnix: 0n,
  speedLimitBps: 0n,
  createdUnix: 0n,
  accessHapp: false,
  accessAmnezia: false,
  ...over,
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const written: string[] = [];
beforeEach(() => {
  search = {};
  role = Role.OWNER;
  listUsers.mockResolvedValue({
    users: [
      person({ id: "usr_a", name: "alice" }),
      person({ id: "usr_b", name: "bob", groupId: "grp_ok", groupName: "ok", accessHapp: true, lastSeenUnix: BigInt(now() - 3600) }),
      person({ id: "usr_c", name: "carol", status: UserStatus.DISABLED }),
    ],
    nextPageToken: "",
    counts: { all: 3, online: 0, expiring: 0, overQuota: 0, devicesUsed: 4, devicesOnline: 0 },
  });
  getUser.mockResolvedValue({ user: person({ id: "usr_b", name: "bob" }), devices: [], dailyTraffic: [], nodeTraffic: [], profiles: [], nodeAccess: [], blockedDevices: [] });
  getSubscriptionLink.mockResolvedValue({ url: "https://sub.example.com/s/bob-7f3k9q", pagePassword: "lake-2718-moss" });
  extendUsers.mockResolvedValue({ users: [] });
  resetUserTraffic.mockResolvedValue({ users: [] });
  setUsersEnabled.mockResolvedValue({ users: [] });
  Object.assign(navigator, { clipboard: { writeText: (s: string) => (written.push(s), Promise.resolve()) } });
});
afterEach(() => {
  setLang("en");
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [listUsers, createUser, getUser, getSubscriptionLink, extendUsers, resetUserTraffic, setUsersEnabled, navigate]) m.mockReset();
  written.length = 0;
});

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <UsersScreen />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async () => {
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const type = (el: HTMLInputElement, value: string) =>
  act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
/** The row of a person: the card of its name, and the button that opens it. */
const rowOf = (name: string) => document.querySelector<HTMLButtonElement>(`section button[aria-label='${name}']`)!.parentElement!;
const opener = (name: string) => document.querySelector<HTMLButtonElement>(`section button[aria-label='${name}']`)!;
const lastCall = () => listUsers.mock.calls.map((c) => c[0] as Record<string, unknown>).filter((r) => r.pageSize !== 1).at(-1)!;

describe("the people table", () => {
  it("names the group of each person as a link to the group's page, in the colour stored on the group", async () => {
    listUsers.mockResolvedValue({
      users: [
        person({ id: "usr_a", name: "alice", groupColor: "mint" }),
        person({ id: "usr_b", name: "bob", groupId: "grp_ok", groupName: "ok", accessHapp: true, groupColor: "rose" }),
        person({ id: "usr_c", name: "carol", groupId: "grp_old", groupName: "old", accessHapp: true, groupColor: "" }),
      ],
      nextPageToken: "",
      counts: { all: 3, online: 0, expiring: 0, overQuota: 0 },
    });
    await mount();
    const tag = (name: string) => rowOf(name).querySelector<HTMLAnchorElement>("a[data-tone]")!;
    expect(tag("bob").getAttribute("href")).toBe("/users/groups/$id");
    expect(JSON.parse(tag("bob").dataset.params!)).toEqual({ id: "grp_ok" });
    expect(tag("bob").className).toContain("tone-chip");
    expect(tag("bob").dataset.tone).toBe("rose"); // not what its id would give
    expect(groupTone("grp_ok")).not.toBe("rose");
    expect(tag("alice").dataset.tone).toBe("mint");
    expect(tag("carol").dataset.tone).toBe(groupTone("grp_old")); // none stored: the id's tone
  });

  it("says in the line under the name that an active person gets nothing, and why", async () => {
    await mount();
    expect(rowOf("alice").textContent).toContain("No access · Gets nothing yet: no profile of the group runs on a node");
    expect(rowOf("bob").textContent).not.toContain("No access"); // bob gets something
    expect(rowOf("carol").textContent).not.toContain("No access"); // off: not "no access"
  });

  it("says the term in days (amber in the last week), expired in red, no expiry, and off for a disabled person", async () => {
    listUsers.mockResolvedValue({
      users: [
        person({ id: "usr_a", name: "alice", accessHapp: true, expiresUnix: BigInt(now() + 41 * DAY + 3600) }),
        person({ id: "usr_b", name: "bob", accessHapp: true, expiresUnix: BigInt(now() + 3 * DAY - 3600) }),
        person({ id: "usr_c", name: "carol", accessHapp: true, status: UserStatus.EXPIRED, expiresUnix: BigInt(now() - 2 * DAY) }),
        person({ id: "usr_d", name: "dave", accessHapp: true }),
        person({ id: "usr_e", name: "erin", accessHapp: true, status: UserStatus.DISABLED, expiresUnix: BigInt(now() + 9 * DAY) }),
      ],
      nextPageToken: "",
      counts: { all: 5, online: 0, expiring: 1, overQuota: 0 },
    });
    await mount();
    const term = (name: string) => [...rowOf(name).querySelectorAll<HTMLElement>("span")].find((s) => /days?$|expired|no expiry|disabled$/.test(s.textContent ?? "") && s.children.length === 0)!;
    expect(term("alice").textContent).toBe("42 days");
    expect(term("alice").className).not.toContain("warn");
    expect(term("bob").textContent).toBe("3 days");
    expect(term("bob").className).toContain("text-warn-text");
    expect(term("carol").textContent).toBe("expired");
    expect(term("carol").className).toContain("text-danger-text");
    expect(term("dave").textContent).toBe("no expiry");
    expect(term("erin").textContent).toBe("disabled");
  });

  it("paints the devices amber when the limit is reached and names a person who is over it", async () => {
    listUsers.mockResolvedValue({
      users: [person({ id: "usr_a", name: "alice", devicesUsed: 6 }), person({ id: "usr_b", name: "bob", devicesUsed: 5 }), person({ id: "usr_c", name: "carol", devicesUsed: 2 })],
      nextPageToken: "",
      counts: { all: 3, online: 0, expiring: 0, overQuota: 0, overDeviceLimit: 1 },
    });
    await mount();
    const dev = (name: string) => [...rowOf(name).querySelectorAll<HTMLElement>("b")].find((b) => /^\d+\/\d+$/.test(b.textContent ?? ""))!;
    expect(dev("alice").className).toContain("text-warn-text");
    expect(dev("alice").parentElement!.getAttribute("title")).toBe("Over device limit");
    expect(dev("bob").className).toContain("text-warn-text");
    expect(dev("carol").className).not.toContain("text-warn-text");
    expect(dev("carol").textContent).toBe("2/5");
  });

  it("shows the word now with a dot and the node as a link to it; anything else is how long ago", async () => {
    listUsers.mockResolvedValue({
      users: [
        person({ id: "usr_on", name: "alice", online: true, accessHapp: true, currentNodeId: "nod_1", currentNodeName: "de1" }),
        person({ id: "usr_off", name: "bob", online: false, accessHapp: true, lastSeenUnix: BigInt(now() - 13 * 60) }),
        person({ id: "usr_new", name: "carol", online: false, accessHapp: true }),
      ],
      nextPageToken: "",
      counts: { all: 3, online: 1, expiring: 0, overQuota: 0 },
    });
    await mount();
    const seen = (name: string) => rowOf(name).children[6] as HTMLElement;
    expect(seen("alice").textContent).toBe("now·de1");
    expect(seen("alice").className).toContain("text-fg");
    const node = seen("alice").querySelector<HTMLAnchorElement>("a")!;
    expect(node.getAttribute("href")).toBe("/nodes/$id");
    expect(JSON.parse(node.dataset.params!)).toEqual({ id: "nod_1" });
    expect(seen("bob").textContent).toBe("13 min ago");
    expect(seen("bob").className).toContain("text-muted");
    expect(seen("carol").textContent).toBe("never online");
  });

  it("shows the apps as a link icon chip, a keys icon chip, both or nothing, with the whole wording for a screen reader", async () => {
    listUsers.mockResolvedValue({
      users: [
        person({ id: "usr_l", name: "alice", accessHapp: true, via: [App.HAPP] }),
        person({ id: "usr_k", name: "bob", accessHapp: true, via: [App.AMNEZIA] }),
        person({ id: "usr_b", name: "carol", accessHapp: true, via: [App.HAPP, App.AMNEZIA] }),
        person({ id: "usr_n", name: "dave", accessHapp: true, via: [] }),
      ],
      nextPageToken: "",
      counts: { all: 4, online: 0, expiring: 0, overQuota: 0 },
    });
    await mount();
    const via = (name: string) => [...rowOf(name).querySelectorAll<HTMLElement>('[role="img"]')].find((e) => e.querySelector(".tone-chip"));
    const chips = (name: string) => [...via(name)!.querySelectorAll<HTMLElement>(".tone-chip")].map((c) => `${c.dataset.tone}:${c.title}:${c.textContent}`);
    expect(chips("alice")).toEqual(["sky:Subscription link:"]);
    expect(via("alice")!.getAttribute("aria-label")).toBe("Subscription link");
    expect(chips("bob")).toEqual(["mint:AmneziaVPN keys:"]);
    expect(chips("carol")).toEqual(["sky:Subscription link:", "mint:AmneziaVPN keys:"]);
    expect(via("carol")!.getAttribute("aria-label")).toBe("Link + AmneziaVPN keys");
    expect(via("dave")).toBeUndefined();
  });

  it("dims a disabled person's cells", async () => {
    await mount();
    expect(rowOf("carol").querySelector(".opacity-50")).not.toBeNull();
    expect(rowOf("bob").querySelector(".opacity-50")).toBeNull();
  });

  it("names the people and the devices under the title, and who is online", async () => {
    await mount();
    expect(document.querySelector("h1")!.parentElement!.textContent).toContain("3 people · 4 devices · 0 online");
  });
});

describe("an open row", () => {
  const withDevices = () =>
    getUser.mockResolvedValue({
      user: person({ id: "usr_b", name: "bob" }),
      devices: [
        { id: "d1", platform: "ios", model: "Phone", client: "Happ/3.4.2", online: true, lastSeenUnix: BigInt(now() - 30), firstSeenUnix: 0n, lastHandshakeUnix: 0n, usedBytes: 0n, idleSinceUnix: 0n },
        { id: "d2", platform: "windows", model: "Laptop", client: "Klick/1.0", online: false, lastSeenUnix: BigInt(now() - 3 * 3600), firstSeenUnix: 0n, lastHandshakeUnix: 0n, usedBytes: 0n, idleSinceUnix: 0n },
      ],
      dailyTraffic: [],
      nodeTraffic: [],
      profiles: [],
      nodeAccess: [],
      blockedDevices: [],
    });

  it("opens on a click and folds on the next, with only one open at a time", async () => {
    await mount();
    expect(opener("bob").getAttribute("aria-expanded")).toBe("false");
    await click(opener("bob"));
    await settle();
    expect(opener("bob").getAttribute("aria-expanded")).toBe("true");
    await click(opener("alice"));
    await settle();
    expect(opener("bob").getAttribute("aria-expanded")).toBe("false");
    expect(opener("alice").getAttribute("aria-expanded")).toBe("true");
    await click(opener("alice"));
    expect(opener("alice").getAttribute("aria-expanded")).toBe("false");
  });

  it("shows the link, the hidden page password (Show reveals it), the devices from the person's card and the page link", async () => {
    withDevices();
    await mount();
    await click(opener("bob"));
    await settle();
    expect(getUser).toHaveBeenCalledWith({ userId: "usr_b" }, expect.anything());
    const card = rowOf("bob").parentElement!;
    expect(card.textContent).toContain("https://sub.example.com/s/bob-7f3k9q");
    expect(card.textContent).toContain("••••••••••••");
    expect(card.textContent).not.toContain("lake-2718-moss");
    await click([...card.querySelectorAll("button")].find((b) => b.textContent === "Show"));
    expect(card.textContent).toContain("lake-2718-moss");
    expect(card.textContent).toContain("Phone");
    expect(card.textContent).toContain("Laptop");
    expect(card.textContent).toContain("Happ");
    expect(card.textContent).toContain("3 h ago");
    const page = [...card.querySelectorAll<HTMLAnchorElement>("a")].find((a) => a.textContent === "Person’s page")!;
    expect(page.getAttribute("href")).toBe("/users/$id");
    expect(JSON.parse(page.dataset.params!)).toEqual({ id: "usr_b" });
  });

  it("says the page asks for no password when the link has none", async () => {
    getSubscriptionLink.mockResolvedValue({ url: "https://sub.example.com/s/bob-7f3k9q", pagePassword: "" });
    await mount();
    await click(opener("bob"));
    await settle();
    expect(rowOf("bob").parentElement!.textContent).toContain("not set, anyone with the link can open the page");
  });

  it("leads with a line about what is wrong: disabled, expired, over the quota, device places full", async () => {
    listUsers.mockResolvedValue({
      users: [
        person({ id: "usr_a", name: "alice", accessHapp: true, status: UserStatus.DISABLED }),
        person({ id: "usr_b", name: "bob", accessHapp: true, status: UserStatus.LIMITED, nextResetUnix: BigInt(now() + 5 * DAY) }),
        person({ id: "usr_c", name: "carol", accessHapp: true, devicesUsed: 5, deviceLimit: 5, lastSeenUnix: BigInt(now() - 100) }),
        person({ id: "usr_d", name: "dave", accessHapp: true }),
      ],
      nextPageToken: "",
      counts: { all: 4, online: 0, expiring: 0, overQuota: 0 },
    });
    await mount();
    const warn = async (name: string) => {
      await click(opener(name));
      await settle();
      return rowOf(name).parentElement!.textContent ?? "";
    };
    expect(await warn("alice")).toContain("Disabled by you. The link doesn’t work");
    expect(await warn("bob")).toContain("Quota used up, access is paused until the reset on");
    expect(await warn("carol")).toContain("All 5 slots are taken");
    expect(await warn("dave")).toContain("Hasn’t connected yet. Send the link");
  });

  it("extends by 30 days, resets the traffic and switches off with a way back", async () => {
    listUsers.mockResolvedValue({
      users: [person({ id: "usr_b", name: "bob", accessHapp: true, expiresUnix: BigInt(now() + 9 * DAY), usedBytes: 5000000000n, lastSeenUnix: BigInt(now() - 60) })],
      nextPageToken: "",
      counts: { all: 1, online: 0, expiring: 0, overQuota: 0 },
    });
    await mount();
    await click(opener("bob"));
    await settle();
    const card = rowOf("bob").parentElement!;
    const act1 = (label: string) => [...card.querySelectorAll("button")].find((b) => b.textContent === label);
    await click(act1("Extend by 30 days"));
    await settle();
    expect(extendUsers).toHaveBeenCalledWith({ userIds: ["usr_b"], days: 30 });
    await click(act1("Reset traffic"));
    await settle();
    expect(resetUserTraffic).toHaveBeenCalledWith({ userIds: ["usr_b"] });
    await click(act1("Disable"));
    await settle();
    expect(setUsersEnabled).toHaveBeenCalledWith({ userIds: ["usr_b"], enabled: false });
    // the toast gives it back
    await click(button("Undo"));
    await settle();
    expect(setUsersEnabled).toHaveBeenLastCalledWith({ userIds: ["usr_b"], enabled: true });
  });

  it("offers the link window (QR) to the owner", async () => {
    await mount();
    await click(opener("bob"));
    await settle();
    await click([...rowOf("bob").parentElement!.querySelectorAll("button")].find((b) => b.textContent === "Link and QR"));
    await settle();
    expect(document.querySelector("[role=dialog]")?.textContent).toContain("bob");
  });

  it("gives an observer the card's link and nothing that changes anything", async () => {
    role = Role.READONLY;
    await mount();
    await click(opener("bob"));
    await settle();
    const card = rowOf("bob").parentElement!;
    expect(getSubscriptionLink).not.toHaveBeenCalled();
    expect(card.textContent).not.toContain("Subscription link");
    for (const label of ["Extend by 30 days", "Reset traffic", "Disable", "Link and QR"]) expect([...card.querySelectorAll("button")].some((b) => b.textContent === label)).toBe(false);
    expect([...card.querySelectorAll("a")].some((a) => a.textContent === "Person’s page")).toBe(true);
  });
});

describe("selecting", () => {
  it("opens a check column on Select, ticks a person by a click on the row instead of opening it, and shows the bar", async () => {
    await mount();
    expect(document.querySelector("[role=toolbar]")).toBeNull();
    await click(button("Select"));
    expect(button("Done")).toBeDefined();
    await click(opener("bob"));
    expect(opener("bob").getAttribute("aria-pressed")).toBe("true");
    expect(opener("bob").getAttribute("aria-expanded")).toBeNull();
    const bar = document.querySelector("[role=toolbar]")!;
    expect(bar.textContent).toContain("1 person");
    expect(bar.textContent).toContain("select all 3");
    await click(opener("bob"));
    expect(document.querySelector("[role=toolbar]")).toBeNull();
  });

  it("selects every shown person, and Done clears them", async () => {
    await mount();
    await click(button("Select"));
    await click(opener("alice"));
    await click([...document.querySelectorAll("[role=toolbar] button")].find((b) => b.textContent === "select all 3"));
    expect(document.querySelector("[role=toolbar]")!.textContent).toContain("3 people");
    expect(document.querySelector("[role=toolbar]")!.textContent).toContain("clear all");
    await click(button("Done"));
    expect(document.querySelector("[role=toolbar]")).toBeNull();
  });

  it("gives the helper Select and the observer none", async () => {
    role = Role.HELPER;
    await mount();
    expect(button("Select")).toBeDefined();
    act(() => root?.unmount());
    host?.remove();
    role = Role.READONLY;
    await mount();
    expect(button("Select")).toBeUndefined();
    expect([...document.querySelector("h1")!.parentElement!.querySelectorAll("button")]).toEqual([]); // no "Person" in the header
    expect(document.querySelector("section button[aria-label='bob']")).not.toBeNull(); // the list is there
  });
});

describe("the people list, filtered and found", () => {
  const chip = (label: string) => [...document.querySelectorAll("[role=radio]")].find((r) => r.textContent?.startsWith(label));

  it("shows the chips with their counts, and hides Expiring and Over quota while nobody is", async () => {
    listUsers.mockResolvedValue({ users: [person({ id: "usr_a", name: "alice", accessHapp: true })], nextPageToken: "", counts: { all: 9, online: 4, expiring: 0, overQuota: 2 } });
    await mount();
    expect(chip("All")?.textContent).toBe("All9");
    expect(chip("Online now")?.textContent).toBe("Online now4");
    expect(chip("Expiring soon")).toBeUndefined();
    expect(chip("Over quota")?.textContent).toBe("Over quota2");
  });

  it("asks the server for the filter, from page 1, and for the search after a short pause", async () => {
    await mount();
    await click(chip("Online now"));
    await settle();
    expect(lastCall()).toMatchObject({ filter: UserFilter.ONLINE, offset: 0 });
    await type(document.querySelector<HTMLInputElement>("input[aria-label='Find a person']")!, "ali");
    await act(async () => void (await new Promise((r) => setTimeout(r, 300))));
    await settle();
    expect(lastCall()).toMatchObject({ filter: UserFilter.ONLINE, query: "ali" });
  });

  it("says nobody was found when a search finds none, and Reset brings everybody back", async () => {
    listUsers.mockImplementation(async (r: { query?: string }) => ({ users: r.query ? [] : [person({ id: "usr_a", name: "alice", accessHapp: true })], nextPageToken: "", counts: { all: 1, online: 0, expiring: 0, overQuota: 0 } }));
    await mount();
    await type(document.querySelector<HTMLInputElement>("input[aria-label='Find a person']")!, "zzz");
    await act(async () => void (await new Promise((r) => setTimeout(r, 300))));
    await settle();
    expect(text()).toContain("Nobody found");
    await click(button("Reset"));
    await act(async () => void (await new Promise((r) => setTimeout(r, 300))));
    await settle();
    expect(text()).not.toContain("Nobody found");
    expect(rowOf("alice")).toBeDefined();
  });

  it("shows the first state, with a button to add a person, while there is nobody", async () => {
    listUsers.mockResolvedValue({ users: [], nextPageToken: "", counts: { all: 0, online: 0, expiring: 0, overQuota: 0 } });
    await mount();
    expect(text()).toContain("Add your first person,");
    expect(text()).toContain("Name and group");
    expect(document.querySelector("input[aria-label='Find a person']")).toBeNull();
    expect(button("New person")).toBeDefined();
  });

  it("keeps the list to one group when the address names it, and takes the filter off with a click", async () => {
    search = { group: "grp_ok" };
    await mount();
    expect(lastCall()).toMatchObject({ groupId: "grp_ok" });
    await click(document.querySelector("button[aria-label='Show everybody']") ?? [...document.querySelectorAll("button")].find((b) => b.textContent?.startsWith("Group:")));
    expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ to: "/users", replace: true }));
  });

  it("opens the Groups tab by its address", async () => {
    await mount();
    await click([...document.querySelectorAll("[role=radio]")].find((x) => x.textContent === "Groups"));
    expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ to: "/users", search: { tab: "groups" } }));
    act(() => root?.unmount());
    host?.remove();
    search = { tab: "groups" };
    await mount();
    expect(button("Group")).toBeDefined();
    expect(text()).toContain("2 groups");
  });
});

describe("a new person", () => {
  it("does not copy the link of a person who gets nothing yet: the window says why", async () => {
    createUser.mockResolvedValue({ user: person({ id: "usr_n", name: "erin" }), subscriptionUrl: "https://sub.example.com/p/T", pagePassword: "" });
    await mount();
    await click(button("Person"));
    await settle();
    await type(document.querySelector<HTMLInputElement>("input[placeholder='For example, carol']")!, "erin");
    await click(button("Create"));
    await settle();
    expect(createUser).toHaveBeenCalled();
    expect(written).toEqual([]);
    expect(text()).toContain("erin gets nothing yet: no profile of the group “empty” runs on a node.");
  });

  it("copies the link of a new person who gets something, shows the done screen with the link and the page password", async () => {
    createUser.mockResolvedValue({ user: person({ id: "usr_n", name: "erin", accessHapp: true }), subscriptionUrl: "https://sub.example.com/p/T", pagePassword: "tide-8132-pine" });
    await mount();
    await click(button("Person"));
    await settle();
    await type(document.querySelector<HTMLInputElement>("input[placeholder='For example, carol']")!, "erin");
    await click(button("Create"));
    await settle();
    expect(written).toEqual(["Link: https://sub.example.com/p/T\nPage password: tide-8132-pine"].map((s) => written[0] ?? s));
    expect(written[0]).toContain("https://sub.example.com/p/T");
    expect(written[0]).toContain("tide-8132-pine");
    const dialog = document.querySelector("[data-shot=create-user]")!;
    expect(dialog.textContent).toContain("erin added");
    expect(dialog.textContent).toContain("tide-8132-pine");
    await click([...dialog.querySelectorAll("button")].find((b) => b.textContent === "Add another"));
    await settle();
    expect(document.querySelector<HTMLInputElement>("input[placeholder='For example, carol']")!.value).toBe("");
  });
});

describe("the people list, in pages", () => {
  const many = (n: number, from = 1) => Array.from({ length: n }, (_, i) => person({ id: `usr_${from + i}`, name: `user${String(from + i).padStart(3, "0")}`, accessHapp: true }));
  const pages = (all: number, over: Record<string, number> = {}) => ({ nextPageToken: "", counts: { all, online: 3, expiring: 2, overQuota: 1, ...over } });

  it("asks the server for the first page and shows how many there are", async () => {
    listUsers.mockResolvedValue({ users: many(50), ...pages(137) });
    await mount();
    expect(listUsers.mock.calls[0]![0]).toMatchObject({ pageSize: 50, offset: 0 });
    expect(text()).toContain("Showing 1–50 of 137");
    expect(document.querySelector("button[aria-label='Page 3']")).not.toBeNull();
    expect(document.querySelector("button[aria-label='Previous page']")).toHaveProperty("disabled", true);
  });

  it("asks for the rows under the page of the URL, and moves the URL when a page is clicked", async () => {
    search = { page: 2, size: 25 };
    listUsers.mockResolvedValue({ users: many(25, 26), ...pages(137) });
    await mount();
    expect(listUsers.mock.calls[0]![0]).toMatchObject({ pageSize: 25, offset: 25 });
    expect(text()).toContain("Showing 26–50 of 137");
    await click(document.querySelector("button[aria-label='Next page']"));
    const move = navigate.mock.calls.at(-1)![0] as { search: (prev: Record<string, unknown>) => Record<string, unknown> };
    expect(move.search({ page: 2, size: 25 })).toEqual({ page: 3, size: 25 });
  });

  it("pages by the filter in use: its own count is the total", async () => {
    listUsers.mockResolvedValue({ users: many(50), ...pages(137, { online: 60 }) });
    await mount();
    await click([...document.querySelectorAll("[role=radio]")].find((r) => r.textContent?.startsWith("Online")));
    await settle();
    expect(lastCall()).toMatchObject({ pageSize: 50, offset: 0 });
    expect(text()).toContain("Showing 1–50 of 60");
    // a filter starts from the first page again
    const move = navigate.mock.calls.at(-1)![0] as { search: (prev: Record<string, unknown>) => Record<string, unknown> };
    expect(move.search({ page: 3 })).toEqual({});
  });

  it("shows no pager when everybody fits a page", async () => {
    await mount();
    expect(document.querySelector("nav")).toBeNull();
  });

  it("steps back to the last page when the rows it showed are gone", async () => {
    search = { page: 9 };
    listUsers.mockResolvedValue({ users: many(37, 101), ...pages(137) });
    await mount();
    const move = navigate.mock.calls.at(-1)![0] as { search: (prev: Record<string, unknown>) => Record<string, unknown>; replace?: boolean };
    expect(move.search({ page: 9 })).toEqual({ page: 3 });
    expect(move.replace).toBe(true);
  });
});

describe("the people list, sorted", () => {
  const moved = () => {
    const move = navigate.mock.calls.at(-1)![0] as { search: (prev: Record<string, unknown>) => Record<string, unknown> };
    return (prev: Record<string, unknown> = {}) => move.search(prev);
  };
  const head = (label: string) => [...document.querySelectorAll<HTMLElement>("[role=columnheader]")].find((h) => h.textContent === label)!;
  const sortBtn = (label: string) => head(label).querySelector("button")!;

  it("makes the sortable headers real buttons: person, group, devices, traffic, term, last online", async () => {
    await mount();
    for (const label of ["Person", "Group", "Devices", "Traffic this month", "Term", "Last online"]) expect(sortBtn(label), label).toBeInstanceOf(HTMLButtonElement);
  });

  it("sorts by name, ascending, until the URL says otherwise; only that header has aria-sort ascending", async () => {
    await mount();
    expect(lastCall()).toMatchObject({ sort: UserSort.NAME, descending: false, offset: 0 });
    expect(head("Person").getAttribute("aria-sort")).toBe("ascending");
    expect(document.querySelectorAll("[role=columnheader][aria-sort='ascending']").length).toBe(1);
  });

  it("asks for the order of the URL, by offset and never with a page token", async () => {
    search = { sort: "traffic", desc: 1, page: 2 };
    await mount();
    expect(lastCall()).toMatchObject({ sort: UserSort.TRAFFIC, descending: true, offset: 50, pageSize: 50 });
    for (const [req] of listUsers.mock.calls) expect(req).not.toHaveProperty("pageToken");
    expect(head("Traffic this month").getAttribute("aria-sort")).toBe("descending");
  });

  it("falls back to name order for a sort the server does not know", async () => {
    search = { sort: "colour", desc: "maybe" };
    await mount();
    expect(lastCall()).toMatchObject({ sort: UserSort.NAME, descending: false });
  });

  it("sorts a column in its natural first direction: most first for traffic, devices and last online, soonest first for the term", async () => {
    await mount();
    const first = async (label: string) => {
      await click(sortBtn(label));
      return moved()({ page: 3, size: 25 });
    };
    expect(await first("Traffic this month")).toEqual({ size: 25, sort: "traffic", desc: 1 });
    expect(await first("Devices")).toEqual({ size: 25, sort: "devices", desc: 1 });
    expect(await first("Last online")).toEqual({ size: 25, sort: "seen", desc: 1 });
    expect(await first("Term")).toEqual({ size: 25, sort: "expiry" });
    expect(await first("Group")).toEqual({ size: 25, sort: "group" });
  });

  it("reverses the active column on the second click", async () => {
    search = { sort: "traffic", desc: 1, page: 3 };
    await mount();
    await click(sortBtn("Traffic this month"));
    expect(moved()({ sort: "traffic", desc: 1, page: 3 })).toEqual({ sort: "traffic" });
    act(() => root?.unmount());
    host?.remove();
    search = {};
    await mount();
    await click(sortBtn("Person"));
    expect(moved()({})).toEqual({ desc: 1 });
  });

  it("keeps the order when the filter or the search changes, and the filter and the search when the order changes", async () => {
    search = { sort: "traffic", desc: 1 };
    await mount();
    await click([...document.querySelectorAll("[role=radio]")].find((r) => r.textContent?.startsWith("Online")));
    await type(document.querySelector<HTMLInputElement>("input[aria-label='Find a person']")!, "an");
    await act(async () => void (await new Promise((r) => setTimeout(r, 300))));
    await settle();
    expect(lastCall()).toMatchObject({ filter: 1, query: "an", sort: UserSort.TRAFFIC, descending: true, offset: 0 });
    await click(sortBtn("Term"));
    expect(lastCall()).toMatchObject({ filter: 1, query: "an" });
    expect(moved()({ sort: "traffic", desc: 1 })).toEqual({ sort: "expiry" });
  });

  it("offers the same choice as a line of chips where the head is gone, the chosen one pressed", async () => {
    search = { sort: "traffic", desc: 1 };
    await mount();
    const chips = [...document.querySelectorAll<HTMLElement>("[role=group][aria-label='Sort by'] button")];
    expect(chips.map((c) => c.getAttribute("aria-pressed"))).toEqual(["false", "true", "false", "false", "false", "false", "false"]);
    await click(chips[3]); // the term
    expect(moved()({ sort: "traffic", desc: 1 })).toEqual({ sort: "expiry" });
  });
});

describe("the device limit in the list", () => {
  const chip = () => [...document.querySelectorAll("[role=radio]")].find((r) => r.textContent?.startsWith("Over device limit"));
  const answer = (overDeviceLimit: number) => ({
    users: [person({ id: "usr_a", name: "alice", devicesUsed: 6 }), person({ id: "usr_b", name: "bob", devicesUsed: 5 }), person({ id: "usr_c", name: "carol", devicesUsed: 2 })],
    nextPageToken: "",
    counts: { all: 3, online: 0, expiring: 0, overQuota: 0, overDeviceLimit },
  });

  it("hides the chip while nobody is over the limit", async () => {
    listUsers.mockResolvedValue(answer(0));
    await mount();
    expect(chip()).toBeUndefined();
  });

  it("shows the chip with its count, asks the server for the filter, and keeps the chip while it is in use", async () => {
    // once the filter is in use, the last person over the limit got a higher one: its count is 0
    listUsers.mockImplementation(async (r: { filter?: UserFilter }) => answer(r.filter === UserFilter.OVER_DEVICE_LIMIT ? 0 : 1));
    await mount();
    expect(chip()?.textContent).toBe("Over device limit1");
    await click(chip());
    await settle();
    expect(lastCall()).toMatchObject({ filter: UserFilter.OVER_DEVICE_LIMIT, offset: 0 });
    expect(chip()?.getAttribute("aria-checked")).toBe("true");
    expect(chip()?.textContent).toBe("Over device limit0"); // still there, so the filter can be left
  });
});
