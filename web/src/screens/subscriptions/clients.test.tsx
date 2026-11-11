import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang } from "@/i18n";
import { actualKind, ClientCompatActual, ClientCompatState, helloLine, mismatchText, needText, rulesEqual, stateKind, windowDays, type CompatRule } from "@/lib/clientcompat";
import { meQuery } from "@/lib/session";
import { dictionaries } from "@/i18n";
import { ClientsTab } from "./clients";
import { subsTabs, validateSubsSearch } from "./tabs";

const getClientCompat = vi.fn();
const updateClientCompat = vi.fn();
const listOutdatedDevices = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  clientCompat: {
    getClientCompat: (...a: unknown[]) => getClientCompat(...a),
    updateClientCompat: (...a: unknown[]) => updateClientCompat(...a),
    listOutdatedDevices: (...a: unknown[]) => listOutdatedDevices(...a),
  },
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, className }: { children?: ReactNode; to: string; params?: { id: string }; className?: string }) => (
    <a href={to.replace("$id", params?.id ?? "")} className={className}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  setLang("en");
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [getClientCompat, updateClientCompat, listOutdatedDevices]) m.mockReset();
});

const B = ClientCompatState.BELOW_MINIMUM;
const U = ClientCompatState.UNSUPPORTED;
const K = ClientCompatState.UNKNOWN;
const dev = (o: Record<string, unknown>) => ({
  deviceId: "d1",
  userId: "u1",
  userName: "alice",
  client: "",
  platform: "",
  model: "",
  osVersion: "",
  shared: false,
  lastSeenUnix: 0n,
  state: B,
  reason: "version",
  needVersion: "",
  fingerprint: "",
  appTitle: "",
  actual: ClientCompatActual.NONE,
  helloPq: 0,
  helloClassic: 0,
  helloUnknown: 0,
  tableMismatch: "",
  ...o,
});
const devices = [
  dev({ userId: "alice", userName: "alice", client: "Happ/3.0.2", appTitle: "Happ", platform: "android", model: "Pixel 8", osVersion: "15", needVersion: "3.1.1", lastSeenUnix: BigInt(Math.floor(Date.now() / 1000) - 7200) }),
  dev({ userId: "bob", userName: "bob", state: U, reason: "fingerprint", fingerprint: "random", client: "Mihomo/1.19.12", appTitle: "Mihomo", platform: "windows", model: "ThinkPad T14" }),
  dev({ userId: "carol", userName: "carol", deviceId: "", shared: true, state: K, reason: "app_unknown", client: "NovaVPN/1.2" }),
];
const counts = [
  { state: ClientCompatState.OK, devices: 14, people: 6 },
  { state: B, devices: 2, people: 2 },
  { state: U, devices: 3, people: 3 },
  { state: K, devices: 4, people: 4 },
];
/** What the panel adds to a page: the classes over all devices, the window, the disagreements, the nodes that do not count. */
const measured = {
  actualCounts: [
    { actual: ClientCompatActual.NONE, devices: 9, people: 5 },
    { actual: ClientCompatActual.PQ, devices: 12, people: 6 },
    { actual: ClientCompatActual.CLASSIC, devices: 3, people: 2 },
    { actual: ClientCompatActual.MIXED, devices: 1, people: 1 },
  ],
  helloWindowHours: 168,
  mismatchWorse: 0,
  mismatchBetter: 0,
  helloKexNodesWithout: [] as string[],
};
const rule = (app: string, minChrome: string, minOther: string, o: Partial<CompatRule> = {}): CompatRule => ({ app, platform: "", core: "xray", unsupported: false, minChrome, minOther, title: app, ...o });
const defaults = [rule("happ", "3.1.1", "3.1.1", { title: "Happ" }), rule("mihomo", "1.19.31", "", { title: "Mihomo", core: "mihomo" })];
const table = (rules: CompatRule[] = defaults) => ({ rules, defaults });

/** The tab as `role` sees it; `list` answers the device query (by default the three devices and the counts above). */
async function mount(o: { role?: Role; userId?: string; onClearUser?: () => void; rules?: CompatRule[]; list?: (req: { state: number; actual: number }) => unknown } = {}) {
  const { role = Role.OWNER } = o;
  getClientCompat.mockResolvedValue(table(o.rules));
  listOutdatedDevices.mockImplementation(async (req: { state: number; actual: number }) => o.list?.(req) ?? { devices, total: devices.length, counts, fingerprints: ["chrome", "firefox"], ...measured });
  updateClientCompat.mockImplementation(async (r: { rules: CompatRule[] }) => ({ rules: r.rules, defaults }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role } } as never);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <ClientsTab userId={o.userId} onClearUser={o.onClearUser} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async () => {
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const text = () => host?.textContent ?? "";
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const radios = () => [...(document.querySelectorAll("[role=radiogroup]")[0]?.querySelectorAll<HTMLElement>("[role=radio]") ?? [])]; // the state cards
const chips = () => [...(document.querySelectorAll("[role=radiogroup]")[1]?.querySelectorAll<HTMLElement>("[role=radio]") ?? [])]; // All, new-style, old-style, mixed, no data
const glyphs = /[~!×–]/g; // the pill's mark is in its text
const words = (el: Element) => (el.textContent ?? "").replace(glyphs, "");
const rows = () => [...document.querySelectorAll<HTMLElement>("table tbody tr")];
const button = (name: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.includes(name));
const typeInto = async (input: HTMLInputElement, value: string) => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
  await act(async () => void input.dispatchEvent(new Event("input", { bubbles: true })));
};

describe("App versions tab", () => {
  it("explains itself in two sentences and says it changes nothing yet", async () => {
    await mount();
    expect(text()).toContain("post-quantum handshake");
    expect(text()).toContain("changes nothing: it disconnects nobody and shows people nothing");
  });

  it("sums the states up: devices large, people small, and 'Need attention' first with all but OK", async () => {
    await mount();
    const cards = radios();
    expect(cards).toHaveLength(5);
    expect(cards[0]!.textContent).toContain("Need attention");
    expect(cards[0]!.textContent).toContain("9");
    expect(cards[0]!.getAttribute("aria-checked")).toBe("true");
    expect(cards[1]!.textContent).toContain("14");
    expect(cards[1]!.textContent).toContain("6 people");
    expect(cards[2]!.textContent).toContain("Below minimum");
    expect(cards[2]!.textContent).toContain("2 people");
    expect(cards[3]!.textContent).toContain("Unsupported");
    expect(cards[4]!.textContent).toContain("Unknown");
    expect(cards[4]!.textContent).toContain("4 people");
    expect(text()).toContain("Checked by fingerprint: chrome, firefox");
    // the first request: everything but OK, everybody, 50 a page from the start
    expect(listOutdatedDevices.mock.calls[0]![0]).toEqual({ state: ClientCompatState.UNSPECIFIED, actual: ClientCompatActual.UNSPECIFIED, userId: "", pageSize: 50, offset: 0 });
  });

  it("lists the devices with their state, what is missing and a link to the person", async () => {
    await mount();
    expect(rows()).toHaveLength(3);
    const [alice, bob, carol] = rows();
    expect(alice!.textContent).toContain("alice");
    expect(alice!.textContent).toContain("Pixel 8");
    expect(alice!.textContent).toContain("Android 15");
    expect(alice!.textContent).toContain("Happ");
    expect(alice!.textContent).toContain("Happ/3.0.2");
    expect(alice!.textContent).toContain("Below minimum");
    expect(alice!.textContent).toContain("Needs version ≥ 3.1.1");
    expect(alice!.textContent).toContain("2 h ago");
    expect(alice!.querySelector("a")!.getAttribute("href")).toBe("/users/alice");
    expect(bob!.textContent).toContain("Unsupported");
    expect(bob!.textContent).toContain("The profile’s “random” fingerprint gives a classic handshake");
    expect(bob!.textContent).toContain("—");
    expect(carol!.textContent).toContain("Shared link place");
    expect(carol!.textContent).toContain("NovaVPN/1.2");
    expect(carol!.textContent).toContain("The app is not in the table");
  });

  it("filters by a state on a click and goes back to 'Need attention' on the second", async () => {
    await mount();
    await click(radios()[3]);
    await settle();
    expect(listOutdatedDevices.mock.calls.at(-1)![0]).toMatchObject({ state: ClientCompatState.UNSUPPORTED, offset: 0 });
    expect(radios()[3]!.getAttribute("aria-checked")).toBe("true");
    await click(radios()[3]);
    await settle();
    expect(listOutdatedDevices.mock.calls.at(-1)![0]).toMatchObject({ state: ClientCompatState.UNSPECIFIED });
    expect(radios()[0]!.getAttribute("aria-checked")).toBe("true");
  });

  it("says so when no device gets REALITY servers, and when everything is fine", async () => {
    const zero = counts.map((c) => ({ ...c, devices: 0, people: 0 }));
    await mount({ list: () => ({ devices: [], total: 0, counts: zero, fingerprints: [], ...measured }) });
    expect(text()).toContain("No device gets REALITY servers yet");
    expect(radios()).toHaveLength(0);
    act(() => root?.unmount());
    host?.remove();

    await mount({ list: () => ({ devices: [], total: 0, counts: [{ state: ClientCompatState.OK, devices: 5, people: 2 }, ...counts.slice(1).map((c) => ({ ...c, devices: 0, people: 0 }))], fingerprints: [], ...measured }) });
    expect(text()).toContain("All devices are fine");
    expect(text()).not.toContain("No device gets REALITY servers yet");
    expect(radios()).toHaveLength(5);
  });

  it("says a state has no devices", async () => {
    await mount({ list: (req) => (req.state === U ? { devices: [], total: 0, counts, fingerprints: [], ...measured } : { devices, total: 3, counts, fingerprints: [], ...measured }) });
    await click(radios()[3]);
    await settle();
    expect(text()).toContain("No devices in this state");
  });

  it("keeps the list to one person: the request carries the id, the chip clears it", async () => {
    const clear = vi.fn();
    await mount({ userId: "alice", onClearUser: clear });
    expect(listOutdatedDevices.mock.calls[0]![0]).toMatchObject({ userId: "alice" });
    expect(text()).toContain("Person: alice");
    await click(document.querySelector("[aria-label='Show the devices of everyone']"));
    expect(clear).toHaveBeenCalledTimes(1);
  });

  it("speaks Russian", async () => {
    setLang("ru");
    await mount();
    expect(radios()[0]!.textContent).toContain("Требуют внимания");
    expect(rows()[0]!.textContent).toContain("Нужна версия ≥ 3.1.1");
    expect(radios()[1]!.textContent).toContain("6 человек");
    expect(radios()[2]!.textContent).toContain("2 человека");
    expect(rows()[1]!.textContent).toContain("Fingerprint профиля «random» даёт классическое рукопожатие");
  });
});

describe("In fact (what the nodes saw)", () => {
  const P = ClientCompatActual.PQ;
  const C = ClientCompatActual.CLASSIC;
  const X = ClientCompatActual.MIXED;
  const N = ClientCompatActual.NONE;
  const O = ClientCompatState.OK;
  // one device of every class, and the two disagreements: ok by the table but old-style in fact, a problem by the table but new-style
  const seen = [
    dev({ userId: "alice", userName: "alice", deviceId: "a1", state: O, reason: "", client: "Mihomo/1.19.33", appTitle: "Mihomo", platform: "windows", model: "ThinkPad T14", actual: C, helloClassic: 5, tableMismatch: "worse" }),
    dev({ userId: "bob", userName: "bob", deviceId: "b1", state: B, reason: "version", needVersion: "3.1.1", client: "Happ/2.7.1", appTitle: "Happ", platform: "android", model: "Pixel 8", actual: P, helloPq: 41, helloUnknown: 1, tableMismatch: "better" }),
    dev({ userId: "carol", userName: "carol", deviceId: "c1", state: U, reason: "app", client: "SFA/1.14.3", appTitle: "sing-box", actual: C, helloClassic: 12 }),
    dev({ userId: "dave", userName: "dave", deviceId: "d1", state: K, reason: "app_unknown", client: "NovaVPN/1.2", actual: X, helloPq: 3, helloClassic: 2 }),
    dev({ userId: "erin", userName: "erin", deviceId: "e1", state: K, reason: "no_version", actual: N }),
    dev({ userId: "frank", userName: "frank", deviceId: "f1", state: K, reason: "no_version", actual: N, helloUnknown: 4 }),
  ];
  const page = (o: Record<string, unknown> = {}) => ({ devices: seen, total: seen.length, counts, fingerprints: ["chrome"], ...measured, mismatchWorse: 1, mismatchBetter: 1, ...o });
  const cell = (row: HTMLElement) => words(row.querySelectorAll("td")[4]!);

  it("has a column 'In fact' with the pill of each class and the counts under it", async () => {
    await mount({ list: () => page() });
    expect([...document.querySelectorAll("table thead th")].map((h) => h.textContent)).toEqual(["Person", "Device", "App", "State", "In fact", "What is missing"]);
    const [alice, bob, carol, dave, erin, frank] = rows();
    expect(cell(alice!)).toContain("Old-style");
    expect(cell(alice!)).toContain("new 0 · old 5");
    expect(cell(bob!)).toContain("New-style");
    expect(cell(bob!)).toContain("new 41 · old 0 · 1 unreadable");
    expect(cell(carol!)).toContain("new 0 · old 12");
    expect(cell(dave!)).toContain("Mixed");
    expect(cell(dave!)).toContain("new 3 · old 2");
    expect(cell(erin!)).toBe("No data");
    expect(cell(frank!)).toContain("4 unreadable");
    // the pill of the class sits next to the pill of the state, in the same shape
    expect(alice!.querySelectorAll("td")[3]!.querySelector(".pill")).toBeTruthy();
    expect(alice!.querySelectorAll("td")[4]!.querySelector(".pill.tone-bad")).toBeTruthy();
    expect(bob!.querySelectorAll("td")[4]!.querySelector(".pill.tone-ok")).toBeTruthy();
    expect(dave!.querySelectorAll("td")[4]!.querySelector(".pill.tone-warn")).toBeTruthy();
    expect(erin!.querySelectorAll("td")[4]!.querySelector(".pill.tone-blip")).toBeTruthy();
  });

  it("marks where the table and the nodes disagree, in both directions, and says why", async () => {
    await mount({ list: () => page() });
    const [alice, bob, carol] = rows();
    const worse = alice!.querySelector<HTMLElement>("[data-mismatch=worse]")!;
    expect(worse.getAttribute("aria-label")).toBe("Differs from the table: The table says OK, but the nodes saw old-style handshakes: the table is wrong here.");
    expect(worse.className).toContain("tone-warn");
    const better = bob!.querySelector<HTMLElement>("[data-mismatch=better]")!;
    expect(better.getAttribute("aria-label")).toContain("nothing to worry about");
    expect(better.className).toContain("tone-ok");
    expect(carol!.querySelector("[data-mismatch]")).toBeNull();
    // a card says it in words
    const cards = [...document.querySelectorAll<HTMLElement>("p[data-mismatch]")];
    expect(cards.map((c) => c.getAttribute("data-mismatch"))).toEqual(["worse", "better"]);
    expect(cards[0]!.textContent).toContain("the table is wrong here");
  });

  it("counts the classes in chips and the disagreements and the silent nodes in quiet lines", async () => {
    await mount({ list: () => page({ helloKexNodesWithout: ["nl1", "de2"], mismatchWorse: 2, mismatchBetter: 1 }) });
    expect(chips().map(words)).toEqual(["All25", "New-style12", "Old-style3", "Mixed1", "No data9"]);
    expect(chips()[0]!.getAttribute("aria-checked")).toBe("true");
    expect(text()).toContain("By what the nodes saw");
    expect(text()).toContain("last 7 days");
    expect(text()).toContain("“In fact” is what the nodes really saw from a device over the last 7 days");
    expect(text()).toContain("2 devices are OK by the table but send old-style handshakes");
    expect(text()).toContain("1 device has a problem by the table, but the nodes saw only new-style handshakes");
    expect(text()).toContain("2 nodes do not count yet: update their agents (nl1, de2)");
  });

  it("keeps the lines about nodes and disagreements away when there is nothing to say", async () => {
    await mount({ list: () => page({ mismatchWorse: 0, mismatchBetter: 0 }) });
    expect(text()).not.toContain("do not count yet");
    expect(text()).not.toContain("by the table");
    await act(async () => root!.unmount());
    host?.remove();
    await mount({ list: () => page({ mismatchWorse: 0, mismatchBetter: 0, helloKexNodesWithout: ["nl1"] }) });
    expect(text()).toContain("1 node does not count yet: update its agent (nl1)");
  });

  it("filters by a class on a click: every state is asked for, the state cards stay unchosen, a second tap goes back", async () => {
    await mount({ list: () => page() });
    await click(chips()[2]);
    await settle();
    expect(listOutdatedDevices.mock.calls.at(-1)![0]).toMatchObject({ state: ClientCompatState.UNSPECIFIED, actual: C, offset: 0 });
    expect(chips()[2]!.getAttribute("aria-checked")).toBe("true");
    expect(radios().map((r) => r.getAttribute("aria-checked"))).toEqual(["false", "false", "false", "false", "false"]);
    // a state beside it narrows the list further
    await click(radios()[2]);
    await settle();
    expect(listOutdatedDevices.mock.calls.at(-1)![0]).toMatchObject({ state: B, actual: C });
    // the same chip again: back to any class
    await click(chips()[2]);
    await settle();
    expect(listOutdatedDevices.mock.calls.at(-1)![0]).toMatchObject({ state: B, actual: ClientCompatActual.UNSPECIFIED });
    expect(chips()[0]!.getAttribute("aria-checked")).toBe("true");
  });

  it("goes back to the default view with 'Need attention', class included", async () => {
    await mount({ list: () => page() });
    await click(chips()[2]);
    await settle();
    await click(radios()[0]);
    await settle();
    expect(listOutdatedDevices.mock.calls.at(-1)![0]).toMatchObject({ state: ClientCompatState.UNSPECIFIED, actual: ClientCompatActual.UNSPECIFIED });
    expect(radios()[0]!.getAttribute("aria-checked")).toBe("true");
    expect(chips()[0]!.getAttribute("aria-checked")).toBe("true");
  });

  it("shows 'No data' for everybody and no lines of its own when nothing was measured", async () => {
    const quiet = seen.map((d) => ({ ...d, actual: N, helloPq: 0, helloClassic: 0, helloUnknown: 0, tableMismatch: "" }));
    await mount({
      list: () => page({ devices: quiet, actualCounts: measured.actualCounts.map((c) => ({ ...c, devices: c.actual === N ? 25 : 0 })), mismatchWorse: 0, mismatchBetter: 0 }),
    });
    expect(rows().map(cell)).toEqual(Array(6).fill("No data"));
    expect(chips().map(words)).toEqual(["All25", "New-style0", "Old-style0", "Mixed0", "No data25"]);
    expect(document.querySelector("[data-mismatch]")).toBeNull();
    expect(text()).not.toContain("do not count yet");
  });

  it("speaks Russian in plain words, with the term only in the tooltip text", async () => {
    setLang("ru");
    await mount({ list: () => page({ helloKexNodesWithout: ["nl1"] }) });
    expect([...document.querySelectorAll("table thead th")].map((h) => h.textContent)[4]).toBe("По факту");
    expect(chips().map(words)).toEqual(["Все25", "По-новому12", "По-старому3", "Смешанно1", "Нет данных9"]);
    expect(cell(rows()[0]!)).toContain("новых 0 · старых 5");
    expect(cell(rows()[1]!)).toContain("не прочитано: 1");
    expect(text()).toContain("1 нода ещё не считает: обнови агент (nl1)");
    expect(text()).toContain("«По факту» — что ноды реально видели у устройства за последние 7 дн.");
    expect([...rows(), ...chips()].some((el) => (el.textContent ?? "").includes("постквант"))).toBe(false);
    expect(rows()[0]!.querySelector("[data-mismatch=worse]")!.getAttribute("aria-label")).toContain("таблица здесь ошибается");
  });

  it("has words in all five languages for every key it adds", () => {
    const keys = Object.keys(dictionaries.en).filter((k) => /^cc\.(act|mis|leadActual|col\.actual|nodesWithout)/.test(k));
    expect(keys.length).toBeGreaterThanOrEqual(22);
    for (const lang of ["ru", "zh", "fa", "tr"] as const) for (const k of keys) expect(dictionaries[lang][k as keyof typeof dictionaries.en], `${lang} ${k}`).toBeTruthy();
  });
});

describe("In fact, the pure parts", () => {
  const t = (key: string, vars?: Record<string, string | number>) => dictionaries.en[key as keyof typeof dictionaries.en].replace(/\{(\w+)\}/g, (_, k: string) => String(vars?.[k] ?? ""));
  const tt = Object.assign(t, { n: t }) as never;
  const m = (o: Record<string, unknown> = {}) => ({ actual: ClientCompatActual.NONE, helloPq: 0, helloClassic: 0, helloUnknown: 0, tableMismatch: "", ...o }) as never;

  it("colours the classes: new-style fine, old-style bad, mixed warm, no data quiet", () => {
    expect(actualKind(ClientCompatActual.PQ)).toBe("ok");
    expect(actualKind(ClientCompatActual.CLASSIC)).toBe("bad");
    expect(actualKind(ClientCompatActual.MIXED)).toBe("warn");
    expect(actualKind(ClientCompatActual.NONE)).toBe("blip");
    expect(actualKind(ClientCompatActual.UNSPECIFIED)).toBe("blip");
  });

  it("writes the counts line only where something was counted", () => {
    expect(helloLine(tt, m())).toBe("");
    expect(helloLine(tt, m({ helloPq: 41, helloClassic: 2 }))).toBe("new 41 · old 2");
    expect(helloLine(tt, m({ helloUnknown: 3 }))).toBe("3 unreadable");
    expect(helloLine(tt, m({ helloClassic: 1, helloUnknown: 3 }))).toBe("new 0 · old 1 · 3 unreadable");
  });

  it("says a disagreement in a sentence, and nothing when the two agree", () => {
    expect(mismatchText(tt, m({ tableMismatch: "worse" }))).toContain("the table is wrong here");
    expect(mismatchText(tt, m({ tableMismatch: "better" }))).toContain("nothing to worry about");
    expect(mismatchText(tt, m())).toBe("");
    expect(mismatchText(tt, m({ tableMismatch: "something new" }))).toBe("");
  });

  it("rounds the window to days, at least one", () => {
    expect(windowDays(168)).toBe(7);
    expect(windowDays(24)).toBe(1);
    expect(windowDays(0)).toBe(1);
  });
});

describe("minimum versions", () => {
  it("is folded until asked for, with the number of rows on the button", async () => {
    await mount();
    expect(button("Show the table (2)")).toBeTruthy();
    expect(document.querySelector("input[type=text]")).toBeNull();
    await click(button("Show the table (2)"));
    expect(button("Hide")).toBeTruthy();
    expect(text()).toContain("Mihomo");
  });

  it("is read only for a helper: values as text, an unknown version as '?', no fields, no buttons, one line about the owner", async () => {
    await mount({ role: Role.HELPER });
    await click(button("Show the table"));
    expect(document.querySelector("input[type=text]")).toBeNull();
    expect(document.querySelector("[role=switch]")).toBeNull();
    expect(text()).toContain("3.1.1");
    expect(document.querySelector("[title=unknown]")?.textContent).toBe("?");
    expect(text()).toContain("Only the owner can change the table.");
    expect(button("Add an app")).toBeUndefined();
    expect(text()).not.toContain("Reset everything");
    expect(button("Revert")).toBeUndefined();
  });

  it("is edited by the owner: a changed row gets Revert, Save sends the whole table once", async () => {
    await mount();
    await click(button("Show the table"));
    expect(text()).not.toContain("Unsaved changes");
    const field = document.querySelector<HTMLInputElement>("input[aria-label='Happ: version for chrome']")!;
    expect(field.value).toBe("3.1.1");
    await typeInto(field, "3.2.x0");
    // only digits and dots stay
    expect(field.value).toBe("3.2.0");
    expect(text()).toContain("Unsaved changes");
    expect(button("Revert")).toBeTruthy();
    expect(document.querySelector("[aria-label='Differs from the built-in table']")).toBeTruthy();
    await click(button("Save"));
    await settle();
    expect(updateClientCompat).toHaveBeenCalledTimes(1);
    expect(updateClientCompat.mock.calls[0]![0]).toEqual({
      rules: [
        { app: "happ", platform: "", core: "xray", unsupported: false, minChrome: "3.2.0", minOther: "3.1.1" },
        { app: "mihomo", platform: "", core: "mihomo", unsupported: false, minChrome: "1.19.31", minOther: "" },
      ],
    });
    expect(document.body.textContent).toContain("Version table saved");
    expect(text()).not.toContain("Unsaved changes");
  });

  it("takes a change back with Revert and shows no bar for an edit that ends where it began", async () => {
    await mount();
    await click(button("Show the table"));
    const field = document.querySelector<HTMLInputElement>("input[aria-label='Mihomo: version for firefox/safari']")!;
    await typeInto(field, "1.20");
    expect(text()).toContain("Unsaved changes");
    await click(button("Revert"));
    expect(field.value).toBe("");
    expect(text()).not.toContain("Unsaved changes");
  });

  it("adds an app of the owner's own, which can be removed, and refuses a duplicate", async () => {
    await mount();
    await click(button("Show the table"));
    const name = document.querySelector<HTMLInputElement>("input[placeholder=happ]")!;
    await typeInto(name, "Happ");
    expect(name.value).toBe("happ");
    expect(text()).toContain("This row is already in the table");
    expect((button("Add")! as HTMLButtonElement).hasAttribute("data-disabled")).toBe(true);
    await typeInto(name, "Nova");
    await click(document.querySelector("form button[type=submit]"));
    expect(document.querySelector("input[aria-label='nova: version for chrome']")).toBeTruthy();
    expect(text()).toContain("Unsaved changes");
    await click(button("Remove"));
    expect(document.querySelector("input[aria-label='nova: version for chrome']")).toBeNull();
    expect(text()).not.toContain("Unsaved changes");
  });

  it("sends the built-in table to reset everything, after a confirmation", async () => {
    const edited = [rule("happ", "9.9", "3.1.1", { title: "Happ" }), defaults[1]!];
    await mount({ rules: edited });
    await click(button("Show the table"));
    await click(button("Reset everything to the built-in table"));
    await click([...document.querySelectorAll("[role=dialog] button")].find((b) => b.textContent === "Reset"));
    await settle();
    expect(updateClientCompat).toHaveBeenCalledTimes(1);
    expect((updateClientCompat.mock.calls[0]![0] as { rules: unknown[] }).rules).toEqual(defaults.map(({ title: _title, ...r }) => r));
  });
});

describe("pure parts", () => {
  const t = (key: string, vars?: Record<string, string | number>) => dictionaries.en[key as keyof typeof dictionaries.en].replace(/\{(\w+)\}/g, (_, k: string) => String(vars?.[k] ?? ""));
  const tt = Object.assign(t, { n: t }) as never;

  it("maps a state to the colour of its pill", () => {
    expect(stateKind(ClientCompatState.OK)).toBe("ok");
    expect(stateKind(B)).toBe("warn");
    expect(stateKind(U)).toBe("bad");
    expect(stateKind(K)).toBe("blip");
    expect(stateKind(ClientCompatState.UNSPECIFIED)).toBe("blip");
  });

  it("says what is missing for each reason, and nothing for a device that is fine", () => {
    const base = { state: B, reason: "", needVersion: "", fingerprint: "" };
    expect(needText(tt, { ...base, reason: "version", needVersion: "3.1.1" })).toBe("Needs version ≥ 3.1.1");
    expect(needText(tt, { ...base, state: U, reason: "app" })).toBe("This app cannot do it");
    expect(needText(tt, { ...base, state: U, reason: "fingerprint", fingerprint: "random" })).toContain("“random”");
    expect(needText(tt, { ...base, state: K, reason: "app_unknown" })).toBe("The app is not in the table");
    expect(needText(tt, { ...base, state: K, reason: "no_threshold" })).toBe("No known suitable version for this platform");
    expect(needText(tt, { ...base, state: K, reason: "no_version" })).toBe("The app did not report its version");
    expect(needText(tt, { ...base, state: ClientCompatState.OK, reason: "version" })).toBe("—");
    expect(needText(tt, { ...base, reason: "something new" })).toBe("—");
  });

  it("compares tables by app and platform, not by order or title", () => {
    const a = [rule("happ", "1", "2", { title: "Happ" }), rule("mihomo", "3", "")];
    expect(rulesEqual(a, [rule("mihomo", "3", "", { title: "Mihomo" }), rule("happ", "1", "2")])).toBe(true);
    expect(rulesEqual(a, [rule("mihomo", "3", ""), rule("happ", "1", "9")])).toBe(false);
    expect(rulesEqual(a, [rule("mihomo", "3", "")])).toBe(false);
    expect(rulesEqual(a, [rule("mihomo", "3", ""), rule("happ", "1", "2", { platform: "ios" })])).toBe(false);
  });

  it("has the tab to the right of 'Who gets what' and keeps a plain ?user in the URL", () => {
    expect(subsTabs).toEqual(["formats", "rules", "clients", "texts", "page", "dns"]);
    expect(validateSubsSearch({ tab: "clients", user: "alice" })).toEqual({ tab: "clients", user: "alice" });
    expect(validateSubsSearch({ tab: "formats" })).toEqual({});
    expect(validateSubsSearch({ tab: "nope", user: "a b" })).toEqual({});
    expect(validateSubsSearch({ tab: "clients", user: "" })).toEqual({ tab: "clients" });
    expect(validateSubsSearch({ tab: "clients", user: "x".repeat(65) })).toEqual({ tab: "clients" });
    expect(validateSubsSearch({ tab: "clients", user: 5 })).toEqual({ tab: "clients" });
    expect(validateSubsSearch({ tab: "clients", user: "a_b-9" }).user).toBe("a_b-9");
  });
});
