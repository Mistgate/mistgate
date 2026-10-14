import { describe, expect, it } from "vitest";
import { amzActions, type Api } from "./amz-actions";
import { ApiError } from "./api";
import { dict } from "./i18n";
import { awgDevice, deviceAbout, fetchedUnix, isReturning, normalize } from "./logic";
import { actions, data, plain, state, text } from "./test-kit";
import type { Device, MgData } from "./types";
import { view } from "./view";

// The devices of the apps on the link (step 6 of the device limit): each app that reports a device ID is a device of its own,
// with rename and remove in its "more" menu; the shared place of the apps that report none; the hint when every place is taken.

const settle = () => new Promise((r) => setTimeout(r, 0));
const page = (d: MgData, over: Parameters<typeof state>[1] = {}, lang: "ru" | "en" | "zh" = "ru", a = actions()) => view(d, state("ios", { returning: true, ...over }, lang), a);
/** The card of "My devices" (the page has other cards above it). */
const card = (el: HTMLElement) => [...el.querySelectorAll<HTMLElement>("section.card")].find((x) => x.querySelector(".dev"))!;
const names = (el: HTMLElement) => [...card(el).querySelectorAll(".dev .dev-n")].map(text);
const row = (el: HTMLElement, id: string) => el.querySelector<HTMLElement>(`#amz-row-${id}`)!;
const byName = (el: HTMLElement, name: string) => [...el.querySelectorAll<HTMLElement>(".dev")].find((x) => text(x.querySelector(".dev-n")) === name)!;
/** The line under a name, part by part. */
const line = (r: HTMLElement) => [...r.querySelector(".dev-m")!.children].map(text).filter((x) => x !== "·" && x !== "").join(" · ");

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

describe("the rows of the link devices", () => {
  it("one row per device, the one online first, then the shared place; a name and one quiet line, no versions in the list", () => {
    const el = page(data("hwid-track"));
    expect(names(el)).toEqual(["iPhone 15", "Pixel 8", "DESKTOP-4F7K2LM", "Приложения без ID устройства"]);
    expect(line(row(el, "l1"))).toBe("в сети");
    expect(line(row(el, "l2"))).toBe("обновлялось позавчера");
    expect(row(el, "l1").querySelectorAll(".dev-m:not(.dev-det)")).toHaveLength(1);
    // what the apps said is in the details (hidden until asked) and the tooltip, never in the list itself
    const seen = card(el).cloneNode(true) as HTMLElement;
    seen.querySelectorAll("[hidden], [role=tooltip]").forEach((x) => x.remove());
    expect(seen.textContent).not.toMatch(/18\.1|Happ|mihomo|HWID/);
    // the one online sorts first whatever the server's order
    expect(names(page(data("hwid-track", (d) => d.devices.reverse())))[0]).toBe("iPhone 15");
  });

  it("rename and remove sit in a 'more' menu, never as buttons on the row; the shared place has none", () => {
    const el = page(data("hwid-track"));
    const more = row(el, "l2").querySelector<HTMLButtonElement>("[data-k=amz-more-l2]")!;
    expect(more.getAttribute("aria-haspopup")).toBe("menu");
    expect(more.getAttribute("aria-expanded")).toBe("false");
    expect(more.getAttribute("aria-label")).toBe("Ещё: переименовать, удалить");
    expect(more.getAttribute("aria-describedby")).toBe("dev-n-l2"); // the name of the device it belongs to
    expect([...row(el, "l2").querySelectorAll("button")].map((b) => b.getAttribute("data-k"))).toEqual(["dev-body-l2", "amz-more-l2"]); // the row's own details, and "more"
    expect(byName(el, "Приложения без ID устройства").querySelector("[data-k^=amz-more-]")).toBeNull();
  });

  it("the menu: rename, then a red remove; they start the rename and the question", () => {
    const a = actions();
    const log: string[] = [];
    a.amz = { ...a.amz, renameStart: (id) => log.push(`rename ${id}`), ask: (c) => log.push(`ask ${c?.kind} ${c?.id}`) };
    const el = page(data("hwid-track"), { amz: { ...state().amz, menu: "l2" } }, "ru", a);
    const menu = row(el, "l2").querySelector("[role=menu]")!;
    expect([...menu.querySelectorAll("[role=menuitem]")].map(text)).toEqual(["Переименовать", "Удалить"]);
    expect(menu.querySelector("[data-k=amz-m-del-l2]")?.classList.contains("bad")).toBe(true);
    menu.querySelector<HTMLButtonElement>("[data-k=amz-m-ren-l2]")!.click();
    menu.querySelector<HTMLButtonElement>("[data-k=amz-m-del-l2]")!.click();
    expect(log).toEqual(["rename l2", "ask remove l2"]);
  });

  it("nothing to press where the person may not change the device (the admin's preview, self-service off)", () => {
    const el = page(data("hwid-track", (d) => d.devices.forEach((x) => ((x.can_remove = false), (x.can_rename = false)))));
    expect(el.querySelector("[data-k^=amz-more-]")).toBeNull();
    expect(names(el)).toHaveLength(4);
  });

  it("the question says which device it is (what the app said) and what removing does; the safe answer first", () => {
    const el = page(data("hwid-track"), { amz: { ...state().amz, confirm: { id: "l2", kind: "remove" } } });
    const q = row(el, "l2");
    expect(q.getAttribute("role")).toBe("alertdialog");
    expect(text(q.querySelector(".dev-n"))).toBe("Удалить «Pixel 8»?");
    expect(text(q.querySelector(".dev-about"))).toBe("Android 15 · Happ 3.4.1");
    expect(text(q.querySelector(`#${q.getAttribute("aria-describedby")}`))).toBe("Android 15 · Happ 3.4.1Приложение на этом устройстве перестанет подключаться. Место освободится.");
    expect([...q.querySelectorAll("button")].map((b) => b.getAttribute("data-k"))).toEqual(["amz-no-l2", "amz-yes-l2"]);
  });

  it("rename is edited in the row, like a key's", () => {
    const el = page(data("hwid-track"), { amz: { ...state().amz, rename: { id: "l1", value: "iPhone 15" } } });
    const f = row(el, "l1");
    expect(f.tagName).toBe("FORM");
    expect(f.querySelector<HTMLInputElement>("#ren-l1")?.value).toBe("iPhone 15");
    expect(text(f.querySelector("label"))).toBe("Название устройства");
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
  it("the hourly budget of changes, with the minutes; the shared place; a device that is gone", async () => {
    const { st, a } = rig("hwid-full", fakeApi({ revokeDevice: async () => Promise.reject(new ApiError(429, "too_many_requests", 1800)) }));
    a.remove("l2");
    await settle();
    expect(st.amz).toMatchObject({ error: "too_many_requests", errorAt: "l2", retryMin: 30 });
    const el = view(data("hwid-full"), { ...state("ios", { returning: true }), amz: st.amz }, actions());
    expect(text(row(el, "l2").querySelector(".note.bad"))).toBe("Слишком много действий подряд. Попробуйте через 30 мин.");
    expect(row(el, "l2").querySelector(".note.bad")?.getAttribute("role")).toBe("alert");
    expect(plain(dict.ru.err("shared_device", 0))).toBe("Это место общее для приложений без ID устройства — его нельзя удалить или переименовать.");
    expect(plain(dict.en.err("shared_device", 0))).toBe("This slot is shared by the apps without a device ID — it can’t be removed or renamed.");
    expect(dict.zh.err("shared_device", 0)).toBe("这是不提供设备 ID 的应用共用的名额，无法删除或重命名。");
    expect(dict.en.err("too_many_requests", 2)).toBe("Too many actions in a row. Try again in 2 min.");
    expect(dict.ru.err("not_found", 0)).toBe("Такого устройства уже нет. Обновите страницу.");
  });

  it("a failed rename keeps the editor open with the error under it", async () => {
    const { st, a } = rig("hwid-full", fakeApi({ renameDevice: async () => Promise.reject(new ApiError(409, "shared_device")) }));
    a.renameStart("l1");
    a.renameInput("Другое имя");
    a.renameSave();
    await settle();
    expect(st.amz).toMatchObject({ error: "shared_device", errorAt: "rename:l1", busy: "" });
    expect(st.amz.rename).not.toBeNull();
  });
});

describe("the shared place, by the mode", () => {
  const shared = (el: HTMLElement) => byName(el, "Приложения без ID устройства");

  it("TRACK and ENFORCE: one short line, they share one place, and when they last updated", () => {
    for (const name of ["hwid-track", "hwid-full"]) expect(line(shared(page(data(name))))).toBe("одно место на всех · 9 дней назад");
    expect(line(shared(page(data("hwid-full", (d) => (d.devices.find((x) => x.kind === "shared")!.online = true)))))).toBe("одно место на всех · в сети");
    expect(line(byName(page(data("hwid-track"), {}, "en"), "Apps without a device ID"))).toBe("one slot for all · 9 days ago");
    expect(text(byName(page(data("hwid-track"), {}, "zh"), "不提供设备 ID 的应用").querySelector(".dev-m"))).toBe("共用一个名额·9天前"); // the time is Intl's
  });

  it("STRICT: a quiet row that is no device says the apps without a device ID do not connect, and which to use", () => {
    const el = page(data("hwid-strict"));
    const r = shared(el);
    expect(line(r)).toBe("не подключатся — нужен Happ или kl!ck");
    expect(r.querySelector(".tile.dash")).not.toBeNull();
    expect(r.querySelector("button")).toBeNull();
    expect(names(el).at(-1)).toBe("Приложения без ID устройства");
    // a shared place left over says the same
    expect(line(shared(page(data("hwid-full", (d) => (d.link_device_limit = "strict")))))).toBe("не подключатся — нужен Happ или kl!ck");
    // with no app at all, STRICT says it instead of "not connected yet"
    expect(names(page(data("hwid-strict", (d) => (d.devices = []))))).toEqual(["Приложения без ID устройства"]);
  });

  it("before any app took the link: the link's own row (the old one); the link to copy is next to 'Add a device'", () => {
    const el = page(data("hwid-track", (d) => (d.devices = [])));
    expect(names(el)).toEqual(["Приложения по ссылке"]);
    expect(line(byName(el, "Приложения по ссылке"))).toBe("ещё не подключались");
    expect(byName(el, "Приложения по ссылке").querySelector("button")).toBeNull();
    expect(el.querySelector("[data-k=dev-link-copy]")?.closest(".dev")?.querySelector("[data-k=dev-add]")).not.toBeNull();
  });
});

describe("every place taken", () => {
  const hint = (el: HTMLElement) => card(el).previousElementSibling!.querySelector(".sec-sub");

  it("ENFORCE: a calm line under the title, two sentences; the card points to it; 'all slots are used' under 'Add' is not said twice", () => {
    const el = page(data("hwid-full"));
    expect(text(hint(el))).toBe("Все места заняты. Удалите ненужное устройство и обновите подписку в приложении.");
    expect(hint(el)?.getAttribute("role")).toBeNull(); // not an alert: nothing went wrong
    expect(card(el).getAttribute("aria-describedby")).toBe(hint(el)?.id);
    expect(el.querySelector(".slots.full")).not.toBeNull();
    expect(el.querySelector(".dev-hint")).toBeNull();
    expect(text(hint(page(data("hwid-full"), {}, "en")))).toBe("All slots are used. Remove an old device, then refresh the subscription in the app.");
    expect(text(hint(page(data("hwid-full"), {}, "zh")))).toBe("名额已用完。请删除不用的设备，然后在应用中更新订阅。");
  });

  it("not in TRACK (nobody is refused), not with room left, not without the link apps", () => {
    const has = (d: MgData) => page(d).querySelector("#dev-full") !== null;
    expect(has(data("hwid-full", (d) => (d.link_device_limit = "track")))).toBe(false);
    expect(has(data("hwid-full", (d) => (d.user.device_limit = 5)))).toBe(false);
    expect(has(data("hwid-full", (d) => (d.access.happ = false)))).toBe(false);
    expect(has(data("hwid-strict", (d) => ((d.user.device_limit = 3), (d.user.devices_used = 3))))).toBe(true);
  });
});

describe("a long list folds", () => {
  it("more than four devices with room left: three rows and 'show N more', which unfolds and folds again", () => {
    const a = actions();
    const el = page(data("hwid-long"), {}, "ru", a);
    expect(names(el)).toEqual(["iPhone 15", "Pixel 8", "DESKTOP-4F7K2LM"]);
    const fold = el.querySelector<HTMLButtonElement>("[data-k=dev-fold]")!;
    expect(text(fold)).toBe("Показать ещё 3");
    expect(fold.getAttribute("aria-expanded")).toBe("false");
    fold.click();
    expect(a.log).toEqual(["devices-open true"]);
    const open = page(data("hwid-long"), { devicesOpen: true });
    expect(names(open)).toEqual(["iPhone 15", "Pixel 8", "DESKTOP-4F7K2LM", "iPad", "Приложения без ID устройства", "Router"]);
    expect(text(open.querySelector("[data-k=dev-fold]"))).toBe("Свернуть");
    expect(open.querySelector("[data-k=dev-fold]")?.getAttribute("aria-expanded")).toBe("true");
    expect(text(page(data("hwid-long"), {}, "en").querySelector("[data-k=dev-fold]"))).toBe("Show 3 more");
    expect(text(page(data("hwid-long"), {}, "zh").querySelector("[data-k=dev-fold]"))).toBe("再显示 3 台");
  });

  it("never when every place is taken (that is when an old device is looked for), nor at four rows", () => {
    const dense = page(data("hwid-dense"));
    expect(names(dense)).toEqual(["iPhone 15", "Pixel 8", "DESKTOP-4F7K2LM", "Приложения без ID устройства", "Router"]);
    expect(dense.querySelector("[data-k=dev-fold]")).toBeNull();
    expect(page(data("hwid-track")).querySelector("[data-k=dev-fold]")).toBeNull();
  });
});

describe("the details of a row: one tap, or a hover", () => {
  it("the row is a disclosure: a button with aria-expanded and aria-controls, the details hidden, a tooltip describing it", () => {
    const a = actions();
    const el = page(data("hwid-full"), {}, "ru", a);
    const btn = row(el, "l1").querySelector<HTMLButtonElement>("button.dev-body")!;
    expect(btn.getAttribute("data-k")).toBe("dev-body-l1");
    expect(btn.getAttribute("aria-expanded")).toBe("false");
    const det = el.querySelector<HTMLElement>(`#${btn.getAttribute("aria-controls")}`)!;
    expect(det.hidden).toBe(true);
    expect(text(det)).toBe("iOS 18.1 · Happ 3.4.2");
    const tip = el.querySelector(`#${btn.getAttribute("aria-describedby")}`)!;
    expect(tip.getAttribute("role")).toBe("tooltip");
    expect(text(tip)).toBe("iOS 18.1 · Happ 3.4.2");
    expect(btn.nextElementSibling).toBe(tip); // the CSS shows it for the button's hover and focus
    expect(btn.querySelector(".dev-chev")?.getAttribute("aria-hidden")).toBe("true");
    expect(text(btn.querySelector(".dev-n"))).toBe("iPhone 15");
    btn.click();
    expect(a.log).toEqual(["details l1"]);
  });

  it("open: the second line shows in place, the chevron turns, the tooltip is gone; a tap closes it; one row at a time", () => {
    const a = actions();
    const el = page(data("hwid-full"), { details: "l2" }, "ru", a);
    const btn = row(el, "l2").querySelector<HTMLButtonElement>("button.dev-body")!;
    expect(btn.getAttribute("aria-expanded")).toBe("true");
    expect(btn.hasAttribute("aria-describedby")).toBe(false);
    expect(row(el, "l2").querySelector("[role=tooltip]")).toBeNull();
    const det = row(el, "l2").querySelector<HTMLElement>(".dev-det")!;
    expect(det.hidden).toBe(false);
    expect(text(det)).toBe("Android 15 · Happ 3.4.1");
    expect(row(el, "l1").querySelector<HTMLElement>(".dev-det")!.hidden).toBe(true);
    btn.click();
    expect(a.log).toEqual(["details null"]);
  });

  it("only rows with something to show: a device that said nothing, the shared place without a last app, a key, STRICT's row", () => {
    const quiet = page(data("hwid-full", (d) => d.devices.forEach((x) => ((x.os_version = ""), (x.client = "")))));
    expect(quiet.querySelector(".dev-body, .dev-chev, [role=tooltip]")).toBeNull();
    expect(names(quiet)).toEqual(["iPhone 15", "Pixel 8", "DESKTOP-4F7K2LM", "Приложения без ID устройства"]);
    expect(page(data("hwid-dense")).querySelector("#amz-row-d6 .dev-body")).toBeNull();
    expect(page(data("hwid-strict")).querySelectorAll(".dev-body")).toHaveLength(3);
  });

  it("the shared place opens to the last app that used it", () => {
    const el = page(data("hwid-full"), { details: "d1" });
    const r = byName(el, "Приложения без ID устройства");
    expect(text(r.querySelector(".dev-det"))).toBe("последнее приложение: HiddifyNext 2.5.7");
    expect(line(r)).toBe("одно место на всех · 9 дней назад");
    expect(text(byName(page(data("hwid-full"), { details: "d1" }, "en"), "Apps without a device ID").querySelector(".dev-det"))).toBe("last app: HiddifyNext 2.5.7");
    expect(text(byName(page(data("hwid-full"), { details: "d1" }, "zh"), "不提供设备 ID 的应用").querySelector(".dev-det"))).toBe("最近的应用：HiddifyNext 2.5.7");
  });
});
