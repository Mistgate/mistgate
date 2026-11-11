import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang } from "@/i18n";
import { ClientCompatState, needText, rulesEqual, stateKind, type CompatRule } from "@/lib/clientcompat";
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
const dev = (o: Record<string, unknown>) => ({ deviceId: "d1", userId: "u1", userName: "alice", client: "", platform: "", model: "", osVersion: "", shared: false, lastSeenUnix: 0n, state: B, reason: "version", needVersion: "", fingerprint: "", appTitle: "", ...o });
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
const rule = (app: string, minChrome: string, minOther: string, o: Partial<CompatRule> = {}): CompatRule => ({ app, platform: "", core: "xray", unsupported: false, minChrome, minOther, title: app, ...o });
const defaults = [rule("happ", "3.1.1", "3.1.1", { title: "Happ" }), rule("mihomo", "1.19.31", "", { title: "Mihomo", core: "mihomo" })];
const table = (rules: CompatRule[] = defaults) => ({ rules, defaults });

/** The tab as `role` sees it; `list` answers the device query (by default the three devices and the counts above). */
async function mount(o: { role?: Role; userId?: string; onClearUser?: () => void; rules?: CompatRule[]; list?: (req: { state: number }) => unknown } = {}) {
  const { role = Role.OWNER } = o;
  getClientCompat.mockResolvedValue(table(o.rules));
  listOutdatedDevices.mockImplementation(async (req: { state: number }) => o.list?.(req) ?? { devices, total: devices.length, counts, fingerprints: ["chrome", "firefox"] });
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
const radios = () => [...document.querySelectorAll<HTMLElement>("[role=radio]")];
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
    expect(listOutdatedDevices.mock.calls[0]![0]).toEqual({ state: ClientCompatState.UNSPECIFIED, userId: "", pageSize: 50, offset: 0 });
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
    await mount({ list: () => ({ devices: [], total: 0, counts: zero, fingerprints: [] }) });
    expect(text()).toContain("No device gets REALITY servers yet");
    expect(radios()).toHaveLength(0);
    act(() => root?.unmount());
    host?.remove();

    await mount({ list: () => ({ devices: [], total: 0, counts: [{ state: ClientCompatState.OK, devices: 5, people: 2 }, ...counts.slice(1).map((c) => ({ ...c, devices: 0, people: 0 }))], fingerprints: [] }) });
    expect(text()).toContain("All devices are fine");
    expect(text()).not.toContain("No device gets REALITY servers yet");
    expect(radios()).toHaveLength(5);
  });

  it("says a state has no devices", async () => {
    await mount({ list: (req) => (req.state === U ? { devices: [], total: 0, counts, fingerprints: [] } : { devices, total: 3, counts, fingerprints: [] }) });
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
