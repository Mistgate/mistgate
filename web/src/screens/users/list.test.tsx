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
const navigate = vi.fn();
let search: Record<string, unknown> = {};
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  users: { listUsers: (...a: unknown[]) => listUsers(...a), createUser: (...a: unknown[]) => createUser(...a) },
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
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: Role.OWNER } }) },
}));
vi.mock("@tanstack/react-router", () => ({
  useSearch: () => search,
  useNavigate: () => navigate,
  Link: ({ children, to, params, search: s, className, onClick, ...rest }: { children?: ReactNode; to: string; params?: object; search?: object; className?: string; onClick?: () => void }) => (
    <a href={to} data-params={JSON.stringify(params ?? {})} data-search={JSON.stringify(s ?? {})} className={className} onClick={onClick} aria-label={(rest as { "aria-label"?: string })["aria-label"]} data-tone={(rest as { "data-tone"?: string })["data-tone"]}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

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
  listUsers.mockResolvedValue({
    users: [
      person({ id: "usr_a", name: "Anna" }),
      person({ id: "usr_b", name: "Boris", groupId: "grp_ok", groupName: "ok", accessHapp: true }),
      person({ id: "usr_c", name: "Clara", status: UserStatus.DISABLED }),
    ],
    nextPageToken: "",
    counts: { all: 3, online: 0, expiring: 0, overQuota: 0 },
  });
  Object.assign(navigator, { clipboard: { writeText: (s: string) => (written.push(s), Promise.resolve()) } });
});
afterEach(() => {
  setLang("en");
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [listUsers, createUser, navigate]) m.mockReset();
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

describe("the people list", () => {
  it("marks an active person who would get nothing, with the reason on hover", async () => {
    await mount();
    expect(text()).toContain("App"); // the column
    // the table: a mark without words, named (with the reason) for a screen reader and in its tooltip
    expect([...document.querySelectorAll("table [role=img][tabindex='0']")].map((d) => d.getAttribute("aria-label"))).toEqual([
      "No access. Gets nothing yet: no profile of the group runs on a node", // Anna
      "Active", // Boris gets something
      "Disabled", // Clara
    ]);
    // the phone card says it in words, the reason on hover
    const marks = [...document.querySelectorAll<HTMLElement>("[title]")].filter((e) => e.textContent?.includes("No access"));
    expect(marks.length).toBe(1);
    expect(marks[0]!.title).toBe("Gets nothing yet: no profile of the group runs on a node");
  });

  it("shows the word now in green with a dot, and a past time in plain grey", async () => {
    listUsers.mockResolvedValue({
      users: [
        person({ id: "usr_on", name: "Online", online: true, accessHapp: true }),
        person({ id: "usr_off", name: "Offline", online: false, accessHapp: true, lastSeenUnix: BigInt(Math.floor(Date.now() / 1000) - 13 * 60) }),
      ],
      nextPageToken: "",
      counts: { all: 2, online: 1, expiring: 0, overQuota: 0 },
    });
    await mount();
    const table = document.querySelector<HTMLElement>("table")!;
    const now = [...table.querySelectorAll<HTMLElement>("span")].filter((s) => s.textContent === "now").at(-1)!; // the innermost
    const cell = now.closest<HTMLElement>(".tone-ok")!;
    expect(cell.className).toContain("tone-text");
    expect(cell.querySelector(".tone-ok.rounded-full")).not.toBeNull(); // the dot
    const ago = [...table.querySelectorAll<HTMLElement>("span")].find((s) => s.textContent === "13 min ago")!;
    expect(ago.className).toContain("text-muted");
    expect(ago.closest(".tone-ok")).toBeNull();
  });

  it("paints each group's chip in the colour stored on the group, and by its id while none is stored", async () => {
    listUsers.mockResolvedValue({
      users: [
        person({ id: "usr_a", name: "Anna", groupColor: "mint" }),
        person({ id: "usr_b", name: "Boris", groupId: "grp_ok", groupName: "ok", accessHapp: true, groupColor: "rose" }),
        person({ id: "usr_c", name: "Clara", groupId: "grp_old", groupName: "old", accessHapp: true, groupColor: "" }),
      ],
      nextPageToken: "",
      counts: { all: 3, online: 0, expiring: 0, overQuota: 0 },
    });
    await mount();
    const chip = (id: string) => [...document.querySelectorAll<HTMLElement>("a[data-tone]")].filter((a) => JSON.parse(a.dataset.search!).group === id);
    const ok = chip("grp_ok");
    expect(ok.length).toBe(2); // the table and the phone card
    expect(ok[0]!.className).toContain("tone-chip");
    expect(ok.map((a) => a.dataset.tone)).toEqual(["rose", "rose"]); // not what its id would give (lavender)
    expect(groupTone("grp_ok")).not.toBe("rose");
    expect(chip("grp_empty")[0]!.dataset.tone).toBe("mint");
    expect(chip("grp_old")[0]!.dataset.tone).toBe(groupTone("grp_old")); // none stored: the id's tone
  });
  it("shows the apps as a link icon chip, a keys icon chip, both or nothing, with the whole wording for a screen reader", async () => {
    listUsers.mockResolvedValue({
      users: [
        person({ id: "usr_l", name: "L", accessHapp: true, via: [App.HAPP] }),
        person({ id: "usr_k", name: "K", accessHapp: true, via: [App.AMNEZIA] }),
        person({ id: "usr_b", name: "B", accessHapp: true, via: [App.HAPP, App.AMNEZIA] }),
        person({ id: "usr_n", name: "N", accessHapp: true, via: [] }),
      ],
      nextPageToken: "",
      counts: { all: 4, online: 0, expiring: 0, overQuota: 0 },
    });
    await mount();
    const table = document.querySelector<HTMLElement>("table")!;
    const rows = [...table.querySelectorAll<HTMLElement>("tr.cursor-pointer")];
    const via = (row: HTMLElement) => [...row.querySelectorAll<HTMLElement>('[role="img"]')].find((e) => e.querySelector(".tone-chip"));
    // icon only: each chip names itself on hover and holds no text
    const chips = (row: HTMLElement) => [...via(row)!.querySelectorAll<HTMLElement>(".tone-chip")].map((c) => `${c.dataset.tone}:${c.title}:${c.textContent}`);
    expect(chips(rows[0]!)).toEqual(["sky:Subscription link:"]);
    expect(via(rows[0]!)!.getAttribute("aria-label")).toBe("Subscription link");
    expect(chips(rows[1]!)).toEqual(["mint:AmneziaVPN keys:"]);
    expect(via(rows[1]!)!.getAttribute("aria-label")).toBe("AmneziaVPN keys");
    expect(chips(rows[2]!)).toEqual(["sky:Subscription link:", "mint:AmneziaVPN keys:"]);
    expect(via(rows[2]!)!.getAttribute("aria-label")).toBe("Link + AmneziaVPN keys");
    expect(via(rows[3]!)).toBeUndefined();
    expect(text()).not.toContain("Both");
  });

  it("shows the status as a mark without words: a quiet dot when healthy, a glyph on its colour for a problem", async () => {
    listUsers.mockResolvedValue({
      users: [
        person({ id: "usr_ok", name: "Fine", accessHapp: true }),
        person({ id: "usr_full", name: "Full", accessHapp: true, devicesUsed: 5, deviceLimit: 5 }),
        person({ id: "usr_exp", name: "Gone", accessHapp: true, status: UserStatus.EXPIRED }),
        person({ id: "usr_off", name: "Paused", accessHapp: true, status: UserStatus.DISABLED }),
      ],
      nextPageToken: "",
      counts: { all: 4, online: 0, expiring: 0, overQuota: 0 },
    });
    await mount();
    const table = document.querySelector("table")!;
    const dot = (name: string) => [...table.querySelectorAll("tr")].find((r) => r.textContent?.includes(name))!.querySelector<HTMLElement>("[role=img][tabindex='0']")!;
    expect(dot("Fine").getAttribute("aria-label")).toBe("Active");
    expect(dot("Fine").textContent).toBe(""); // a plain dot
    expect(dot("Fine").querySelector(".tone-ok.rounded-full")).not.toBeNull();
    // a problem reads by its shape as well as its hue
    expect([dot("Full"), dot("Gone"), dot("Paused")].map((d) => `${d.getAttribute("aria-label")}:${d.textContent}`)).toEqual(["Device limit:!", "Expired:×", "Disabled:–"]);
    expect(dot("Full").querySelector(".tone-warn")).not.toBeNull();
    expect(dot("Gone").querySelector(".tone-bad")).not.toBeNull();
    expect(dot("Paused").querySelector(".tone-off")).not.toBeNull();
    // no status word in the table; the phone card says a problem in words and nothing about a healthy person
    expect(table.querySelector("tbody")!.textContent).not.toMatch(/Active|Device limit|Expired|Disabled/);
    const cards = [...document.querySelectorAll<HTMLElement>("div.cursor-pointer")];
    expect(cards[0]!.textContent).not.toContain("Active");
    expect(cards[1]!.textContent).toContain("Device limit");
  });

  it("names the status in a tooltip when the mark gets the keyboard focus", async () => {
    listUsers.mockResolvedValue({
      users: [person({ id: "usr_ok", name: "Fine", accessHapp: true })],
      nextPageToken: "",
      counts: { all: 1, online: 0, expiring: 0, overQuota: 0 },
    });
    await mount();
    expect(text()).not.toContain("Active");
    const dot = document.querySelector<HTMLElement>("table [role=img][tabindex='0']")!;
    await act(async () => dot.focus());
    await settle();
    expect(text()).toContain("Active");
  });

  it("puts the node of an online person next to the word now, as a link to the node", async () => {
    listUsers.mockResolvedValue({
      users: [person({ id: "usr_on", name: "Online", online: true, accessHapp: true, currentNodeId: "nod_1", currentNodeName: "de1" })],
      nextPageToken: "",
      counts: { all: 1, online: 1, expiring: 0, overQuota: 0 },
    });
    await mount();
    const seen = [...document.querySelector("table")!.querySelectorAll("td")].at(-1)!;
    expect(seen.textContent).toBe("now·de1");
    const node = seen.querySelector<HTMLAnchorElement>("a")!;
    expect(node.getAttribute("href")).toBe("/nodes/$id");
    expect(JSON.parse(node.dataset.params!)).toEqual({ id: "nod_1" });
  });

  it("names the group as a link to it", async () => {
    await mount();
    const g = document.querySelector<HTMLAnchorElement>('a[aria-label="Open group “ok”"]')!;
    expect(g.dataset.search).toBe(JSON.stringify({ tab: "groups", group: "grp_ok" }));
  });

  it("opens the Groups tab by its address", async () => {
    await mount();
    await click([...document.querySelectorAll("[role=tab]")].find((x) => x.textContent === "Groups"));
    expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ to: "/users", search: { tab: "groups" } }));
    act(() => root?.unmount());
    search = { tab: "groups" };
    await mount();
    expect(button("New group")).toBeDefined();
    expect(text()).toContain("2 people");
  });

  it("does not copy the link of a new person who gets nothing yet: the window says why first", async () => {
    createUser.mockResolvedValue({ user: person({ id: "usr_n", name: "Nina" }), subscriptionUrl: "https://sub.example.com/p/T", pagePassword: "" });
    await mount();
    await click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("New user")));
    await settle();
    await type(document.querySelector<HTMLInputElement>("input[placeholder='E.g. Marina']")!, "Nina");
    await click(button("Create"));
    await settle();
    expect(createUser).toHaveBeenCalled();
    expect(written).toEqual([]);
    expect(text()).toContain("Nina gets nothing yet: no profile of the group “empty” runs on a node.");
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
    expect(listUsers.mock.calls.at(-1)![0]).toMatchObject({ pageSize: 50, offset: 0 });
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
  const th = (label: string) => [...document.querySelectorAll<HTMLElement>("th")].find((h) => h.textContent === label)!;
  const sortBtn = (label: string) => th(label).querySelector("button")!;
  // the shell's own count query asks for one row; the list is the other
  const lastCall = () => listUsers.mock.calls.map((c) => c[0] as Record<string, unknown>).filter((r) => r.pageSize !== 1).at(-1)!;

  it("makes the seven sortable headers real buttons, and leaves App plain", async () => {
    await mount();
    for (const label of ["Name", "Status", "Group", "Dev.", "Traffic", "Term", "Last seen"]) expect(sortBtn(label), label).toBeInstanceOf(HTMLButtonElement);
    expect(th("App").querySelector("button")).toBeNull();
    expect(th("Node")).toBeUndefined(); // the node is in "Last seen" now
  });

  it("sorts by name, ascending, until the URL says otherwise; only that header has aria-sort", async () => {
    await mount();
    expect(lastCall()).toMatchObject({ sort: UserSort.NAME, descending: false, offset: 0 });
    expect(th("Name").getAttribute("aria-sort")).toBe("ascending");
    expect(document.querySelectorAll("th[aria-sort]").length).toBe(1);
  });

  it("asks for the order of the URL, by offset and never with a page token", async () => {
    search = { sort: "traffic", desc: 1, page: 2 };
    await mount();
    expect(lastCall()).toMatchObject({ sort: UserSort.TRAFFIC, descending: true, offset: 50, pageSize: 50 });
    for (const [req] of listUsers.mock.calls) expect(req).not.toHaveProperty("pageToken");
    expect(th("Traffic").getAttribute("aria-sort")).toBe("descending");
    expect(document.querySelectorAll("th[aria-sort]").length).toBe(1);
  });

  it("falls back to name order for a sort the server does not know", async () => {
    search = { sort: "colour", desc: "maybe" };
    await mount();
    expect(lastCall()).toMatchObject({ sort: UserSort.NAME, descending: false });
  });

  it("sorts a column in its natural first direction: most first for traffic, devices and last seen, soonest first for the term", async () => {
    await mount();
    const first = async (label: string) => {
      await click(sortBtn(label));
      return moved()({ page: 3, size: 25 });
    };
    expect(await first("Traffic")).toEqual({ size: 25, sort: "traffic", desc: 1 });
    expect(await first("Dev.")).toEqual({ size: 25, sort: "devices", desc: 1 });
    expect(await first("Last seen")).toEqual({ size: 25, sort: "seen", desc: 1 });
    expect(await first("Term")).toEqual({ size: 25, sort: "expiry" });
    expect(await first("Group")).toEqual({ size: 25, sort: "group" });
    expect(await first("Status")).toEqual({ size: 25, sort: "status" });
    // name is the bare URL
    act(() => root?.unmount());
    search = { sort: "traffic", desc: 1 };
    await mount();
    await click(sortBtn("Name"));
    expect(moved()({ sort: "traffic", desc: 1 })).toEqual({});
  });

  it("reverses the active column on the second click, and goes back to the first page", async () => {
    search = { sort: "traffic", desc: 1, page: 3 };
    await mount();
    await click(sortBtn("Traffic"));
    expect(moved()({ sort: "traffic", desc: 1, page: 3 })).toEqual({ sort: "traffic" });
    act(() => root?.unmount());
    search = { sort: "expiry" };
    await mount();
    await click(sortBtn("Term"));
    expect(moved()({ sort: "expiry" })).toEqual({ sort: "expiry", desc: 1 });
    act(() => root?.unmount());
    search = {};
    await mount();
    await click(sortBtn("Name"));
    expect(moved()({})).toEqual({ desc: 1 });
  });

  it("keeps the order when the filter or the search changes, and the filter and the search when the order changes", async () => {
    search = { sort: "traffic", desc: 1 };
    await mount();
    await click([...document.querySelectorAll("[role=radio]")].find((r) => r.textContent?.startsWith("Online")));
    await type(document.querySelector<HTMLInputElement>("input[placeholder='Name or group']")!, "an");
    await act(async () => void (await new Promise((r) => setTimeout(r, 300))));
    await settle();
    expect(lastCall()).toMatchObject({ filter: 1, query: "an", sort: UserSort.TRAFFIC, descending: true, offset: 0 });
    // the order changes by the URL only; the filter and the search are the screen's own state and stay
    await click(sortBtn("Term"));
    expect(lastCall()).toMatchObject({ filter: 1, query: "an" });
    expect(moved()({ sort: "traffic", desc: 1 })).toEqual({ sort: "expiry" });
  });

  it("offers the same choice on the phone: a select and a direction button", async () => {
    search = { sort: "traffic", desc: 1 };
    await mount();
    expect(document.querySelector("[aria-label='Sort by']")?.textContent).toBe("Traffic");
    await click(document.querySelector("button[aria-label='Sort ascending']"));
    expect(moved()({ sort: "traffic", desc: 1, page: 2 })).toEqual({ sort: "traffic" });
    act(() => root?.unmount());
    search = {};
    await mount();
    await click(document.querySelector("button[aria-label='Sort descending']"));
    expect(moved()({})).toEqual({ desc: 1 });
  });
});

describe("the count line under the title", () => {
  const countLine = () => [...document.querySelectorAll("p")].find((p) => p.textContent?.includes("·"));
  const line = () => countLine()?.textContent;
  const withCounts = (counts: Record<string, number>) =>
    listUsers.mockResolvedValue({ users: [person({ id: "usr_a", name: "Anna" })], nextPageToken: "", counts: { expiring: 0, overQuota: 0, ...counts } });

  it("says people, devices and who is online with how many devices, in English", async () => {
    withCounts({ all: 17, online: 6, devicesUsed: 31, devicesOnline: 8 });
    await mount();
    expect(line()).toBe("17 people·31 devices·6 online now(8 devices)");
  });

  it("uses the singular, and drops the online devices when nobody is online", async () => {
    withCounts({ all: 1, online: 1, devicesUsed: 1, devicesOnline: 1 });
    await mount();
    expect(line()).toBe("1 person·1 device·1 online now(1 device)");
    act(() => root?.unmount());
    withCounts({ all: 4, online: 0, devicesUsed: 9, devicesOnline: 0 });
    await mount();
    expect(line()).toBe("4 people·9 devices·0 online now");
  });

  it("gives the numbers their weight and the online part its colour", async () => {
    withCounts({ all: 17, online: 6, devicesUsed: 31, devicesOnline: 8 });
    await mount();
    expect([...countLine()!.querySelectorAll("b")].map((b) => b.textContent)).toEqual(["17", "31", "6", "8"]);
    expect(countLine()!.querySelector(".tone-ok .tone-text")?.textContent).toBe("6 online now");
  });

  it("speaks Russian with its three forms", async () => {
    setLang("ru");
    withCounts({ all: 21, online: 3, devicesUsed: 32, devicesOnline: 5 });
    await mount();
    expect(line()).toBe("21 человек·32 устройства·3 онлайн сейчас(5 устройств)");
    act(() => root?.unmount());
    withCounts({ all: 2, online: 1, devicesUsed: 1, devicesOnline: 1 });
    await mount();
    expect(line()).toBe("2 человека·1 устройство·1 онлайн сейчас(1 устройство)");
  });

  it("speaks Chinese with the measure words", async () => {
    setLang("zh");
    withCounts({ all: 17, online: 6, devicesUsed: 31, devicesOnline: 8 });
    await mount();
    expect(line()).toBe("17 人·31 台设备·6 人在线（8 台设备）");
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
    expect(listUsers.mock.calls.at(-1)![0]).toMatchObject({ filter: UserFilter.OVER_DEVICE_LIMIT, offset: 0 });
    expect(chip()?.getAttribute("aria-checked")).toBe("true");
    expect(chip()?.textContent).toBe("Over device limit0"); // still there, so the filter can be left
  });

  it("paints the devices like the traffic bar: quiet with room, amber when full, red and named when over", async () => {
    listUsers.mockResolvedValue(answer(1));
    await mount();
    const cell = (v: string) => [...document.querySelectorAll("td")].find((td) => td.textContent === v)!;
    expect(cell("6/5").className).toContain("text-danger-text");
    expect(cell("6/5").getAttribute("title")).toBe("Over device limit");
    expect(cell("5/5").className).toContain("text-warn-text");
    expect(cell("2/5").className).toContain("text-muted");
  });
});
