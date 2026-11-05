import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { StepUpCancelled } from "@/components/step-up";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { UserFilter, UserSort, UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { setLang } from "@/i18n";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { UsersBulk, leavesPeople } from "./bulk";
import { UsersScreen } from "./list";

// The bulk bar of the People tab: it shows with the first tick (owner and helper, never for a read-only admin), counts in
// words, says what is on other pages, clears on Esc, and "Select" replaces the selection (all found, by state). Every
// action asks first, names who takes part and who sits out, goes in slices of 200 (or one person at a time, four at once,
// for a fixed date) and never touches a link, a page password or a key.

const api = vi.hoisted(() => ({
  listUsers: vi.fn(),
  extendUsers: vi.fn(),
  setUsersEnabled: vi.fn(),
  resetUserTraffic: vi.fn(),
  deleteUsers: vi.fn(),
  updateUser: vi.fn(),
  getSubscriptionLink: vi.fn(),
  getTelegramLinks: vi.fn(),
  rotateTelegramKey: vi.fn(),
  navigate: vi.fn(),
  blocker: { current: undefined as undefined | { shouldBlockFn: (a: unknown) => boolean; enableBeforeUnload: () => boolean } },
  search: { current: {} as Record<string, unknown> },
  role: { current: 1 },
  refuse: { current: false },
}));
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  users: {
    listUsers: api.listUsers,
    extendUsers: api.extendUsers,
    setUsersEnabled: api.setUsersEnabled,
    resetUserTraffic: api.resetUserTraffic,
    deleteUsers: api.deleteUsers,
    updateUser: api.updateUser,
    getSubscriptionLink: api.getSubscriptionLink,
    getTelegramLinks: api.getTelegramLinks,
    rotateTelegramKey: api.rotateTelegramKey,
  },
  groups: { listGroups: () => Promise.resolve({ groups: [{ id: "grp_all", name: "everyone", profileIds: [], userCount: 0, dnsPresetId: "", happNodes: 1, amneziaNodes: 0 }] }) },
  profiles: { listProfiles: () => Promise.resolve({ profiles: [] }), listProtocols: () => Promise.resolve({ protocols: [] }) },
  nodes: { listNodes: () => Promise.resolve({ nodes: [] }) },
  dns: { listDnsPresets: () => Promise.resolve({ presets: [], providers: [], clientSupport: [] }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: api.role.current } }) },
}));
vi.mock("@tanstack/react-router", () => ({
  useSearch: () => api.search.current,
  useNavigate: () => api.navigate,
  useBlocker: (opts: { shouldBlockFn: (a: unknown) => boolean; enableBeforeUnload: () => boolean }) => {
    api.blocker.current = opts;
    return { status: "idle" };
  },
  Link: ({ children, to, params, className }: { children?: ReactNode; to: string; params?: { id: string }; className?: string }) => (
    <a href={to.replace("$id", params?.id ?? "")} className={className}>
      {children}
    </a>
  ),
}));
// the step-up dialog is not under test: a guard that lets the call through, or says the admin closed the dialog
vi.mock("@/components/step-up", async (orig) => ({
  ...(await orig<object>()),
  useStepUp: () => (call: () => Promise<unknown>) => (api.refuse.current ? Promise.reject(new StepUpCancelled()) : call()),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

// Every test here mounts the whole People screen (50 rows with tick boxes) and drives menus and windows through many act()
// turns: 1 to 3 s on an idle machine, so the default 5 s is only a 2x margin. Under load a test timed out, and its body kept
// running on the shared document and root, which made the following tests fail at press() with "expected undefined to be
// truthy". A roomy limit for this file only; a test that really hangs still fails, just later.
vi.setConfig({ testTimeout: 30_000 });

const NOW = Math.floor(Date.now() / 1000);
const pad = (i: number) => String(i).padStart(4, "0");
/** Made-up people p0000, p0001…: every 10th is off, every 7th (from 3) expired, every 5th without access, every 4th has no expiry, every 3rd used nothing, every 6th never connected. */
const makePeople = (n: number) =>
  Array.from({ length: n }, (_, i) => ({
    id: `usr_${pad(i)}`,
    name: `p${pad(i)}`,
    groupId: "grp_all",
    groupName: "everyone",
    status: i % 10 === 0 ? UserStatus.DISABLED : i % 7 === 3 ? UserStatus.EXPIRED : UserStatus.ACTIVE,
    apps: { happ: true, amnezia: false },
    via: [],
    devicesUsed: 0,
    deviceLimit: 5,
    usedBytes: i % 3 === 0 ? 0n : 1_000_000_000n,
    quotaBytes: 0n,
    expiresUnix: i % 4 === 0 ? 0n : BigInt(NOW + 86400 * (10 + (i % 5))),
    lastSeenUnix: i % 6 === 0 ? 0n : BigInt(NOW - 3600),
    nextResetUnix: 0n,
    speedLimitBps: 0n,
    createdUnix: 0n,
    accessHapp: i % 5 !== 0,
    accessAmnezia: false,
    accessTelegram: false,
  }));
type P = ReturnType<typeof makePeople>[number];
let everyone: P[] = [];
const ids = (list: P[]) => list.map((p) => p.id);

/** Offset paging for the table, name-order keyset paging (a page token is the offset) for "All found". */
function serve(list: P[]) {
  everyone = list;
  api.listUsers.mockImplementation(async (req: { pageSize: number; pageToken: string; offset?: number }) => {
    const current = everyone;
    const start = req.pageToken ? Number(req.pageToken) : (req.offset ?? 0);
    const end = Math.min(current.length, start + req.pageSize);
    return { users: current.slice(start, end), nextPageToken: end < current.length ? String(end) : "", counts: { all: list.length, online: 0, expiring: 0, overQuota: 0, overDeviceLimit: 0, devicesUsed: 0, devicesOnline: 0 } };
  });
}
/** The calls change the made-up people and answer with them as they are now, so the page's refetch tells the same story. */
function serveChanges() {
  const find = (userIds: string[]) => userIds.flatMap((id) => everyone.find((p) => p.id === id) ?? []);
  const change = (userIds: string[], how: (p: P) => void) => ({
    users: find(userIds).map((p) => {
      how(p);
      return { ...p };
    }),
  });
  api.extendUsers.mockImplementation(async ({ userIds, days }: { userIds: string[]; days: number }) => change(userIds, (p) => void (p.expiresUnix > 0n && (p.expiresUnix += BigInt(days * 86400)))));
  api.setUsersEnabled.mockImplementation(async ({ userIds, enabled }: { userIds: string[]; enabled: boolean }) => change(userIds, (p) => void (p.status = enabled ? UserStatus.ACTIVE : UserStatus.DISABLED)));
  api.resetUserTraffic.mockImplementation(async ({ userIds }: { userIds: string[] }) => change(userIds, (p) => void (p.usedBytes = 0n)));
  api.deleteUsers.mockImplementation(async ({ userIds }: { userIds: string[] }) => void (everyone = everyone.filter((p) => !userIds.includes(p.id))));
  api.updateUser.mockImplementation(async ({ userId, expiresUnix }: { userId: string; expiresUnix: bigint }) => ({ user: change([userId], (p) => void (p.expiresUnix = expiresUnix)).users[0] }));
}

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  api.search.current = {};
  api.role.current = Role.OWNER;
  api.refuse.current = false;
  api.blocker.current = undefined;
  setLang("en");
  serve(makePeople(120));
  serveChanges();
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  document.body.innerHTML = "";
  for (const f of Object.values(api)) if (typeof f === "function" && "mockReset" in f) f.mockReset();
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
const settle = async (n = 8) => {
  for (let i = 0; i < n; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const bar = () => document.querySelector("[data-bulk-bar]");
const barText = () => bar()?.textContent ?? "";
const boxes = () => [...document.querySelectorAll("table tbody [role=checkbox]")] as HTMLElement[];
const click = (el: Element | undefined | null) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
/** Base UI opens a menu on the press, not on the click. */
const press = async (el: Element | null | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => {
    for (const type of ["pointerdown", "mousedown", "pointerup", "mouseup", "click"]) el!.dispatchEvent(new MouseEvent(type, { bubbles: true, button: 0 }));
  });
  await settle();
};
const text = (b: Element) => b.textContent?.trim();
const barButton = (label: string) => {
  const all = [...(bar()?.querySelectorAll("button") ?? [])];
  return all.find((b) => text(b) === label) ?? all.find((b) => text(b)?.startsWith(label));
};
const item = (label: string) => [...document.querySelectorAll("[role=menuitem]")].find((b) => text(b)?.startsWith(label));
const dialog = () => document.querySelector("[role=dialog]");
const dialogButton = (label: string) => [...(dialog()?.querySelectorAll("button") ?? [])].find((b) => text(b) === label);
const selectMenu = () => [...document.querySelectorAll("button")].find((b) => text(b) === "Select");
/** Until the condition holds (a collected list takes a few turns of the event loop per page). */
async function waitFor(cond: () => boolean, turns = 30) {
  for (let i = 0; i < turns && !cond(); i++) await settle(1);
}
async function pick(label: string) {
  await press(selectMenu());
  await waitFor(() => !!item(label) && item(label)!.getAttribute("aria-disabled") !== "true" && item(label)!.getAttribute("data-disabled") === null);
  await click(item(label));
  await settle();
}
const escape = () => act(async () => void document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
const tick = async (n: number) => {
  for (const b of boxes().slice(0, n)) await click(b);
};
const setInput = (el: HTMLInputElement, value: string) =>
  act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });

describe("the bar", () => {
  it("shows with the first tick for the owner and the helper, counting in words, and clears with its button or Esc", async () => {
    await mount();
    expect(bar()).toBeNull();
    await tick(3);
    expect(barText()).toContain("3");
    expect(barText()).toContain("people selected");
    expect(document.querySelector("[data-bulk-bar] [role=region]")?.getAttribute("aria-label")).toBe("Selected people");
    await click(boxes()[0]);
    await click(boxes()[1]);
    await click(boxes()[2]);
    await tick(1);
    expect(barText()).toContain("person selected");
    await escape();
    expect(bar()).toBeNull();
    await tick(2);
    await click(bar()!.querySelector("button[aria-label='Clear selection']"));
    expect(bar()).toBeNull();
  });

  it("is there for a helper too, and not at all for a read-only admin, who gets no tick boxes and no Select either", async () => {
    api.role.current = Role.HELPER;
    await mount();
    expect(boxes()).toHaveLength(50);
    expect(selectMenu()).toBeTruthy();
    act(() => root?.unmount());
    host?.remove();
    api.role.current = Role.READONLY;
    await mount();
    expect(boxes()).toHaveLength(0);
    expect(document.querySelector("table [role=checkbox]")).toBeNull();
    expect(selectMenu()).toBeUndefined();
    expect(bar()).toBeNull();
    expect(document.querySelectorAll("table tbody tr")).toHaveLength(50);
  });

  it("speaks Russian with its three forms", async () => {
    setLang("ru");
    await mount();
    const says = async (n: number) => {
      await escape();
      await tick(n);
      return barText();
    };
    expect(await says(1)).toContain("человек выбран");
    expect(await says(2)).toContain("человека выбрано");
    expect(await says(5)).toContain("человек выбрано");
    expect(ru["bulk.people.selected"].split("|")).toHaveLength(3);
    expect(en["bulk.people.selected"].split("|")).toHaveLength(2);
  });

  it("counts the ones on other pages after a move to page 2, and keeps them", async () => {
    await mount();
    await tick(3);
    expect(barText()).not.toContain("not on this page");
    // page 2 of the URL: the table shows p0050… and the three ticked ones are elsewhere
    api.search.current = { page: 2 };
    await tick(1); // any change renders the tab again, with the new address (and takes p0000 off the selection)
    await settle();
    expect(api.listUsers).toHaveBeenCalledWith(expect.objectContaining({ offset: 50 }), expect.anything());
    await click(boxes()[0]);
    expect(barText()).toMatch(/^3people selected/);
    expect(barText()).toContain("2 not on this page");
  });
});

describe("Select", () => {
  it("replaces the selection: this page, then all found by the name-order pages of 200", async () => {
    await mount();
    await tick(2);
    await pick("This page");
    expect(barText()).toContain("50");
    await pick("All found");
    expect(barText()).toMatch(/^120people selected/);
    // collected once, when the menu first opened
    const scope = api.listUsers.mock.calls.map((c) => c[0]).filter((r: { pageSize: number }) => r.pageSize === 200);
    expect(scope).toHaveLength(1);
    expect(scope[0]).toMatchObject({ pageSize: 200, sort: UserSort.NAME, filter: UserFilter.UNSPECIFIED, query: "", groupId: "" });
  });

  it("walks the pages by their token and stops at 5000", async () => {
    serve(makePeople(5300));
    await mount();
    await pick("All found");
    const scope = api.listUsers.mock.calls.map((c) => c[0]).filter((r: { pageSize: number }) => r.pageSize === 200);
    expect(scope).toHaveLength(25);
    expect(scope.map((r: { pageToken: string }) => r.pageToken).slice(0, 3)).toEqual(["", "200", "400"]);
    expect(barText()).toContain("5000");
    expect(barText()).toContain("At most 5000 at once");
  });

  it("picks the expired, the disabled, the ones with no access and the ones that never connected", async () => {
    await mount();
    const of = (keep: (p: P) => boolean) => everyone.filter(keep).length;
    const cases: [string, number][] = [
      ["Expired", of((p) => p.status === UserStatus.EXPIRED)],
      ["Disabled", of((p) => p.status === UserStatus.DISABLED)],
      ["No access", of((p) => p.status === UserStatus.ACTIVE && !p.accessHapp)],
      ["Never connected", of((p) => p.lastSeenUnix === 0n)],
    ];
    for (const [label, n] of cases) {
      await pick(label);
      expect(barText(), label).toContain(`${n}`);
      expect(barText(), label).toContain(n === 1 ? "person selected" : "people selected");
    }
  });
});

describe("Extend", () => {
  it("adds days to the ones with an expiry, leaves out those without, and sends them in slices of 200", async () => {
    serve(makePeople(900));
    await mount();
    await pick("All found");
    await press(barButton("Extend"));
    await settle();
    expect(dialog()?.textContent).toContain("Change the term of 900 people");
    const endless = everyone.filter((p) => p.expiresUnix === 0n).length;
    expect(dialog()?.textContent).toContain(`Will change (${900 - endless})`);
    expect(dialog()?.textContent).toContain(`Skipped (${endless})`);
    expect(dialog()?.textContent).toContain("No expiry date, nothing to extend");
    expect(dialog()?.textContent).toContain("and ");
    await click(dialogButton("Add 30 days"));
    await settle(20);
    const sent = api.extendUsers.mock.calls.map((c) => c[0] as { userIds: string[]; days: number });
    expect(sent.map((r) => r.userIds.length)).toEqual([200, 200, 200, 900 - endless - 600]);
    expect(sent.every((r) => r.days === 30)).toBe(true);
    expect(sent.flatMap((r) => r.userIds).every((id) => everyone.find((p) => p.id === id)!.expiresUnix > 0n)).toBe(true);
    expect(barText()).toContain("Extended for");
  });

  it("sets a fixed date with one UpdateUser per person, and warns about the ones it shortens", async () => {
    await mount();
    await tick(8);
    await press(barButton("Extend"));
    await settle();
    await click([...dialog()!.querySelectorAll("[role=radio]")].find((r) => text(r) === "Until a date"));
    await settle();
    const date = dialog()!.querySelector("input[type=date]") as HTMLInputElement;
    expect(date.min).toMatch(/^\d{4}-\d\d-\d\d$/);
    // 11 days out: the people whose end is 12 to 14 days away have a later one, and it gets shorter
    const day = new Date(Date.now() + 11 * 86400_000);
    const value = `${day.getFullYear()}-${String(day.getMonth() + 1).padStart(2, "0")}-${String(day.getDate()).padStart(2, "0")}`;
    await setInput(date, value);
    await settle();
    const until = BigInt(Math.floor(new Date(day.getFullYear(), day.getMonth(), day.getDate(), 23, 59, 59).getTime() / 1000));
    const later = boxes().slice(0, 8).length && everyone.slice(0, 8).filter((p) => p.expiresUnix > until).length;
    if (later) expect(dialog()?.textContent).toContain("a later end date: it moves earlier");
    await click(dialogButton("Set the date"));
    await settle(12);
    expect(api.extendUsers).not.toHaveBeenCalled();
    expect(api.updateUser).toHaveBeenCalledTimes(8);
    expect(api.updateUser.mock.calls.map((c) => c[0].expiresUnix)).toEqual(Array(8).fill(until));
    expect(api.updateUser.mock.calls.map((c) => c[0].userId)).toEqual(ids(everyone.slice(0, 8)));
  });

  it("takes the expiry away with 0, from the people who have one", async () => {
    await mount();
    await tick(8);
    await press(barButton("Extend"));
    await settle();
    await click([...dialog()!.querySelectorAll("[role=radio]")].find((r) => text(r) === "No expiry"));
    await settle();
    const withEnd = everyone.slice(0, 8).filter((p) => p.expiresUnix > 0n);
    expect(dialog()?.textContent).toContain(`Will change (${withEnd.length})`);
    await click(dialogButton("Remove the expiry"));
    await settle(12);
    expect(api.updateUser.mock.calls.map((c) => c[0])).toEqual(withEnd.map((p) => ({ userId: p.id, expiresUnix: 0n })));
  });
});

describe("Disable and Enable", () => {
  it("switches off only the active ones, then offers Enable again for exactly the ones it switched off", async () => {
    await mount();
    await tick(12);
    const pickedPeople = everyone.slice(0, 12);
    const active = pickedPeople.filter((p) => p.status !== UserStatus.DISABLED);
    await press(barButton("Disable"));
    await settle();
    expect(dialog()?.textContent).toContain("They lose access at once");
    expect(dialog()?.textContent).toContain(`Will change (${active.length})`);
    expect(dialog()?.textContent).toContain("Already disabled");
    await click(dialogButton("Disable"));
    await settle(12);
    expect(api.setUsersEnabled).toHaveBeenCalledTimes(1);
    expect(api.setUsersEnabled).toHaveBeenCalledWith({ userIds: active.map((p) => p.id), enabled: false });
    expect(barText()).toContain(`Disabled ${active.length} of 12 people`);
    const again = [...document.querySelectorAll("button")].find((b) => text(b) === "Enable again");
    expect(again).toBeTruthy();
    api.setUsersEnabled.mockClear();
    await click(again);
    await settle(12);
    expect(api.setUsersEnabled).toHaveBeenCalledWith({ userIds: active.map((p) => p.id), enabled: true });
  });

  it("counts the ones Enable takes anew after a Disable (the answer is merged into the selection)", async () => {
    await mount();
    await tick(12);
    const before = everyone.slice(0, 12).filter((p) => p.status === UserStatus.DISABLED).length;
    await press(barButton("More"));
    expect(item("Enable")?.textContent).toContain(String(before));
    await escape();
    await press(barButton("Disable"));
    await settle();
    await click(dialogButton("Disable"));
    await settle(12);
    // most of them are off now, so the bar offers Enable itself, and it takes all twelve
    await press(barButton("Enable"));
    await settle();
    expect(dialog()?.textContent).toContain("Will change (12)");
  });

  it("says that an expired person is switched on but still gets nothing", async () => {
    serve(makePeople(30).map((p, i) => (i === 0 ? { ...p, status: UserStatus.DISABLED, expiresUnix: BigInt(NOW - 86400) } : p)));
    await mount();
    await click(boxes()[0]);
    await press(barButton("Enable"));
    await settle();
    expect(dialog()?.textContent).toContain("term has run out");
  });
});

describe("Reset traffic and Delete", () => {
  it("resets the ones that used something and names the rest as skipped", async () => {
    await mount();
    await tick(6);
    const used = everyone.slice(0, 6).filter((p) => p.usedBytes > 0n);
    await press(barButton("More"));
    await click(item("Reset traffic"));
    await settle();
    expect(dialog()?.textContent).toContain("No traffic used");
    await click(dialogButton("Reset traffic"));
    await settle(12);
    expect(api.resetUserTraffic).toHaveBeenCalledWith({ userIds: used.map((p) => p.id) });
  });

  it("asks for the exact number, deletes nothing before it is typed, and the deleted leave the selection", async () => {
    await mount();
    await tick(4);
    const four = ids(everyone.slice(0, 4));
    await press(barButton("More"));
    await click(item("Delete"));
    await settle();
    expect(dialog()?.textContent).toContain("Delete 4 people");
    expect(dialog()?.textContent).toContain("confirm your sign-in");
    const go = dialogButton("Delete")!;
    expect(go.hasAttribute("disabled") || go.getAttribute("data-disabled") !== null).toBe(true);
    await click(go);
    expect(api.deleteUsers).not.toHaveBeenCalled();
    await setInput(dialog()!.querySelector("input")!, "5");
    expect(dialogButton("Delete")!.hasAttribute("disabled") || dialogButton("Delete")!.getAttribute("data-disabled") !== null).toBe(true);
    await setInput(dialog()!.querySelector("input")!, "4");
    await click(dialogButton("Delete"));
    await settle(12);
    expect(api.deleteUsers).toHaveBeenCalledTimes(1);
    expect(api.deleteUsers).toHaveBeenCalledWith({ userIds: four });
    expect(barText()).toContain("Deleted 4 of 4 people");
    // nobody is left in the selection: the bar is only the summary
    expect(barText()).not.toContain("selected");
  });

  it("deletes nothing when the step-up is refused: the rows read not started", async () => {
    api.refuse.current = true;
    await mount();
    await tick(3);
    await press(barButton("More"));
    await click(item("Delete"));
    await settle();
    await setInput(dialog()!.querySelector("input")!, "3");
    await click(dialogButton("Delete"));
    await settle(12);
    expect(api.deleteUsers).not.toHaveBeenCalled();
    expect(barText()).toContain("3 not started");
    // the people stay selected
    expect(barText()).toContain("3");
  });
});

describe("what the bar never touches", () => {
  it("never fetches a link, a page password or a Telegram key, whatever it runs", async () => {
    await mount();
    await pick("This page");
    await press(barButton("Extend"));
    await settle();
    await click(dialogButton("Add 30 days"));
    await settle(12);
    for (const run of [
      async () => {
        await press(barButton("Disable"));
        await settle();
        await click(dialogButton("Disable"));
      },
      async () => {
        await press(barButton("More"));
        await click(item("Reset traffic"));
        await settle();
        await click(dialogButton("Reset traffic"));
      },
    ]) {
      await run();
      await settle(12);
    }
    expect(api.getSubscriptionLink).not.toHaveBeenCalled();
    expect(api.getTelegramLinks).not.toHaveBeenCalled();
    expect(api.rotateTelegramKey).not.toHaveBeenCalled();
  });
});

describe("leaving the page", () => {
  it("blocks only a move off the People list while a batch runs, never paging or sorting", () => {
    const away = (fullPath: string, search: Record<string, unknown> = {}) => leavesPeople({ fullPath, search });
    expect(away("/users")).toBe(false);
    expect(away("/users/", { page: 3 })).toBe(false);
    expect(away("/users", { sort: "traffic", desc: 1 })).toBe(false);
    expect(away("/users", { tab: "groups" })).toBe(true);
    expect(away("/nodes")).toBe(true);
    expect(away("/users/$id")).toBe(true);
  });

  it("asks the router to block with that rule only while a batch is running", async () => {
    let release!: () => void;
    api.setUsersEnabled.mockImplementation(() => new Promise<{ users: P[] }>((r) => (release = () => r({ users: [] }))));
    await mount();
    await tick(2);
    const next = (fullPath: string, search = {}) => ({ next: { fullPath, search }, current: { fullPath: "/users", search: {} } });
    expect(api.blocker.current!.shouldBlockFn(next("/nodes"))).toBe(false);
    expect(api.blocker.current!.enableBeforeUnload()).toBe(false);
    await press(barButton("More"));
    await click(item("Enable"));
    await settle();
    // two of the first two are active: nothing to enable, so use Disable
    await escape();
    await press(barButton("Disable"));
    await settle();
    await click(dialogButton("Disable"));
    await settle();
    expect(api.blocker.current!.shouldBlockFn(next("/nodes"))).toBe(true);
    expect(api.blocker.current!.shouldBlockFn(next("/users", { tab: "groups" }))).toBe(true);
    expect(api.blocker.current!.shouldBlockFn(next("/users", { page: 2 }))).toBe(false);
    expect(api.blocker.current!.enableBeforeUnload()).toBe(true);
    await act(async () => release());
    await settle();
    expect(api.blocker.current!.shouldBlockFn(next("/nodes"))).toBe(false);
  });
});

describe("Esc", () => {
  it("clears the selection, but not while a menu or a window is open", async () => {
    await mount();
    await tick(2);
    await press(barButton("More"));
    await escape();
    expect(barText()).toContain("2");
    if (!item("Delete")) await press(barButton("More"));
    await click(item("Delete"));
    await settle();
    expect(dialog()).toBeTruthy();
    await escape();
    expect(barText()).toContain("2");
    await click(dialogButton("Cancel"));
    await settle();
    await escape();
    expect(bar()).toBeNull();
  });
});

describe("UsersBulk alone", () => {
  it("renders nothing for no selection and no batch", async () => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const qc = new QueryClient();
    await act(async () =>
      root!.render(
        <QueryClientProvider client={qc}>
          <UsersBulk picked={[]} shown={new Set()} onClear={() => {}} onMerge={() => {}} />
        </QueryClientProvider>,
      ),
    );
    expect(bar()).toBeNull();
  });
});
