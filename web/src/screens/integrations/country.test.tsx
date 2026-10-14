import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { setLang } from "@/i18n";
import { CountryGroup } from "./country";

const getGlobalping = vi.fn();
const updateGlobalping = vi.fn();
const testGlobalping = vi.fn();
const disconnectGlobalping = vi.fn();
const getCountryReachability = vi.fn();
const getChecks = vi.fn();
const getBlocklist = vi.fn();
const updateBlocklist = vi.fn();
const testBlocklist = vi.fn();
const disconnectBlocklist = vi.fn();
vi.mock("@/lib/api", () => ({
  blocklist: {
    getBlocklist: (...a: unknown[]) => getBlocklist(...a),
    updateBlocklist: (...a: unknown[]) => updateBlocklist(...a),
    testBlocklist: (...a: unknown[]) => testBlocklist(...a),
    disconnectBlocklist: (...a: unknown[]) => disconnectBlocklist(...a),
  },
  globalping: {
    getGlobalping: (...a: unknown[]) => getGlobalping(...a),
    updateGlobalping: (...a: unknown[]) => updateGlobalping(...a),
    testGlobalping: (...a: unknown[]) => testGlobalping(...a),
    disconnectGlobalping: (...a: unknown[]) => disconnectGlobalping(...a),
  },
  countryReach: {
    getCountryReachability: (...a: unknown[]) => getCountryReachability(...a),
    updateCountryReachability: () => Promise.resolve({ settings: { country: "KZ" } }),
  },
  health: { getChecks: (...a: unknown[]) => getChecks(...a) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: 1 } }) },
  isUnauthenticated: () => false,
  webauthnSupported: () => false,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  getCountryReachability.mockResolvedValue({ settings: { country: "RU" } });
  getChecks.mockResolvedValue({ nowUnix: 0, intervalS: 300, columns: [], rows: [], countryChecks: [] });
  getBlocklist.mockResolvedValue({ settings: lists() });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  act(() => setLang("en"));
  for (const m of [getGlobalping, updateGlobalping, testGlobalping, disconnectGlobalping, getCountryReachability, getChecks, getBlocklist, updateBlocklist, testBlocklist, disconnectBlocklist]) m.mockReset();
});

const settings = (over: Record<string, unknown> = {}) => ({ enabled: false, intervalMin: 15, hasToken: false, ...over });
const lists = (over: Record<string, unknown> = {}) => ({ enabled: false, available: true, checkedUnix: 0, listsUnix: 0, ...over });

async function mount(first: object) {
  getGlobalping.mockResolvedValue({ settings: first });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <CountryGroup />
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 3; i++) await settle();
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const text = () => document.body.textContent ?? "";
const state = () => document.querySelector<HTMLElement>("[role=status][data-state]");
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = async (b: Element | undefined | null) => {
  await act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  for (let i = 0; i < 3; i++) await settle();
};
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]");
function type(input: Element | null, value: string) {
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  return act(async () => {
    set.call(input, value);
    input?.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

describe("the country group", () => {
  it("has its title, the shared country from its own service, and the Globalping card", async () => {
    await mount(settings());
    expect(text()).toContain("Reachability from a country");
    expect(getCountryReachability).toHaveBeenCalledTimes(1);
    expect(document.querySelector("[aria-label=Country]")?.textContent).toContain("Russia");
    expect(text()).toContain("Globalping");
  });
});

describe("the Globalping card", () => {
  it("is off by default: nothing runs, nothing to test, and it says what switching it on would send", async () => {
    await mount(settings());
    expect(state()?.dataset.state).toBe("off");
    expect(state()?.textContent).toBe("Off · Nothing runs and nothing is sent.");
    expect(document.querySelector("[role=switch]")?.getAttribute("aria-checked")).toBe("false");
    expect(button("Test")).toBeUndefined();
    expect(button("Disconnect")).toBeUndefined();
    expect(document.querySelector("input[type=password]")).toBeNull();
    expect(text()).toContain("Node addresses go to Globalping and its volunteer probes.");
    expect(getChecks).not.toHaveBeenCalled(); // off asks for no rounds either
  });

  it("switches on through UpdateGlobalping, then waits for a first test with the token field open", async () => {
    updateGlobalping.mockResolvedValue({ settings: settings({ enabled: true }) });
    await mount(settings());
    await click(document.querySelector("[role=switch]"));
    expect(updateGlobalping).toHaveBeenCalledTimes(1);
    expect(updateGlobalping).toHaveBeenCalledWith({ enabled: true });
    expect(state()?.dataset.state).toBe("untested");
    expect(state()?.textContent).toBe("On · not tested yet");
    expect(document.querySelector("input[type=password]")).not.toBeNull();
    expect(button("Test")).toBeDefined();
  });

  it("switches off through UpdateGlobalping as well", async () => {
    updateGlobalping.mockResolvedValue({ settings: settings({ hasToken: true }) });
    await mount(settings({ enabled: true, hasToken: true }));
    await click(document.querySelector("[role=switch]"));
    expect(updateGlobalping).toHaveBeenCalledWith({ enabled: false });
    expect(state()?.dataset.state).toBe("off");
    // the token stays for switching back on: it can still be cleared
    expect(button("Disconnect")).toBeDefined();
  });

  it("shows only “set” for a stored token and sends a new one once, in the update and nowhere else", async () => {
    updateGlobalping.mockResolvedValue({ settings: settings({ enabled: true, hasToken: true }) });
    await mount(settings({ enabled: true }));
    await click(button("Save token"));
    expect(text()).toContain("This does not look like a Globalping token.");
    expect(updateGlobalping).not.toHaveBeenCalled();
    await type(document.querySelector("input[type=password]"), " gp-token-0123456789 ");
    await click(button("Save token"));
    expect(updateGlobalping).toHaveBeenCalledWith({ token: "gp-token-0123456789" });
    expect(document.querySelector("[data-testid=gp-token]")?.textContent).toBe("set");
    expect(document.querySelector("input[type=password]")).toBeNull();
    expect(document.body.innerHTML).not.toContain("gp-token-0123456789");
  });

  it("tests through TestGlobalping and says what is left of the hour", async () => {
    testGlobalping.mockResolvedValue({ reachable: true, tokenUsed: true, limit: 500, remaining: 412, resetS: 1800, credits: 0 });
    await mount(settings({ enabled: true, hasToken: true }));
    await click(button("Test"));
    expect(testGlobalping).toHaveBeenCalledTimes(1);
    expect(testGlobalping).toHaveBeenCalledWith({});
    expect(state()?.dataset.state).toBe("works");
    expect(state()?.textContent).toBe("Works · 412 of 500 tests left this hour · token accepted");
  });

  it("fails when the test fails, in words", async () => {
    testGlobalping.mockRejectedValue(new ConnectError("globalping_unauthorized", Code.FailedPrecondition));
    await mount(settings({ enabled: true, hasToken: true }));
    await click(button("Test"));
    expect(state()?.dataset.state).toBe("fails");
    expect(state()?.textContent).toBe("Does not work · Globalping refused the token. Paste a new one or disconnect.");
  });

  it("does not count a test asked for too soon as a failure", async () => {
    testGlobalping.mockRejectedValue(new ConnectError("too soon", Code.ResourceExhausted));
    await mount(settings({ enabled: true, hasToken: true }));
    await click(button("Test"));
    expect(state()?.dataset.state).toBe("untested");
  });

  it("works on the rounds alone, and has no data when the newest round failed on Globalping's side", async () => {
    const at = Math.floor(Date.now() / 1000) - 240;
    const probe = { network: "Rostelecom", asn: 1, city: "Moscow", icmpOk: true, icmpMs: 42, tcp: [], valid: true };
    const round = (verdict: number, reason = "") => ({ nodeId: "nod_1", country: "RU", verdict, atUnix: at, probes: reason ? [] : [probe], reason, eyeball: true, confidence: "", listedBy: [], cdnProvider: "", cdnRegion: "" });
    getChecks.mockResolvedValue({ nowUnix: at, intervalS: 300, columns: [], rows: [], countryChecks: [round(CountryVerdict.OK)] });
    await mount(settings({ enabled: true, hasToken: true }));
    expect(state()?.textContent).toBe("Works · last round 4 min ago");
    act(() => root?.unmount());
    host?.remove();
    getChecks.mockResolvedValue({ nowUnix: at, intervalS: 300, columns: [], rows: [], countryChecks: [round(CountryVerdict.UNKNOWN, "rate_limited")] });
    await mount(settings({ enabled: true, hasToken: true }));
    expect(state()?.dataset.state).toBe("noData");
    expect(state()?.textContent).toBe("On, but no data · Globalping’s hourly limit is used up · last round 4 min ago");
  });

  it("disconnects only after a confirmation, through DisconnectGlobalping, which clears the token", async () => {
    disconnectGlobalping.mockResolvedValue({ settings: settings() });
    await mount(settings({ enabled: true, hasToken: true }));
    await click(button("Disconnect"));
    expect(disconnectGlobalping).not.toHaveBeenCalled();
    expect(dialog()?.textContent).toContain("Disconnect Globalping?");
    expect(dialog()?.textContent).toContain("the token is deleted, and the results leave Health and the node pages");
    await click([...dialog()!.querySelectorAll("button")].find((b) => b.textContent === "Disconnect"));
    expect(disconnectGlobalping).toHaveBeenCalledTimes(1);
    expect(disconnectGlobalping).toHaveBeenCalledWith({});
    expect(updateGlobalping).not.toHaveBeenCalled();
    expect(state()?.dataset.state).toBe("off");
    expect(button("Disconnect")).toBeUndefined();
  });

  it("speaks Russian and Chinese", async () => {
    act(() => setLang("ru"));
    await mount(settings());
    expect(text()).toContain("Доступность из страны");
    expect(state()?.textContent).toBe("Выключено · Ничего не запускается и никуда не отправляется.");
    expect(text()).toContain("Адреса нод уходят в Globalping и на его волонтёрские зонды.");
    act(() => root?.unmount());
    host?.remove();
    act(() => setLang("zh"));
    await mount(settings({ enabled: true, hasToken: true }));
    expect(text()).toContain("从所选国家访问");
    expect(document.querySelector("[data-testid=gp-token]")?.textContent).toBe("已设置");
    expect(text()).toContain("节点地址会发送给 Globalping 及其志愿者探测点。");
    expect(button("测试")).toBeDefined();
    expect(button("断开连接")).toBeDefined();
  });
});

describe("the block-list card", () => {
  // the card of its own, next to Globalping's: its state line, its buttons
  const card = (title = "Block lists") => [...document.querySelectorAll("section")].find((s) => s.querySelector("h3")?.textContent === title);
  const listsState = (title?: string) => card(title)?.querySelector<HTMLElement>("[role=status][data-state]");
  const listsButton = (label: string, title?: string) => [...(card(title)?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === label);
  const listsSwitch = () => card()?.querySelector("[role=switch]");
  const at = Math.floor(Date.now() / 1000) - 240;
  const listsAt = Math.floor(new Date(2026, 9, 14, 18, 5).getTime() / 1000);

  it("sits next to the Globalping card in the group, off by default: nothing downloaded, nothing to test, the privacy line", async () => {
    await mount(settings());
    const grid = card()?.parentElement;
    expect([...grid!.children].map((c) => c.querySelector("h3")?.textContent)).toEqual(["Globalping", "Block lists"]);
    expect(listsState()?.dataset.state).toBe("off");
    expect(listsState()?.textContent).toBe("Off · Nothing is downloaded.");
    expect(listsSwitch()?.getAttribute("aria-checked")).toBe("false");
    expect(listsSwitch()?.getAttribute("aria-label")).toBe("Use the block lists");
    expect(listsButton("Test")).toBeUndefined();
    expect(listsButton("Disconnect")).toBeUndefined();
    expect(card()?.textContent).toContain("The lists are downloaded to the panel and compared here: no node address leaves the panel.");
    expect(testBlocklist).not.toHaveBeenCalled();
  });

  it("switches on and off through UpdateBlocklist, then waits for a first test", async () => {
    updateBlocklist.mockResolvedValue({ settings: lists({ enabled: true }) });
    await mount(settings());
    await click(listsSwitch());
    expect(updateBlocklist).toHaveBeenCalledWith({ enabled: true });
    expect(updateGlobalping).not.toHaveBeenCalled();
    expect(listsState()?.dataset.state).toBe("untested");
    expect(listsState()?.textContent).toBe("On · not tested yet");
    expect(listsButton("Test")).toBeDefined();
    expect(listsButton("Disconnect")).toBeDefined();
    updateBlocklist.mockResolvedValue({ settings: lists() });
    await click(listsSwitch());
    expect(updateBlocklist).toHaveBeenLastCalledWith({ enabled: false });
    expect(listsState()?.dataset.state).toBe("off");
  });

  it("says only for Russia when the country is another one, and offers no test", async () => {
    getBlocklist.mockResolvedValue({ settings: lists({ enabled: true, available: false }) });
    await mount(settings());
    expect(listsState()?.dataset.state).toBe("unavailable");
    expect(listsState()?.textContent).toBe("Not available · only for Russia; nothing is downloaded");
    expect(listsButton("Test")).toBeUndefined();
    expect(listsButton("Disconnect")).toBeDefined();
  });

  it("works on the stored check: the date of the lists and when they were compared", async () => {
    getBlocklist.mockResolvedValue({ settings: lists({ enabled: true, checkedUnix: at, listsUnix: listsAt }) });
    await mount(settings());
    expect(listsState()?.dataset.state).toBe("works");
    expect(listsState()?.textContent).toBe("Works · lists of Oct 14, 18:05 · compared 4 min ago");
  });

  it("tests through TestBlocklist and says what it found: prefixes, CDN ranges, nodes listed, the mirror", async () => {
    getBlocklist.mockResolvedValue({ settings: lists({ enabled: true }) });
    testBlocklist.mockResolvedValue({ listsUnix: listsAt, prefixes: 20580, cdnRanges: 1234, nodesChecked: 4, nodesListed: 1, mirrorUsed: true });
    await mount(settings());
    await click(listsButton("Test"));
    expect(testBlocklist).toHaveBeenCalledTimes(1);
    expect(testBlocklist).toHaveBeenCalledWith({});
    expect(testGlobalping).not.toHaveBeenCalled();
    expect(listsState()?.dataset.state).toBe("works");
    expect(listsState()?.textContent).toBe("Works · lists of Oct 14, 18:05 · 20,580 prefixes · 1,234 CDN ranges · 1 of 4 nodes listed · main list from the mirror");
  });

  it("fails when the test fails, in words, and does not count a test asked for too soon", async () => {
    getBlocklist.mockResolvedValue({ settings: lists({ enabled: true }) });
    testBlocklist.mockRejectedValue(new ConnectError("blocklist_unreachable", Code.Unavailable));
    await mount(settings());
    await click(listsButton("Test"));
    expect(listsState()?.dataset.state).toBe("fails");
    expect(listsState()?.textContent).toBe("Does not work · Could not download the lists, and the panel holds no earlier copy.");
    testBlocklist.mockRejectedValue(new ConnectError("tested a moment ago", Code.ResourceExhausted));
    await click(listsButton("Test"));
    expect(listsState()?.dataset.state).toBe("fails"); // still the last real answer, the toast says to wait
  });

  it("disconnects only after a confirmation, through DisconnectBlocklist, which also deletes the results", async () => {
    getBlocklist.mockResolvedValue({ settings: lists({ enabled: true, checkedUnix: at, listsUnix: listsAt }) });
    disconnectBlocklist.mockResolvedValue({ settings: lists() });
    await mount(settings());
    await click(listsButton("Disconnect"));
    expect(disconnectBlocklist).not.toHaveBeenCalled();
    expect(dialog()?.textContent).toContain("Disconnect the block lists?");
    expect(dialog()?.textContent).toContain("the downloaded lists and every stored result are deleted");
    await click([...dialog()!.querySelectorAll("button")].find((b) => b.textContent === "Disconnect"));
    expect(disconnectBlocklist).toHaveBeenCalledTimes(1);
    expect(disconnectBlocklist).toHaveBeenCalledWith({});
    expect(updateBlocklist).not.toHaveBeenCalled();
    expect(disconnectGlobalping).not.toHaveBeenCalled();
    expect(listsState()?.dataset.state).toBe("off");
    expect(listsButton("Disconnect")).toBeUndefined();
  });

  it("speaks Russian and Chinese", async () => {
    act(() => setLang("ru"));
    getBlocklist.mockResolvedValue({ settings: lists({ enabled: true, available: false }) });
    await mount(settings());
    expect(listsState("Списки блокировок")?.textContent).toBe("Недоступно · только для России, ничего не скачивается");
    expect(card("Списки блокировок")?.textContent).toContain("Списки скачиваются на панель и сверяются здесь же: адреса нод не покидают панель.");
    expect(listsButton("Отключить", "Списки блокировок")).toBeDefined();
    act(() => root?.unmount());
    host?.remove();
    getBlocklist.mockResolvedValue({ settings: lists({ enabled: true }) });
    testBlocklist.mockResolvedValue({ listsUnix: listsAt, prefixes: 20581, cdnRanges: 2, nodesChecked: 1, nodesListed: 1, mirrorUsed: false });
    await mount(settings());
    await click(listsButton("Проверить", "Списки блокировок"));
    expect(listsState("Списки блокировок")?.textContent).toBe(`Работает · списки от 14 окт., 18:05 · ${(20581).toLocaleString("ru-RU")} префикс · 2 диапазона CDN · в реестре 1 из 1 ноды`);
    act(() => root?.unmount());
    host?.remove();
    act(() => setLang("zh"));
    getBlocklist.mockResolvedValue({ settings: lists() });
    await mount(settings());
    expect(listsState("封锁名单")?.textContent).toBe("已关闭 · 不会下载任何内容。");
    expect(card("封锁名单")?.textContent).toContain("名单会下载到面板，并在面板上比对：节点地址不会离开面板。");
  });
});
