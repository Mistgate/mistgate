import { describe, expect, it } from "vitest";
import { amzActions, type Api } from "./amz-actions";
import { ApiError } from "./api";
import { dict, langs } from "./i18n";
import { awgDevice, deviceAbout, fetchedUnix, isReturning, normalize } from "./logic";
import { newAmzState } from "./state";
import { actions, amzNoop, data, plain, state, text } from "./test-kit";
import type { Device, MgData } from "./types";
import { view } from "./view";

// The devices as the board draws them: the card of the places (the figure, the bar, "Add a device", the link), then the list: a tile,
// the name, one quiet line, when it last updated, and "more", which opens the window of the device. The devices of the apps on the
// link (each app that reports a device ID is a device of its own), the shared place of the apps that report none, the keys.

const settle = () => new Promise((r) => setTimeout(r, 0));
const page = (d: MgData, over: Parameters<typeof state>[1] = {}, lang: "ru" | "en" | "zh" | "fa" | "tr" = "ru", a = actions()) => view(d, state("ios", { returning: true, tab: "devices", devicesOpen: true, ...over }, lang), a);
/** The places card and the list card. */
const places = (el: HTMLElement) => el.querySelector<HTMLElement>(".u-dvh")!;
const list = (el: HTMLElement) => el.querySelector<HTMLElement>(".u-dvl")!;
const rows = (el: HTMLElement) => [...list(el).querySelectorAll<HTMLElement>(".u-dv")];
const names = (el: HTMLElement) => rows(el).map((r) => text(r.querySelector(".nm")));
const row = (el: HTMLElement, id: string) => el.querySelector<HTMLElement>(`#amz-row-${id}`)!;
const byName = (el: HTMLElement, name: string) => rows(el).find((x) => text(x.querySelector(".nm")) === name)!;
/** The quiet line under a name (the phone's copy of the time left out), and the time of a computer. */
const meta = (r: HTMLElement) => text(r.querySelector(".mt")).replace(text(r.querySelector(".mt .only-m")), "");
const when = (r: HTMLElement) => text(r.querySelector(".when"));

function fakeApi(over: Partial<Api> = {}): Api & { calls: string[] } {
  const calls: string[] = [];
  const no = async () => Promise.reject(new ApiError(500, "failed"));
  return {
    calls,
    addDevice: no,
    getConfigs: no,
    rotateKey: no,
    revokeDevice: async (url, id) => {
      calls.push(`revoke ${url} ${id}`);
    },
    renameDevice: async (url, id, label) => {
      calls.push(`rename ${url} ${id} ${label}`);
      return { device: awgDevice({ id, label: label.slice(0, 40) }), configs: [] };
    },
    ...over,
  };
}
/** The actions of a page with the link apps' address and the keys' address told apart. */
function rig(name: string, api = fakeApi()) {
  const d = data(name, (x) => {
    x.devices_endpoint = "https://sub.example.com/s/tok/devices";
    if (x.amnezia) x.amnezia.endpoints = "https://sub.example.com/s/tok/keys";
  });
  const st = state("ios", { returning: true });
  const a = amzActions({ data: d, st, render: () => {}, api, reveal: () => {} });
  return { d, st, a, api };
}

describe("the page data of the link devices", () => {
  it("reads the kind, what the person may do, what the app said and the mode; a missing kind is told by the old rule", () => {
    const d = normalize({
      devices: [
        { id: "a", platform: "ios", model: "iPhone", kind: "link", can_remove: true, can_rename: true, os_version: " 18.1 ", client: "Happ/3.4.2" },
        { id: "b", kind: "shared" },
        { id: "c", platform: "", model: "" },
        { id: "d", platform: "android", app: "amnezia" },
        { id: "e", platform: "android", kind: "other" },
      ],
      devices_endpoint: "https://sub.example.com/s/tok/devices",
      link_device_limit: "strict",
    });
    expect(d.devices.map((x) => x.kind)).toEqual(["link", "shared", "shared", "key", "link"]);
    expect(d.devices[0]).toMatchObject({ can_remove: true, can_rename: true, os_version: "18.1", client: "Happ/3.4.2" });
    expect(d.devices[1]).toMatchObject({ can_remove: false, can_rename: false, os_version: "", client: "" });
    expect(d.devices_endpoint).toBe("https://sub.example.com/s/tok/devices");
    expect(d.link_device_limit).toBe("strict");
    expect(normalize({ link_device_limit: "off" }).link_device_limit).toBe("track");
    expect(normalize({}).devices_endpoint).toBe("");
  });

  it("what the app said, for telling two phones apart: the OS and its version, the app and its version", () => {
    const x = (platform: string, os_version: string, client: string) => ({ platform, os_version, client }) as Device;
    expect(deviceAbout(x("ios", "18.1", "Happ/3.4.2"))).toBe("iOS 18.1 · Happ 3.4.2");
    expect(deviceAbout(x("ios", "27.0", "Happ/6.0.0/ios/2609301456616"))).toBe("iOS 27.0 · Happ 6.0.0"); // as stored before the panel cut it
    expect(deviceAbout(x("android", "Android 14", ""))).toBe("Android 14");
    expect(deviceAbout(x("windows", "", "mihomo/1.19.31"))).toBe("Windows · mihomo 1.19.31");
    expect(deviceAbout(x("", "", ""))).toBe("");
    expect(deviceAbout(x("ios", "", ""))).toBe(""); // the platform alone is no detail
  });

  it("an app that took a device of its own counts as having fetched the subscription (the returning visitor)", () => {
    const d = data("hwid-strict");
    expect(fetchedUnix(d)).toBeGreaterThan(0);
    expect(isReturning(d, false)).toBe(true);
    expect(fetchedUnix(data("hwid-strict", (x) => (x.devices = x.devices.map((y) => ({ ...y, kind: "key" })))))).toBe(0);
  });
});

describe("the card of the places", () => {
  it("the figure with its words, and the bar: a computer says 'places taken', a phone shortens it", () => {
    const el = page(data("hwid-track"));
    const p = places(el);
    expect(p.getAttribute("aria-label")).toBe("Места для устройств");
    expect(text(p.querySelector(".u-fig"))).toBe("4 из 5мест занято, свободно 1 местомест, свободно 1 место");
    expect(text(p.querySelector(".u-fig .only-w"))).toBe("мест занято, свободно 1 место");
    expect(text(p.querySelector(".u-fig .only-m"))).toBe("мест, свободно 1 место");
    const bar = p.querySelector(".u-prog")!;
    expect(bar.getAttribute("role")).toBe("img");
    expect(bar.getAttribute("aria-label")).toBe("Занято 4 из 5");
    expect(bar.querySelector("i")?.getAttribute("style")).toContain("width: 80%");
    expect(bar.querySelector("i")?.classList.contains("full")).toBe(false);
    expect(text(page(data("hwid-track"), {}, "en").querySelector(".u-fig .only-w"))).toBe("slots used, 1 slot free");
  });

  it("every place taken: the bar is amber and says no place is free; with the link apps refused it says how to free one, calmly", () => {
    const el = page(data("hwid-full"));
    const p = places(el);
    expect(p.querySelector(".u-prog i")?.classList.contains("full")).toBe(true);
    expect(text(p.querySelector(".u-fig .only-w"))).toBe("мест занято, свободных мест нет");
    const hint = p.querySelector("#dev-full")!;
    expect(text(hint)).toBe("Все места заняты. Удалите ненужное устройство и обновите подписку в приложении.");
    expect(hint.getAttribute("role")).toBeNull(); // not an alert: nothing went wrong
    expect(p.getAttribute("aria-describedby")).toBe("dev-full");
    expect(text(places(page(data("hwid-full"), {}, "en")).querySelector("#dev-full"))).toBe("All slots are used. Remove an old device, then refresh the subscription in the app.");
    expect(text(places(page(data("hwid-full"), {}, "zh")).querySelector("#dev-full"))).toBe("名额已用完。请删除不用的设备，然后在应用中更新订阅。");
    // "Add a device" stays: the window says what can be done
    expect(p.querySelector<HTMLButtonElement>("[data-k=dev-add]")?.disabled).toBe(false);
  });

  it("not in TRACK (nobody is refused), not with room left, not without the link apps", () => {
    const has = (d: MgData) => page(d).querySelector("#dev-full") !== null;
    expect(has(data("hwid-full", (d) => (d.link_device_limit = "track")))).toBe(false);
    expect(has(data("hwid-full", (d) => (d.user.device_limit = 5)))).toBe(false);
    expect(has(data("hwid-full", (d) => (d.access.happ = false)))).toBe(false);
    expect(has(data("hwid-strict", (d) => ((d.user.device_limit = 3), (d.user.devices_used = 3))))).toBe(true);
  });

  it("no limit: the number alone, 'no limit' in words, no bar", () => {
    const el = page(data("hwid-track", (d) => (d.user.device_limit = 0)));
    expect(text(places(el).querySelector(".u-fig"))).toBe("4устройств, без ограничения");
    expect(places(el).querySelector(".u-prog")).toBeNull();
  });

  it("'Add a device' opens the window; the link is copied by the button (both sizes), which says it is done", () => {
    const a = actions();
    const el = page(data("hwid-track"), {}, "ru", a);
    const add = [...places(el).querySelectorAll("button")].filter((b) => text(b) === "Добавить устройство");
    expect(add).toHaveLength(1);
    expect(add[0]!.getAttribute("data-k")).toBe("dev-add");
    expect(add[0]!.classList.contains("pri")).toBe(true);
    add[0]!.click();
    const desk = places(el).querySelector<HTMLButtonElement>("[data-k=dev-link-copy]")!;
    const small = places(el).querySelector<HTMLButtonElement>("[data-k=dev-link-copy-m]")!;
    expect([text(desk).startsWith("Скопировать ссылку"), desk.classList.contains("only-w"), desk.classList.contains("u-cpb")]).toEqual([true, true, true]);
    expect([text(small).startsWith("Ссылка"), small.classList.contains("only-m")]).toEqual([true, true]);
    desk.click();
    small.click();
    expect(a.log).toEqual(["add true", `copy ${data("hwid-track").subscription_url}`, "mark", `copy ${data("hwid-track").subscription_url}`, "mark"]);
    expect(desk.dataset.cp).toBe("done");
  });

  it("keys alone: no link to copy; no way to connect at all: no buttons", () => {
    const keys = page(data("many", (d) => (d.access.happ = false)));
    expect(places(keys).querySelector("[data-k=dev-add]")).not.toBeNull();
    expect(places(keys).querySelector("[data-k^=dev-link-copy]")).toBeNull();
    const none = page(data("many", (d) => ((d.access.happ = false), (d.access.amnezia = false), (d.apps = []))));
    expect(places(none).querySelector("button")).toBeNull();
  });

  it("zero devices: the figure and one line that says to add the first", () => {
    const el = page(data("amnezia-empty", (d) => ((d.devices = []), (d.access.happ = false), (d.user.devices_used = 0))));
    expect(text(places(el).querySelector(".u-fig .only-w"))).toBe("мест занято, свободно 3 места");
    expect(rows(el)).toHaveLength(0);
    expect(text(list(el).querySelector(".u-empty"))).toBe("Устройств пока нет. Добавьте первое, и оно появится здесь");
    expect(list(el).querySelector(".u-more")).toBeNull();
  });
});

describe("the rows of the link devices", () => {
  it("one row per device, the one online first, then the shared place; a tile, a name and what the app said", () => {
    const el = page(data("hwid-track"));
    expect(names(el)).toEqual(["iPhone 15", "Pixel 8", "DESKTOP-4F7K2LM", "Приложения без ID устройства"]);
    expect(meta(row(el, "l1"))).toBe("iOS 18.1 · Happ 3.4.2");
    expect(meta(row(el, "l2"))).toBe("Android 15 · Happ 3.4.1");
    expect(meta(row(el, "l3"))).toBe("Windows 10.0.26100 · mihomo 1.19.31");
    expect(row(el, "l1").querySelector(".tl svg")).not.toBeNull();
    expect(row(el, "l1").querySelector(".tl")?.getAttribute("aria-hidden")).toBe("true");
    // the one online sorts first whatever the server's order
    expect(names(page(data("hwid-track", (d) => d.devices.reverse())))[0]).toBe("iPhone 15");
    // no device is a row of the old kind
    expect(el.querySelector(".dev, .dev-n, .dev-m, .dev-body")).toBeNull();
  });

  it("when it last updated: a computer has its own column, a phone has the time at the end of the quiet line; online has a dot", () => {
    const el = page(data("hwid-track"));
    expect(when(row(el, "l1"))).toBe("в сети");
    expect(row(el, "l1").querySelector(".when .u-dot")).not.toBeNull();
    expect(when(row(el, "l2"))).toBe("обновлялось позавчера");
    expect(row(el, "l2").querySelector(".when")?.classList.contains("only-w")).toBe(true);
    expect(text(row(el, "l2").querySelector(".mt .only-m"))).toBe(" · обновлялось позавчера");
    expect(row(el, "l2").querySelector(".mt .only-m")?.classList.contains("only-m")).toBe(true);
    expect(when(page(data("hwid-track"), {}, "en").querySelector<HTMLElement>("#amz-row-l2")!)).toBe("updated 2 days ago");
  });

  it("'more' is one button on the row: the window of the device opens; it names the device and says it opens a window", () => {
    const a = actions();
    const log: string[] = [];
    a.amz = { ...a.amz, menu: (id) => void log.push(`menu ${id}`) };
    const el = page(data("hwid-track"), {}, "ru", a);
    const more = row(el, "l2").querySelector<HTMLButtonElement>("[data-k=amz-more-l2]")!;
    expect(more.classList.contains("u-ic")).toBe(true);
    expect(more.getAttribute("aria-label")).toBe("Ещё: Pixel 8");
    expect(more.getAttribute("aria-haspopup")).toBe("dialog");
    expect([...row(el, "l2").querySelectorAll("button")]).toEqual([more]);
    more.click();
    expect(log).toEqual(["menu l2"]);
    // nothing of the old row is left in it: no menu, no question, no field
    expect(el.querySelector(".menu, [role=menu], [role=alertdialog], form, input")).toBeNull();
    expect(byName(el, "Приложения без ID устройства").querySelector("button")).toBeNull();
  });

  it("nothing to press where the person may not change the device (the admin's preview, self-service off)", () => {
    const el = page(data("hwid-track", (d) => d.devices.forEach((x) => ((x.can_remove = false), (x.can_rename = false)))));
    expect(el.querySelector("[data-k^=amz-more-]")).toBeNull();
    expect(names(el)).toHaveLength(4);
  });

  it("a device that said nothing has the platform's word instead, unless that is its name already", () => {
    const quiet = page(data("hwid-full", (d) => d.devices.forEach((x) => ((x.os_version = ""), (x.client = "")))));
    expect(meta(row(quiet, "l1"))).toBe("iPhone");
    expect(meta(row(quiet, "l2"))).toBe("Android");
    const named = page(data("hwid-full", (d) => d.devices.forEach((x) => ((x.os_version = ""), (x.client = ""), (x.model = "")))));
    expect(names(named).slice(0, 2)).toEqual(["iPhone", "Android"]);
    expect(meta(row(named, "l1"))).toBe("");
  });

  it("the tile is the board's: a phone, a Mac, a computer; a device with no platform has a plain one", () => {
    const d = data("hwid-track", (x) => ((x.devices[2]!.platform = "macos"), x.devices.push({ ...x.devices[0]!, id: "l9", platform: "", model: "", online: false, last_seen_unix: 1 })));
    const el = page(d, { devicesOpen: true });
    const path = (id: string) => row(el, id).querySelector(".tl svg path")?.getAttribute("d") ?? "";
    expect(path("l1")).toBe(path("l2")); // both phones: one tile
    expect(path("l1")).toContain("M9.3 2.5h5.4");
    expect(path("l3")).toContain("M5 15.5v-9"); // a Mac
    expect(names(el)).toContain("Устройство");
  });
});

describe("the shared place, by the mode", () => {
  const shared = (el: HTMLElement) => byName(el, "Приложения без ID устройства");

  it("TRACK and ENFORCE: they share one place and the last app; when they last updated is its own column", () => {
    for (const name of ["hwid-track", "hwid-full"]) {
      const r = shared(page(data(name)));
      expect(meta(r)).toBe("одно место на всех · последнее приложение: HiddifyNext 2.5.7");
      expect(when(r)).toBe("обновлялись 9 дней назад");
    }
    expect(when(shared(page(data("hwid-full", (d) => (d.devices.find((x) => x.kind === "shared")!.online = true)))))).toBe("в сети");
    expect(meta(byName(page(data("hwid-track"), {}, "en"), "Apps without a device ID"))).toBe("one slot for all · last app: HiddifyNext 2.5.7");
    expect(meta(byName(page(data("hwid-track"), {}, "zh"), "不提供设备 ID 的应用"))).toBe("共用一个名额 · 最近的应用：HiddifyNext 2.5.7");
  });

  it("STRICT: a quiet row that is no device says the apps without a device ID do not connect, and which to use", () => {
    const el = page(data("hwid-strict"));
    const r = shared(el);
    expect(meta(r)).toBe("не подключатся — нужен Happ или kl!ck");
    expect(r.classList.contains("quiet")).toBe(true);
    expect(r.querySelector("button")).toBeNull();
    expect(r.querySelector(".when")).toBeNull();
    expect(names(el).at(-1)).toBe("Приложения без ID устройства");
    // a shared place left over says the same
    expect(meta(shared(page(data("hwid-full", (d) => (d.link_device_limit = "strict")))))).toBe("не подключатся — нужен Happ или kl!ck");
    // with no app at all, STRICT says it instead of "not connected yet"
    expect(names(page(data("hwid-strict", (d) => (d.devices = []))))).toEqual(["Приложения без ID устройства"]);
  });

  it("before any app took the link: the link's own row, 'not connected yet'; the link to copy is on the card above", () => {
    const el = page(data("hwid-track", (d) => (d.devices = [])));
    expect(names(el)).toEqual(["Приложения по ссылке"]);
    expect(when(byName(el, "Приложения по ссылке"))).toBe("ещё не подключались");
    expect(byName(el, "Приложения по ссылке").querySelector("button")).toBeNull();
    expect(el.querySelector("[data-k=dev-link-copy]")?.closest(".u-dvh")?.querySelector("[data-k=dev-add]")).not.toBeNull();
  });
});

describe("the Telegram and TrustTunnel keys", () => {
  const tg = (over: Partial<MgData["devices"][number]>) =>
    data("hwid-track", (d) =>
      d.devices.push({ ...d.devices.find((x) => x.kind === "link")!, id: "dev_tg", kind: "telegram", model: "Telegram proxy", app: "telegram", can_remove: false, can_rename: false, online: false, last_seen_unix: 0, ...over }),
    );
  // the list is long here: unfolded, so the key's row is on the page wherever it sorts
  const open = { devicesOpen: true };

  it("says when Telegram last used it, in its own words, with nothing to rename or remove", () => {
    expect(when(row(page(tg({}), open), "dev_tg"))).toBe("ещё не подключался");
    expect(when(row(page(tg({ online: true }), open), "dev_tg"))).toBe("в сети");
    expect(when(row(page(tg({ last_seen_unix: Math.floor(Date.now() / 1000) - 9 * 86400 }), open), "dev_tg"))).toBe("подключался 9 дней назад");
    const r = row(page(tg({}), open), "dev_tg");
    expect(text(r.querySelector(".nm"))).toBe("Telegram-прокси");
    expect(r.querySelector("button")).toBeNull();
    expect(when(row(page(tg({}), open, "en"), "dev_tg"))).toBe("not connected yet");
  });

  it("keeps its kind through the page's own reading of the data", () => {
    expect(normalize({ ...data("hwid-track"), devices: [{ id: "dev_tg", kind: "telegram", platform: "", model: "Telegram proxy", app: "telegram" }] }).devices[0]!.kind).toBe("telegram");
  });

  it("the TrustTunnel key has a name of its own and nothing to remove or rename", () => {
    const el = page(data("trusttunnel"), { returning: true, tab: "devices" }, "en");
    const r = byName(el, "TrustTunnel key");
    expect(r).toBeTruthy();
    expect(r.querySelector("button")).toBeNull();
    expect(when(r)).toContain("connected");
    expect(r.querySelector(".tl svg")).not.toBeNull();
  });

  it("a subscription that is not active shows the TrustTunnel key among the devices, with no tabs", () => {
    const el = view(data("trusttunnel-disabled"), state("ios", {}, "en"), actions());
    expect(el.querySelector(".u-tabs")).toBeNull();
    expect(names(el)).toContain("TrustTunnel key");
  });
});

describe("the keys of the AmneziaVPN app", () => {
  it("each key: the name, what kind of key it is, when it last connected; no profile or address", () => {
    const el = page(data("many"));
    expect(names(el)).toEqual(["Приложения без ID устройства", "Pixel 7", "MacBook", "Windows"]);
    expect(meta(row(el, "d3"))).toBe("Ключ AmneziaVPN · Германия, Финляндия · AWG 3.1");
    expect(when(row(el, "d3"))).toBe("в сети");
    expect(when(row(el, "d4"))).toBe("подключалось 12 дней назад");
    expect(when(row(el, "d5"))).toBe("ещё не подключалось");
    expect(el.textContent).not.toMatch(/Main|10\.66|fd66/);
    expect(meta(row(page(data("many"), {}, "en"), "d3"))).toBe("AmneziaVPN key · Germany, Финляндия · AWG 3.1".replace("Финляндия", "Finland"));
  });

  it("a key of a router has the board's router tile", () => {
    const el = page(data("devices-rich"), { devicesOpen: true });
    expect(names(el)).toContain("Router");
    expect(row(el, "d6").querySelector(".tl svg path")?.getAttribute("d")).toContain("M5 13h14");
  });

  it("'more' opens the window of the key, when the page may issue keys (self-service)", () => {
    const a = actions();
    const log: string[] = [];
    a.amz = { ...a.amz, menu: (id) => void log.push(`menu ${id}`) };
    const el = page(data("many"), {}, "ru", a);
    el.querySelector<HTMLButtonElement>("[data-k=amz-more-d3]")!.click();
    expect(log).toEqual(["menu d3"]);
    expect(row(el, "d3").querySelector("[data-k=amz-more-d3]")?.getAttribute("aria-label")).toBe("Ещё: Pixel 7");
    // the owner issues the keys, or the page is the admin's preview: rows without buttons
    expect(page(data("amnezia-off")).querySelector("[data-k^=amz-more-d]")).toBeNull();
    expect(page(data("many", (d) => (d.amnezia!.endpoints = ""))).querySelector("[data-k^=amz-more-d]")).toBeNull();
  });

  it("a stale key says so, with a button for the new key (the page's own window asks first)", () => {
    const a = actions({ amz: { ...amzNoop, stale: (id) => void a.log.push(`stale ${id}`) } });
    const el = page(data("stale"), {}, "ru", a);
    const r = row(el, "d3");
    expect(r.classList.contains("stale")).toBe(true);
    expect(text(r.querySelector(".u-tag.work"))).toBe("нужен новый ключ");
    const btn = r.querySelector<HTMLButtonElement>("[data-k=amz-stale-d3]")!;
    expect([text(btn), btn.classList.contains("pri")]).toEqual(["Получить новый ключ", true]);
    btn.click();
    expect(a.log).toEqual(["stale d3"]);
    expect(el.querySelector(".stale-card, .stale .h3")).toBeNull(); // the card that listed the keys is gone
    // two stale keys: a button each, with its own key for the keyboard
    const two = data("many");
    two.amnezia!.devices.forEach((x) => (x.stale = true));
    expect([...page(two).querySelectorAll("[data-k^=amz-stale-]")].map((b) => b.getAttribute("data-k"))).toEqual(["amz-stale-d3", "amz-stale-d4", "amz-stale-d5"]);
    expect(text(page(data("stale"), {}, "en").querySelector(".u-tag.work"))).toBe("new key needed");
  });

  it("a stale key without self-service has the mark and says why there is no button; one that is not active has neither", () => {
    const el = page(data("stale", (d) => (d.amnezia!.self_service = false)));
    expect(el.querySelector("[data-k^=amz-stale-]")).toBeNull();
    expect(plain(row(el, "d3").querySelector(".u-tag.work")?.getAttribute("title"))).toBe("Ключи выдаёт администратор — напишите, если нужен новый.");
    const off = view(data("stale", (x) => (x.user.status = "expired")), state("ios", { tab: "devices" }), actions());
    expect(off.querySelector(".u-tag.work, [data-k^=amz-stale-]")).toBeNull();
  });

  it("a busy row: a spinner on the button that was pressed, no second press; the other rows stay", () => {
    const am = { ...newAmzState("ios", "p31", "ios"), busy: "d3" };
    const busy = row(page(data("many"), { amz: am }), "d3");
    const more = busy.querySelector<HTMLButtonElement>("[data-k=amz-more-d3]")!;
    expect([more.disabled, more.getAttribute("aria-busy"), more.querySelector(".spin") !== null]).toEqual([true, "true", true]);
    const stale = row(page(data("stale"), { amz: { ...am, busy: "renew:d3" } }), "d3");
    expect(text(stale.querySelector("[data-k=amz-stale-d3]"))).toBe("Секунду…");
    expect(stale.querySelector<HTMLButtonElement>("[data-k=amz-stale-d3]")?.disabled).toBe(true);
    expect(row(page(data("many"), { amz: am }), "d4").querySelector<HTMLButtonElement>("[data-k=amz-more-d4]")?.disabled).toBe(false);
  });
});

describe("remove and rename call the link's own address", () => {
  it("remove: POST <devices_endpoint>/<id>/revoke, then the row and its place are gone", async () => {
    const { d, st, a, api } = rig("hwid-full");
    a.ask({ id: "l2", kind: "remove" });
    a.remove("l2");
    await settle();
    expect(api.calls).toEqual(["revoke https://sub.example.com/s/tok/devices l2"]);
    expect(d.devices.map((x) => x.id)).not.toContain("l2");
    expect(d.user.devices_used).toBe(3);
    expect(st.amz.confirm).toBeNull();
  });

  it("rename: POST <devices_endpoint>/<id>/rename with the label; the row takes the name the server answered", async () => {
    const { d, st, a, api } = rig("hwid-full");
    a.renameStart("l1");
    expect(st.amz.rename).toEqual({ id: "l1", value: "iPhone 15" });
    a.renameInput("  Телефон в прихожей  ");
    a.renameSave();
    await settle();
    expect(api.calls).toEqual(["rename https://sub.example.com/s/tok/devices l1 Телефон в прихожей"]);
    expect(d.devices.find((x) => x.id === "l1")?.model).toBe("Телефон в прихожей");
    expect(st.amz.rename).toBeNull();
  });

  it("a key still goes to the keys' address; the shared place cannot be renamed; without an address nothing is sent", async () => {
    const { a, api } = rig("hwid-dense");
    a.remove("d6");
    await settle();
    expect(api.calls).toEqual(["revoke https://sub.example.com/s/tok/keys d6"]);
    const shared = rig("hwid-full");
    shared.a.renameStart("d1");
    expect(shared.st.amz.rename).toBeNull();
    const off = rig("hwid-full");
    off.d.devices_endpoint = "";
    off.a.remove("l2");
    await settle();
    expect(off.api.calls).toEqual([]);
  });
});

describe("the errors, in words", () => {
  it("the hourly budget of changes, with the minutes, under the row; the shared place; a device that is gone", async () => {
    const { st, a } = rig("hwid-full", fakeApi({ revokeDevice: async () => Promise.reject(new ApiError(429, "too_many_requests", 1800)) }));
    a.remove("l2");
    await settle();
    expect(st.amz).toMatchObject({ error: "too_many_requests", errorAt: "l2", retryMin: 30 });
    const el = view(data("hwid-full"), { ...state("ios", { returning: true, tab: "devices" }), amz: st.amz }, actions());
    const err = row(el, "l2").nextElementSibling!;
    expect(err.classList.contains("a-err")).toBe(true);
    expect(text(err)).toBe("Слишком много действий подряд. Попробуйте через 30 мин.");
    expect(err.getAttribute("role")).toBe("alert");
    expect(row(el, "l1").nextElementSibling?.classList.contains("a-err")).toBe(false);
    expect(plain(dict.ru.err("shared_device", 0))).toBe("Это место общее для приложений без ID устройства — его нельзя удалить или переименовать.");
    expect(plain(dict.en.err("shared_device", 0))).toBe("This slot is shared by the apps without a device ID — it can’t be removed or renamed.");
    expect(dict.zh.err("shared_device", 0)).toBe("这是不提供设备 ID 的应用共用的名额，无法删除或重命名。");
    expect(dict.en.err("too_many_requests", 2)).toBe("Too many actions in a row. Try again in 2 min.");
    expect(dict.ru.err("not_found", 0)).toBe("Такого устройства уже нет. Обновите страницу.");
  });

  it("a failed rename keeps its error where it happened; the row of that device shows it", async () => {
    const { st, a, d } = rig("hwid-full", fakeApi({ renameDevice: async () => Promise.reject(new ApiError(409, "shared_device")) }));
    a.renameStart("l1");
    a.renameInput("Другое имя");
    a.renameSave();
    await settle();
    expect(st.amz).toMatchObject({ error: "shared_device", errorAt: "rename:l1", busy: "" });
    expect(st.amz.rename).not.toBeNull();
    const el = view(d, { ...state("ios", { returning: true, tab: "devices" }), amz: st.amz }, actions());
    expect(row(el, "l1").nextElementSibling?.classList.contains("a-err")).toBe(true);
  });
});

describe("a long list folds", () => {
  const fold = (el: HTMLElement) => el.querySelector<HTMLButtonElement>("[data-k=dev-fold]");

  it("more than three devices: three rows and 'show N more', which unfolds and folds again", () => {
    const a = actions();
    const el = page(data("hwid-long"), { devicesOpen: false }, "ru", a);
    expect(names(el)).toEqual(["iPhone 15", "Pixel 8", "DESKTOP-4F7K2LM"]);
    expect(text(fold(el))).toBe("Показать ещё 3");
    expect(fold(el)!.getAttribute("aria-expanded")).toBe("false");
    expect(fold(el)!.closest(".u-more")).not.toBeNull();
    fold(el)!.click();
    expect(a.log).toEqual(["devices-open true"]);
    const open = page(data("hwid-long"));
    expect(names(open)).toEqual(["iPhone 15", "Pixel 8", "DESKTOP-4F7K2LM", "iPad", "Приложения без ID устройства", "Router"]);
    expect(text(fold(open))).toBe("Свернуть");
    expect(fold(open)!.getAttribute("aria-expanded")).toBe("true");
    expect(text(fold(page(data("hwid-long"), { devicesOpen: false }, "en")))).toBe("Show 3 more");
    expect(text(fold(page(data("hwid-long"), { devicesOpen: false }, "zh")))).toBe("再显示 3 台");
  });

  it("the board folds at four rows too, and when every place is taken; three rows are all shown", () => {
    const dense = page(data("hwid-dense"), { devicesOpen: false });
    expect(names(dense)).toEqual(["iPhone 15", "Pixel 8", "DESKTOP-4F7K2LM"]);
    expect(text(fold(dense))).toBe("Показать ещё 2");
    expect(fold(page(data("hwid-dense"), { devicesOpen: true }))).not.toBeNull();
    expect(fold(page(data("hwid-track", (d) => (d.devices = d.devices.slice(0, 3))), { devicesOpen: false }))).toBeNull();
  });
});

describe("a subscription that is not active", () => {
  it("keeps the devices, with no tabs: the places card says why nothing can be added, the rows can still be changed", () => {
    for (const name of ["expired", "quota", "disabled"]) {
      const el = view(data(name), state("android", { returning: true }), actions());
      expect(el.querySelector(".u-tabs")).toBeNull();
      const add = el.querySelector<HTMLButtonElement>("[data-k=dev-add]")!;
      expect(add.disabled).toBe(true);
      expect(add.getAttribute("aria-describedby")).toBe("why-off");
      expect(text(el.querySelector("#why-off"))).toBe(name === "disabled" ? "Подписка отключена, ключи не выдаются. Удалить или переименовать устройство можно." : "Подписка сейчас не активна — ключи не выдаются. Удалить или переименовать устройство можно.");
      expect(el.querySelector("[data-k=amz-more-d3]")?.getAttribute("aria-label")).toBe("Ещё: Pixel 7");
      expect(el.querySelector("[data-k^=dev-link-copy]")).toBeNull();
      expect(el.querySelector(".u-tbl")).toBeNull();
    }
  });

  it("with no device at all there is nothing to show", () => {
    const el = view(data("disabled", (d) => ((d.devices = []), (d.amnezia!.devices = []))), state("android"), actions());
    expect(el.querySelector(".u-dvh, .u-dvl")).toBeNull();
  });
});

describe("the page as a whole", () => {
  it("every focus key is its own, on every tab, so the page can put the focus back after it is drawn", () => {
    for (const name of ["hwid-long", "many", "telegram", "trusttunnel", "stale", "dns-both"]) {
      for (const tab of ["servers", "telegram", "trusttunnel", "devices"] as const) {
        const el = view(data(name), state("windows", { tab, returning: true, devicesOpen: true }), actions());
        const keys = [...el.querySelectorAll("[data-k]")].map((x) => x.getAttribute("data-k")!).filter((k) => !k.startsWith("hero-"));
        expect(keys.filter((k, i) => keys.indexOf(k) !== i), `${name} ${tab}`).toEqual([]);
      }
    }
  });

  it("the words of the devices are in all five languages", () => {
    for (const lang of langs) {
      const w = dict[lang];
      for (const k of ["dvSlotsAria", "dvUnl", "linkShort", "devNone", "srvNone", "thServer", "thRoute", "thDns"] as const) expect(w[k], `${lang} ${k}`).toBeTruthy();
      expect(w.dvTail(true, "x")).toContain("x");
      expect(w.dvTail(false, "x")).toContain("x");
      expect(w.dvMoreAria("Pixel")).toContain("Pixel");
      expect(w.keyMeta("AmneziaVPN", "AWG 3.1")).toContain("AWG 3.1");
    }
    expect(dict.ru.devNone).toBe("Устройств пока нет. Добавьте первое, и оно появится здесь");
  });
});
