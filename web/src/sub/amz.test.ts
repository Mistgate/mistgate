import { describe, expect, it } from "vitest";
import { addModal, defaultWhere, keyView, newAmzState, staleDevices, whereOptions, type AmzState } from "./amnezia";
import { amzActions, type Api } from "./amz-actions";
import { ApiError, endpoint } from "./api";
import { cases } from "./dev-data";
import { dict } from "./i18n";
import { awgConfig, awgDevice, canAddDevice, defaultLabel, mainProfile, nodeLabels, normalize, otherDevices, profileChoices, selfServe, versionsFor } from "./logic";
import { actions, amzNoop, data, plain, state, text } from "./test-kit";
import type { MgData } from "./types";
import { view } from "./view";

const t = dict.ru;
const cfg = (node: string, cc = "DE", label = ({ DE: "Германия · Франкфурт", FI: "Финляндия" } as Record<string, string>)[cc] ?? cc) =>
  awgConfig({ node_id: node, label, country_code: cc, version: "3.1", conf: "[Interface]\nPrivateKey = x\n", vpn_key: "vpn://k", filename: `mistgate-${cc.toLowerCase()}.conf`, warnings: ["amnezia_desktop_mtu"] });

function fakeApi(over: Partial<Api> = {}): Api & { calls: string[] } {
  const calls: string[] = [];
  return {
    calls,
    addDevice: async (_e, b) => {
      calls.push(`add ${b.profile_id} ${b.platform} ${b.label}`);
      return { device: awgDevice({ id: "dev_new", platform: b.platform, label: b.label, profile_id: b.profile_id, profile_name: "Main", version: "3.1" }), configs: [cfg("de1")] };
    },
    getConfigs: async (_e, id) => {
      calls.push(`configs ${id}`);
      return { configs: [cfg("de1"), cfg("fi1", "FI")] };
    },
    rotateKey: async (_e, id) => {
      calls.push(`rotate ${id}`);
      return { configs: [cfg("de1")] };
    },
    revokeDevice: async (_e, id) => {
      calls.push(`revoke ${id}`);
    },
    renameDevice: async (_e, id, label) => {
      calls.push(`rename ${id} ${label}`);
      return { device: awgDevice({ id, label, platform: "android", profile_id: "p31" }), configs: [] };
    },
    ...over,
  };
}

function setup(name: string, api: Api, f: (d: MgData) => void = () => {}) {
  const d = data(name, f);
  const st = { lang: "ru" as const, amz: newAmzState("android", mainProfile(d.amnezia?.profiles ?? []), "android") };
  let draws = 0;
  const log: string[] = [];
  const marked: string[] = [];
  const a = amzActions({ data: d, st, render: () => draws++, api, reveal: (id) => log.push(`reveal ${id}`), focus: (k) => log.push(`focus ${k}`), marked: () => marked.push("marked") });
  return { data: d, st, a, log, marked, draws: () => draws };
}
const settle = () => new Promise((r) => setTimeout(r, 0));
/** What the line under a device name says, part by part: "Android · в сети". */
const meta = (r: HTMLElement) => [...r.querySelector(".dev-m")!.children].map(text).filter((x) => x !== "·").join(" · ");
const row = (el: HTMLElement, id: string) => el.querySelector<HTMLElement>(`#amz-row-${id}`)!;

describe("page data", () => {
  it("reads the Amnezia section and drops what is not there", () => {
    const d = normalize(cases.many);
    expect(d.amnezia?.devices).toHaveLength(3);
    expect(d.amnezia?.profiles.map((p) => p.id)).toEqual(["p20", "pw", "p31"]);
    expect(d.amnezia?.endpoints).toBe("dev:");
    expect(normalize({ amnezia: { devices: [{ id: "" }, null, 5] } }).amnezia?.devices).toEqual([]);
    expect(normalize(cases.happ).amnezia).toBeNull();
  });
  it("lists key devices once: not among the link's", () => {
    expect(otherDevices(normalize(cases.many)).map((x) => x.id)).toEqual(["d1"]);
  });
  it("offers adding only with self-service, an address to call, a profile and a free slot", () => {
    expect(canAddDevice(normalize(cases.many))).toBe(true);
    expect(canAddDevice(normalize(cases["amnezia-off"]))).toBe(false);
    expect(canAddDevice(normalize(cases["amnezia-none"]))).toBe(false);
    expect(canAddDevice(normalize(cases.devices))).toBe(false); // limit reached
    expect(canAddDevice(normalize(cases.happ))).toBe(false);
    // the admin's preview: self-service on, no address: nothing to press
    const preview = normalize({ ...cases.many, amnezia: { ...cases.many!.amnezia, endpoints: "" } });
    expect([selfServe(preview), canAddDevice(preview)]).toEqual([false, false]);
  });
  it("a subscription that is not active still has its devices (no profiles, nothing to add)", () => {
    const d = normalize(cases.expired);
    expect(d.amnezia?.devices.map((x) => x.label)).toEqual(["Pixel 7"]);
    expect([d.amnezia?.can_add, d.amnezia?.profiles]).toEqual([false, []]);
    expect(selfServe(d)).toBe(true); // rename and remove are allowed
  });
});

describe("endpoint", () => {
  it("posts to the page's own origin, whatever host the data names", () => {
    const here = { origin: "https://mirror.example.net", href: "https://mirror.example.net/p/t" } as Location;
    expect(endpoint("https://sub.example.com/p/t/devices", "/dev_1/configs", here)).toBe("https://mirror.example.net/p/t/devices/dev_1/configs");
    expect(endpoint("https://sub.example.com/p/t/dns", "", here)).toBe("https://mirror.example.net/p/t/dns");
    expect(endpoint("/p/t/devices", "", here)).toBe("https://mirror.example.net/p/t/devices");
  });
});

describe("names and choices a friend can read", () => {
  it("a device left without a name is called by its platform, numbered when taken, never 'ios 1'", () => {
    expect(defaultLabel("ios", [], t)).toBe("iPhone");
    expect(defaultLabel("ios", ["iPhone", "iphone 2"], t)).toBe("iPhone 3");
    expect(defaultLabel("macos", ["Pixel"], t)).toBe("Mac");
    expect(defaultLabel("other", ["Устройство"], t)).toBe("Устройство 2");
    expect(defaultLabel("other", [], dict.en)).toBe("Device");
  });

  it("the connection options: the main one first and by its countries, the WARP one as the spare exit, 2.0 for old apps", () => {
    const d = normalize(cases.many);
    const choices = profileChoices(d.amnezia!.profiles, t, "ru", "AmneziaVPN");
    expect(choices.map((c) => [c.id, c.label, c.kind])).toEqual([
      ["p31", "🇩🇪 Германия, 🇫🇮 Финляндия", "main"],
      ["pw", "WARP (выход через Cloudflare)", "warp"],
      ["p20", "Для старых версий AmneziaVPN (до 5.0.1.5)", "old"],
    ]);
    expect(mainProfile(d.amnezia!.profiles)).toBe("p31");
    // the same label twice gets a number; without a country the main one is "Main"; without flags (Windows) the names only
    const two = [
      { id: "a", name: "x", version: "3.1", egress: "direct" as const, countries: [] },
      { id: "b", name: "y", version: "3.1", egress: "direct" as const, countries: [] },
    ];
    expect(profileChoices(two, t, "ru", "A").map((c) => c.label)).toEqual(["Основной", "Основной (2)"]);
    expect(profileChoices(d.amnezia!.profiles, t, "ru", "AmneziaVPN", false)[0]!.label).toBe("Германия, Финляндия");
  });

  it("a key's node choice is the server's public name with the flag, never the panel's node name", () => {
    expect(nodeLabels([cfg("de1"), cfg("fi1", "FI", "Финляндия")])).toEqual(["🇩🇪 Германия · Франкфурт", "🇫🇮 Финляндия"]);
    const repeated = [cfg("de1", "DE", "Germany"), cfg("de2", "DE", "Germany 2"), cfg("x", "", "Server")];
    expect(nodeLabels(repeated, false)).toEqual(["Germany", "Germany 2", "Server"]);
    expect(nodeLabels(repeated).join(" ")).not.toMatch(/de1|de2/);
    expect(nodeLabels([awgConfig({ node_name: "de1", country_code: "" })], false)).toEqual(["1"]); // no label: a number, not the node's name
  });

  it("the app versions a key needs, for the device's platform only", () => {
    const all = [
      { client: "amnezia", app: "AmneziaVPN", min: "5.0.1.5" },
      { client: "amnezia", app: "AmneziaWG Android", min: "v3.1.20260814" },
      { client: "mihomo", app: "Mihomo", min: "v1.19.30" },
    ];
    expect(versionsFor("ios", all)).toEqual([{ client: "amnezia", app: "AmneziaVPN", min: "5.0.1.5" }]);
    expect(versionsFor("android", all).map((m) => `${m.app} ${m.min}`)).toEqual(["AmneziaVPN 5.0.1.5", "AmneziaWG 3.1.20260814"]);
    expect(plain(t.needApp(versionsFor("android", all)))).toBe("Нужна AmneziaVPN 5.0.1.5 или новее — или AmneziaWG 3.1.20260814 и новее");
  });
});

describe("actions", () => {
  it("add: the device appears with the name the page gave it, the count goes up, the dialog turns into its key", async () => {
    const api = fakeApi();
    const { data: d, st, a, log, marked } = setup("many", api);
    const before = d.user.devices_used;
    a.add(true);
    expect(st.amz).toMatchObject({ adding: true });
    expect(st.amz.form.profile).toBe("p31"); // the main one, not the first in the list
    a.form({ platform: "windows", label: "" });
    a.create();
    expect(st.amz.busy).toBe("add");
    await settle();
    expect(api.calls).toEqual(["add p31 windows Windows 2"]); // "Windows" is taken by a device of the list
    expect(d.amnezia?.devices.at(-1)?.id).toBe("dev_new");
    expect(d.user.devices_used).toBe(before + 1);
    expect(st.amz).toMatchObject({ created: "dev_new", adding: true, open: null, busy: "", error: "" });
    expect(marked).toHaveLength(1); // this device set something up
    a.add(false);
    expect(st.amz).toMatchObject({ created: null, adding: false });
    expect(log).toEqual(["reveal amz-row-dev_new"]);
    a.form({ label: "  Work PC " });
    a.create();
    await settle();
    expect(api.calls.at(-1)).toBe("add p31 windows Work PC");
  });

  it("form changes redraw only when asked: typing must not lose the cursor", () => {
    const { a, draws } = setup("many", fakeApi());
    a.form({ label: "x" });
    expect(draws()).toBe(0);
    a.form({ platform: "mac" }, true);
    expect(draws()).toBe(1);
  });

  it("nothing is ever sent without an address (self-service off, the admin's preview): a call to '' would count as a guess", async () => {
    const api = fakeApi();
    const { a, st } = setup("amnezia-off", api);
    a.show("d3");
    a.renew("d3");
    a.rotate("d3");
    a.remove("d3");
    a.add(true);
    a.create();
    a.renameStart("d3");
    a.renameInput("New");
    a.renameSave();
    await settle();
    expect(api.calls).toEqual([]);
    expect(st.amz.open).toBeNull();
  });

  it("an expired password sends the page to the form instead of showing an error", async () => {
    let locked = 0;
    const d = data("many");
    const st = { lang: "ru" as const, amz: newAmzState("android", "p31") };
    const a = amzActions({ data: d, st, render: () => {}, api: fakeApi({ getConfigs: async () => Promise.reject(new ApiError(401, "locked")) }), reveal: () => {}, locked: () => locked++ });
    a.show("d3");
    await settle();
    expect(locked).toBe(1);
  });

  it("a refusal keeps the form and names the code where it happened", async () => {
    const api = fakeApi({ addDevice: async () => Promise.reject(new ApiError(409, "device_limit")) });
    const { data: d, st, a } = setup("many", api);
    const used = d.user.devices_used;
    a.add(true);
    a.create();
    await settle();
    expect(st.amz).toMatchObject({ error: "device_limit", errorAt: "add", busy: "", adding: true });
    expect(d.user.devices_used).toBe(used);
  });

  it("too many requests tells how many minutes", async () => {
    const { st, a } = setup("many", fakeApi({ getConfigs: async () => Promise.reject(new ApiError(429, "too_many_requests", 1800)) }));
    a.show("d3");
    await settle();
    expect(st.amz).toMatchObject({ error: "too_many_requests", errorAt: "d3", retryMin: 30, open: null });
  });

  it("show asks once, opens the dialog on the key and clears the outdated mark", async () => {
    const api = fakeApi();
    const { data: d, st, a, log } = setup("stale", api);
    expect(d.amnezia?.devices[0]?.stale).toBe(true);
    a.show("d3");
    expect(st.amz.open).toBeNull(); // no empty dialog while the key is on its way
    await settle();
    expect(d.amnezia?.devices[0]?.stale).toBe(false);
    expect(st.amz.open).toBe("d3");
    a.add(false); // closing the dialog (any kind)
    expect(st.amz.open).toBeNull();
    expect(log).toEqual(["reveal amz-row-d3"]);
    a.show("d3");
    expect(st.amz.open).toBe("d3");
    expect(api.calls).toEqual(["configs d3"]); // held in memory: no second write against the hourly budget
  });

  it("'new key needed': the key is fetched, then shown in the dialog; closing it scrolls to the device", async () => {
    const api = fakeApi();
    const { data: d, st, a, log } = setup("stale", api);
    a.renew("d3");
    expect(st.amz.busy).toBe("renew:d3");
    expect(st.amz.renew).toBeNull(); // no empty dialog while the key is on its way
    await settle();
    expect(api.calls).toEqual(["configs d3"]);
    expect(st.amz).toMatchObject({ renew: "d3", busy: "", adding: false });
    expect(d.amnezia?.devices[0]?.stale).toBe(false);
    a.add(false);
    expect(st.amz.renew).toBeNull();
    expect(log).toEqual(["reveal amz-row-d3"]);
  });

  it("'new key needed' that fails says so in its own card and opens nothing", async () => {
    const { st, a, data: d } = setup("stale", fakeApi({ getConfigs: async () => Promise.reject(new ApiError(409, "no_inbound")) }));
    a.renew("d3");
    await settle();
    expect(st.amz).toMatchObject({ renew: null, error: "no_inbound", errorAt: "renew:d3" });
    const el = view(d, state("android", { amz: st.amz }), actions());
    expect(text(el.querySelector(".stale .note.bad"))).toBe("Этот вариант пока нигде не запущен — напишите в поддержку.");
    expect(row(el, "d3").querySelector(".note.bad")).toBeNull();
  });

  it("a question takes the keyboard to its safe answer, and cancelling gives it back to the control that asked", () => {
    const { a, log } = setup("many", fakeApi());
    a.ask({ id: "d4", kind: "remove" });
    a.ask(null);
    a.ask({ id: "d4", kind: "rotate" });
    a.ask(null);
    expect(log).toEqual(["focus amz-no-d4", "focus amz-more-d4|amz-deld-d4", "focus amz-no-d4", "focus amz-more-d4|amz-rotd-d4"]);
  });

  it("rotate replaces the key; remove drops the device and frees the slot", async () => {
    const api = fakeApi();
    const { data: d, st, a } = setup("many", api);
    a.show("d3");
    await settle();
    a.ask({ id: "d3", kind: "rotate" });
    a.rotate("d3");
    await settle();
    expect(st.amz.configs.d3).toHaveLength(1);
    expect(st.amz.confirm).toBeNull();
    const used = d.user.devices_used;
    a.remove("d4");
    await settle();
    expect(d.amnezia?.devices.map((x) => x.id)).not.toContain("d4");
    expect(d.devices.map((x) => x.id)).not.toContain("d4");
    expect(d.user.devices_used).toBe(used - 1);
  });

  it("a second click while a call is in flight does nothing", async () => {
    const api = fakeApi();
    const { a } = setup("many", api);
    a.show("d3");
    a.show("d4");
    await settle();
    expect(api.calls).toEqual(["configs d3"]);
  });

  it("rename: the field is in the row with the name in it; saving sends the trimmed name and shows it; cancelling and an unchanged name send nothing", async () => {
    const api = fakeApi();
    const { data: d, st, a, log } = setup("many", api);
    a.menu("d3");
    expect(st.amz.menu).toBe("d3");
    a.renameStart("d3");
    expect(st.amz).toMatchObject({ menu: null, rename: { id: "d3", value: "Pixel 7" } });
    a.renameInput("  Телефон мамы ");
    a.renameSave();
    expect(st.amz.busy).toBe("rename:d3");
    await settle();
    expect(api.calls).toEqual(["rename d3 Телефон мамы"]);
    expect(d.amnezia?.devices.find((x) => x.id === "d3")?.label).toBe("Телефон мамы");
    expect(st.amz).toMatchObject({ rename: null, busy: "" });
    a.renameStart("d3");
    a.renameSave(); // unchanged: closes
    a.renameStart("d3");
    a.renameInput("   ");
    a.renameSave(); // empty: closes
    a.renameStart("d3");
    a.renameCancel();
    await settle();
    expect(api.calls).toHaveLength(1);
    expect(log.at(-1)).toBe("focus amz-more-d3|amz-rend-d3");
  });

  it("a failed rename keeps the field open and says what happened under it", async () => {
    const { st, a } = setup("many", fakeApi({ renameDevice: async () => Promise.reject(new ApiError(404, "not_found")) }));
    a.renameStart("d3");
    a.renameInput("X");
    a.renameSave();
    await settle();
    expect(st.amz).toMatchObject({ error: "not_found", errorAt: "rename:d3", busy: "" });
    expect(st.amz.rename).not.toBeNull();
  });
});

describe("the list of devices", () => {
  const rows = (name: string, over: Partial<ReturnType<typeof state>> = {}, a = actions()) => view(data(name), state("android", { returning: true, ...over }), a);

  it("each key with what a friend needs: name, platform, when; no AWG, profile or address", () => {
    const el = rows("many");
    const names = [...el.querySelectorAll(".dev .dev-n")].map(text);
    expect(names).toEqual(["Приложения по ссылке", "Pixel 7", "MacBook", "Windows"]);
    expect(meta(row(el, "d3"))).toBe("Android · в сети");
    expect(meta(row(el, "d4"))).toBe("Mac · подключалось 12 дней назад");
    expect(meta(row(el, "d5"))).toBe("ещё не подключалось");
    expect(el.textContent).not.toMatch(/AWG|Main|10\.66|fd66/);
  });

  it("a phone: 'Show key' across the row and the 'more' button; the menu has rename, replace and a red remove", () => {
    const a = actions({ amz: { ...amzNoop, show: (id) => void a.log.push(`show ${id}`), menu: (id) => void a.log.push(`menu ${id}`) } });
    const el = rows("many", {}, a);
    const acts = row(el, "d3").querySelector(".dev-acts.only-m")!;
    expect(text(acts.querySelector(".btn"))).toBe("Показать ключ");
    acts.querySelector<HTMLButtonElement>("[data-k=amz-show-d3]")!.click();
    acts.querySelector<HTMLButtonElement>("[data-k=amz-more-d3]")!.click();
    expect(a.log).toEqual(["show d3", "menu d3"]);
    expect(acts.querySelector("[data-k=amz-more-d3]")?.getAttribute("aria-label")).toBe("Ещё: переименовать, заменить ключ, удалить");
    const open = rows("many", { amz: { ...newAmzState("android", "p31", "android"), menu: "d3" } });
    const menu = row(open, "d3").querySelector(".menu")!;
    expect([...menu.querySelectorAll("[role=menuitem]")].map(text)).toEqual(["Переименовать", "Заменить ключ", "Удалить"]);
    expect(menu.querySelector(".bad")).not.toBeNull();
    expect(row(open, "d3").querySelector("[data-k=amz-more-d3]")?.getAttribute("aria-expanded")).toBe("true");
  });

  it("a computer: quiet words in the row", () => {
    const a = actions({ amz: { ...amzNoop, ask: (c) => void a.log.push(`ask ${c?.kind}`), renameStart: (id) => void a.log.push(`rename ${id}`) } });
    const el = rows("many", {}, a);
    const links = row(el, "d3").querySelector(".links.only-w")!;
    expect([...links.querySelectorAll("button")].map(text)).toEqual(["Показать ключ", "Переименовать", "Заменить ключ", "Удалить"]);
    expect(links.querySelector(".bad")).not.toBeNull();
    links.querySelector<HTMLButtonElement>("[data-k=amz-rend-d3]")!.click();
    links.querySelector<HTMLButtonElement>("[data-k=amz-rotd-d3]")!.click();
    links.querySelector<HTMLButtonElement>("[data-k=amz-deld-d3]")!.click();
    expect(a.log).toEqual(["rename d3", "ask rotate", "ask remove"]);
  });

  it("the questions say what happens, in the row, with a red 'Remove', a plain 'Cancel' and an alertdialog", () => {
    const remove = row(rows("many", { amz: { ...newAmzState("android", "p31", "android"), confirm: { id: "d3", kind: "remove" } } }), "d3");
    expect(remove.getAttribute("role")).toBe("alertdialog");
    expect(text(remove.querySelector(".dev-n"))).toBe("Удалить «Pixel 7»?");
    expect(text(remove.querySelector(".sm.mut"))).toBe("VPN на нём сразу перестанет работать.");
    expect([...remove.querySelectorAll(".dev-acts button")].map((b) => [text(b), b.className])).toEqual([["Отмена", "btn sec grow"], ["Удалить", "btn bad grow"]]);
    const rotate = row(rows("many", { amz: { ...newAmzState("android", "p31", "android"), confirm: { id: "d3", kind: "rotate" } } }), "d3");
    expect(text(rotate.querySelector(".dev-n"))).toBe("Заменить ключ «Pixel 7»?");
    expect(text(rotate.querySelector(".sm.mut"))).toBe("Старый перестанет работать сразу — новый нужно будет добавить в AmneziaVPN заново.");
    expect(text(rotate.querySelector(".btn.pri"))).toBe("Заменить");
  });

  it("the rename field has the name, the limit and the platform; Save and Cancel", () => {
    const el = rows("many", { amz: { ...newAmzState("android", "p31", "android"), rename: { id: "d3", value: "Pixel 7" } } });
    const r = row(el, "d3");
    expect(r.tagName).toBe("FORM");
    expect(r.querySelector<HTMLInputElement>("input")?.value).toBe("Pixel 7");
    expect(r.querySelector("input")?.getAttribute("maxlength")).toBe("40");
    expect(text(r.querySelector(".hint"))).toBe("До 40 символов · Android");
    expect([...r.querySelectorAll(".dev-acts button")].map(text)).toEqual(["Отмена", "Сохранить"]);
  });

  it("a busy row says 'One moment' on the pressed button; an error stays in the row", () => {
    const am = { ...newAmzState("android", "p31", "android"), busy: "d3" };
    const busy = row(rows("many", { amz: am }), "d3");
    expect(text(busy.querySelector(".dev-acts .btn"))).toBe("Секунду…");
    expect(busy.querySelector<HTMLButtonElement>("[data-k=amz-more-d3]")?.disabled).toBe(true);
    const err = row(rows("many", { amz: { ...newAmzState("android", "p31", "android"), error: "too_many_requests", errorAt: "d4", retryMin: 18 } }), "d4");
    expect(text(err.querySelector(".note.bad"))).toBe("Слишком много действий подряд. Попробуйте через 18 мин.");
  });

  it("a stale key is marked, and its main action is the new key", () => {
    const a = actions({ amz: { ...amzNoop, renew: (id) => void a.log.push(`renew ${id}`) } });
    const r = row(rows("stale", {}, a), "d3");
    expect(text(r.querySelector(".stag"))).toBe("нужен новый ключ");
    expect(text(r.querySelector(".dev-acts .btn"))).toBe("Получить новый ключ");
    expect(r.querySelector(".dev-acts .btn")?.classList.contains("pri")).toBe(true);
    r.querySelector<HTMLButtonElement>("[data-k=amz-renew-d3]")!.click();
    expect(a.log).toEqual(["renew d3"]);
  });

  it("the slot counter: segments and the words", () => {
    const el = rows("many");
    expect(el.querySelectorAll(".slots .segs i")).toHaveLength(8);
    expect(el.querySelectorAll(".slots .segs i.f")).toHaveLength(4);
    expect(text(el.querySelector(".slots .only-m"))).toBe("4 из 8");
    expect(text(el.querySelector(".slots .only-w"))).toBe("занято 4 из 8");
    expect(el.querySelector(".slots")?.getAttribute("aria-label")).toBe("Занято 4 из 8");
    expect(rows("plain").querySelector(".slots")).toBeNull();
  });

  it("add a device: one button that opens the dialog; with no keys yet the row explains", () => {
    const a = actions();
    const el = rows("many", {}, a);
    const btns = [...el.querySelectorAll("button")].filter((b) => text(b) === "Добавить устройство");
    expect(btns).toHaveLength(1);
    btns[0]!.click();
    expect(a.log).toEqual(["add true"]);
    const empty = rows("amnezia-empty");
    expect([...empty.querySelectorAll(".dev-n")].map(text)).toEqual(["Приложения по ссылке", "Ключи AmneziaVPN"]);
    expect(text(empty.querySelector(".dev-b .sm.mut"))).toBe("Пока ни одного. Ключ выдаётся на каждое устройство отдельно");
  });

  it("every slot taken: the words, 'Add' waits, and the way to write", () => {
    const el = rows("limit");
    const slotRow = el.querySelector(".dev-x")!.closest(".dev")!;
    expect(text(slotRow.querySelector(".note.warn"))).toBe("Занято 3 из 3. Удалите устройство, которым больше не пользуетесь, или напишите — добавим место.");
    expect(slotRow.querySelector<HTMLButtonElement>("[data-k=amz-add]")?.disabled).toBe(true);
    expect(text(slotRow.querySelector("a.btn"))).toBe("Написать");
    expect(text(rows("limit").querySelector(".slots .only-m"))).toBe("3 из 3");
  });

  it("the owner issues the keys: rows without buttons, 'ask for a key' and Write", () => {
    const el = rows("amnezia-off");
    expect(el.querySelectorAll(".dev-acts.only-m, .links")).toHaveLength(0);
    expect(el.querySelector("[data-k=amz-add]")).toBeNull();
    const ask = [...el.querySelectorAll(".dev")].at(-1)!;
    expect(text(ask.querySelector(".dev-n"))).toBe("Попросите ключ");
    expect(text(ask.querySelector(".sm.mut"))).toBe("Ключи AmneziaVPN выдаёт владелец — напишите, для какого устройства нужен.");
    expect(text(ask.querySelector("a.btn"))).toBe("Написать");
  });

  it("no profile: keys are not available yet", () => {
    const el = rows("amnezia-none");
    const last = [...el.querySelectorAll(".dev")].at(-1)!;
    expect(text(last.querySelector(".dev-n"))).toBe("Ключи пока недоступны");
    expect(text(last.querySelector(".sm.mut"))).toBe("Сервер ещё настраивается — напишите администратору.");
    expect(text(view(data("amnezia-none"), state("android", { returning: true }, "en"), actions()))).toContain("The server is still being set up — message the admin.");
  });

  it("the admin's preview (no address) shows the rows without buttons and does not say 'ask the owner'", () => {
    const el = rows("many", {}, actions());
    const d = data("many", (x) => (x.amnezia!.endpoints = ""));
    const p = view(d, state("android", { returning: true }), actions());
    expect(p.querySelectorAll(".dev-acts.only-m, .links")).toHaveLength(0);
    expect(p.querySelector("[data-k=amz-add]")).toBeNull();
    expect(p.textContent).not.toContain("Попросите ключ");
    expect(el.querySelectorAll(".dev-acts.only-m").length).toBeGreaterThan(0);
  });

  it("only the link: one row", () => {
    const el = rows("happ");
    expect([...el.querySelectorAll(".dev-n")].map(text)).toEqual(["Приложения по ссылке"]);
    expect(text(el.querySelector(".dev .hint"))).toBe("Все устройства с этой ссылкой занимают одно место");
  });

  it("when the subscription is not active the devices stay: rename and remove, but no key is shown or issued", () => {
    for (const name of ["expired", "quota", "disabled"]) {
      const el = view(data(name), state("android"), actions());
      const r = row(el, "d3");
      const show = r.querySelector<HTMLButtonElement>("[data-k=amz-show-d3]")!;
      expect(show.disabled).toBe(true);
      expect(show.getAttribute("aria-describedby")).toBe("why-off");
      expect(el.querySelector("#why-off")?.textContent).toBe("Подписка сейчас не активна");
      expect(el.querySelector<HTMLButtonElement>(".dev .btn.out")?.disabled).toBe(true);
      expect(r.querySelector("[data-k=amz-more-d3]")?.getAttribute("aria-label")).toBe("Ещё: переименовать, удалить");
      expect(text(el.querySelector(".dev .note"))).toBe(name === "disabled" ? "Подписка отключена, ключи не выдаются. Удалить или переименовать устройство можно." : "Подписка сейчас не активна — ключи не выдаются. Удалить или переименовать устройство можно.");
      expect(el.querySelector(".srv")).toBeNull();
    }
  });
});

describe("the key dialog", () => {
  const modal = (here: string, id: string, mode: "open" | "renew" | "created" = "open", over: (s: AmzState) => void = () => {}, name = "return") => {
    const d = data(name);
    const s = { lang: "ru" as const, amz: newAmzState("android", "p31", here) };
    s.amz[mode] = id;
    s.amz.configs[id] = [cfg("de1"), cfg("fi1", "FI")];
    over(s.amz);
    const box = document.createElement("div");
    box.append(...(addModal({ d, s, a: actions(), t, support: "https://t.me/example_support" }) as Node[]));
    return box;
  };

  it("where to add it: a phone offers itself or another device, a computer itself, a phone or another computer", () => {
    expect(whereOptions("android", t).map((o) => o.id)).toEqual(["this", "other"]);
    expect(whereOptions("windows", t).map((o) => o.label)).toEqual(["Этот компьютер", "Телефон", "Другой компьютер"]);
    expect(whereOptions("", t).map((o) => o.id)).toEqual(["phone", "other"]);
    expect([defaultWhere("android", "android"), defaultWhere("ios", "android"), defaultWhere("windows", "windows"), defaultWhere("android", "windows"), defaultWhere("macos", "windows"), defaultWhere("ios", "")]).toEqual(["this", "other", "this", "phone", "other", "phone"]);
    expect([keyView("android", "android", "this"), keyView("macos", "windows", "this"), keyView("ios", "windows", "phone"), keyView("macos", "windows", "other"), keyView("windows", "android", "other"), keyView("android", "ios", "other")]).toEqual([
      "this-phone",
      "this-desktop",
      "phone-qr",
      "other-desktop",
      "other-desktop",
      "phone-qr",
    ]);
  });

  it("on this very phone the key is copied: the copy button leads, then the steps; the title says whose key it is", () => {
    const b = modal("android", "d3");
    expect(text(b.querySelector("h3"))).toBe("Ключ для «Pixel 7»");
    expect(text(b.querySelector(".sh-head .sub"))).toBe("Устройство уже в списке — ключ можно показать снова");
    expect([...b.querySelectorAll(".seg:not(.pills) [role=radio]")].map((x) => [text(x), x.getAttribute("aria-checked")])).toEqual([["Этот телефон", "true"], ["Другое устройство", "false"]]);
    expect(text(b.querySelector(".cfg > .btn.pri, .cfg .btn.pri"))).toBe("Скопировать ключ");
    expect([...b.querySelectorAll(".steps-box li")].map(text)).toEqual(["1Откройте AmneziaVPN", "2Нажмите «+» и вставьте ключ", "3Нажмите «Продолжить»"]);
    expect(b.querySelector(".qr")).toBeNull();
    expect(plain(text(b.querySelector(".hint.row")))).toBe("Нужна AmneziaVPN 5.0.1.5 или новее — или AmneziaWG 3.1.20260814 и новее");
    expect(text(b.querySelector(".priv"))).toBe("В ключе личные данные устройства — не пересылайте его.");
    expect(b.textContent).not.toContain("vpn://"); // the key is copied, never written out
  });

  it("on this very computer the file is downloaded, and the page says why not the key", () => {
    const b = modal("windows", "d5", "open", () => {}, "many");
    expect(text(b.querySelector(".btn.pri"))).toBe("Скачать файл");
    expect(text(b.querySelector(".keyfile"))).toBe("mistgate-de.conf");
    expect(text(b.querySelector("p.sm:not(.mut)"))).toBe("Затем: AmneziaVPN → «+» → «Файл с настройками подключения»");
    expect(text(b.querySelector(".note.warn"))).toBe("На компьютере добавляйте файл, а не ключ — иначе соединение может не заработать.");
  });

  it("for a phone the QR code comes first, dark on white, with what to press in the app; the file and the key are still at hand", () => {
    const b = modal("windows", "d3");
    expect([...b.querySelectorAll(".seg:not(.pills) [role=radio]")].map((x) => x.getAttribute("aria-checked"))).toEqual(["false", "true", "false"]);
    expect(b.querySelector(".keyq .qr svg")).not.toBeNull();
    expect(text(b.querySelector(".keyq-t .b"))).toBe("Отсканируйте в AmneziaVPN");
    expect(text(b.querySelector(".keyq-t .sm.mut"))).toBe("«+» → «QR-код» и наведите камеру телефона на код");
    expect([...b.querySelectorAll(".keyq-t .btn")].map(text)).toEqual(["Скачать файл", "Скопировать ключ"]);
  });

  it("for another computer: open this page there; the file can be moved by hand", () => {
    const b = modal("windows", "d4");
    expect(text(b.querySelector(".h3"))).toBe("Откройте эту страницу на том компьютере");
    expect(text(b.querySelector(".stack .sm.mut"))).toMatch(/^Проще всего так: на нужном компьютере/);
    expect([...b.querySelectorAll(".cfg .btn")].map(text)).toEqual(["Скачать файл", "Готово"]);
  });

  it("the choice of where to add comes from the page's state", () => {
    const b = modal("android", "d3", "open", (s) => (s.where.d3 = "other"));
    expect(b.querySelector(".keyq .qr")).not.toBeNull();
  });

  it("the country choice: names with the flag (none on Windows), and it explains itself", () => {
    const b = modal("android", "d3");
    expect([...b.querySelectorAll(".pick select option")].map(text)).toEqual(["🇩🇪 Германия · Франкфурт", "🇫🇮 Финляндия"]);
    expect(text(b.querySelector(".pick .hint"))).toBe("ещё 1");
    const w = modal("windows", "d3");
    expect([...w.querySelectorAll(".pick select option")].map(text)).toEqual(["Германия · Франкфурт", "Финляндия"]);
    expect([...w.querySelectorAll(".seg.pills button")].map(text)).toEqual(["Германия · Франкфурт", "Финляндия"]);
    expect(text(w.querySelector(".fld > .hint.only-w"))).toBe("Каждая страна — отдельное подключение. Добавьте одну; перестанет работать — добавьте другую.");
  });

  it("the new key of a stale device: what to add, and which old connection goes (by country, never by the panel's node)", () => {
    const b = modal("android", "d3", "renew", () => {}, "stale");
    expect(text(b.querySelector("h3"))).toBe("Новый ключ для «Pixel 7»");
    expect(text(b.querySelector(".sh-head .sub"))).toBe("Старый ключ больше не работает");
    expect([...b.querySelectorAll(".steps-box li")].map((x) => text(x.lastElementChild))).toEqual(["В AmneziaVPN нажмите «+», вставьте ключ и «Продолжить»", "Удалите старое подключение «Германия» — оно больше не подключается"]);
    expect(b.textContent).not.toMatch(/de1|fi1/);
    expect(b.querySelector("[data-autofocus]")).toBe(b.querySelector(".btn.pri"));
    const pc = modal("windows", "d3", "renew", () => {}, "stale");
    expect(text(pc.querySelector(".note:not(.warn)"))).toBe("Удалите старое подключение «Германия» — оно больше не подключается");
  });

  it("after Create the dialog shows the new device's key with its own words", () => {
    const b = modal("android", "d3", "created");
    expect(text(b.querySelector(".sh-head .sub"))).toBe("Настройте устройство сейчас. Закроете окно — устройство останется в списке, ключ можно будет показать снова.");
    expect(b.querySelector("form")).toBeNull();
  });

  it("buttons copy and download what the key is", () => {
    const a = actions();
    const d = data("return");
    const s = { lang: "ru" as const, amz: newAmzState("android", "p31", "windows") };
    s.amz.open = "d3";
    s.amz.configs.d3 = [cfg("de1")];
    const box = document.createElement("div");
    box.append(...(addModal({ d, s, a, t }) as Node[]));
    const [dl, cp] = [...box.querySelectorAll<HTMLButtonElement>(".keyq-t .btn")];
    dl!.click();
    cp!.click();
    expect(a.log).toEqual(["download mistgate-de.conf", "copy vpn://k"]);
    box.querySelector<HTMLButtonElement>("[data-k=modal-done]")!.click();
    expect(a.log.at(-1)).toBe("add false");
  });

  it("a label is text, never markup", () => {
    const evil = structuredClone(cases.many!);
    evil.amnezia!.devices[0]!.label = "<img src=x onerror=alert(1)>";
    const el = view(normalize(evil), state("android", { returning: true }, "en"), actions());
    expect(el.querySelector("img[onerror]")).toBeNull();
    expect(text(row(el, "d3").querySelector(".dev-n"))).toBe("<img src=x onerror=alert(1)>");
  });
});

describe("the add-a-device dialog", () => {
  const form = (name = "many", over: (s: AmzState) => void = () => {}, here = "ios") => {
    const d = data(name);
    const s = { lang: "ru" as const, amz: newAmzState(here, "p31", here) };
    s.amz.adding = true;
    over(s.amz);
    const box = document.createElement("div");
    box.append(...(addModal({ d, s, a: actions(), t, support: "https://t.me/example_support" }) as Node[]));
    return box;
  };

  it("the device (six choices), its name, and the connection option only when there is a choice, named by countries", () => {
    const b = form();
    expect(text(b.querySelector("h3"))).toBe("Новое устройство");
    expect(text(b.querySelector(".sh-head .sub"))).toBe("Для него появится свой ключ AmneziaVPN");
    expect([...b.querySelectorAll(".plats .plat")].map(text)).toEqual(["iPhone", "Android", "Windows", "Mac", "Linux", "Другое"]);
    expect(b.querySelector<HTMLElement>(".plat.on")?.textContent).toBe("iPhone");
    expect(b.querySelector<HTMLInputElement>("[data-k=amz-label]")?.placeholder).toBe("Например, телефон мамы");
    expect(text(b.querySelector(".pick .t"))).toBe("🇩🇪 Германия, 🇫🇮 Финляндия");
    expect(text(b.querySelector(".pick .s"))).toBe("Обычное — подходит почти всегда");
    expect([...b.querySelectorAll(".pick select option")].map(text)).toHaveLength(3);
    expect(b.querySelector("[data-k=modal-create]")?.textContent).toBe("Создать ключ");
    expect(form("both").querySelector(".pick")).toBeNull();
  });

  it("busy: 'One moment', and the button waits", () => {
    const b = form("many", (s) => (s.busy = "add"));
    const go = b.querySelector<HTMLButtonElement>("[data-k=modal-create]")!;
    expect([go.textContent, go.disabled]).toEqual(["Секунду…", true]);
  });

  it("'all slots are used' is an error in the form with the way to write", () => {
    const b = form("many", (s) => ((s.error = "device_limit"), (s.errorAt = "add")));
    const note = b.querySelector(".note.bad")!;
    expect(note.getAttribute("role")).toBe("alert");
    expect(text(note.querySelector("b"))).toBe("Все места заняты.");
    expect(text(note.querySelector("a.tlink"))).toBe("Написать в Telegram");
    const other = form("many", (s) => ((s.error = "network"), (s.errorAt = "add")));
    expect(text(other.querySelector(".note.bad"))).toBe("Нет связи. Проверьте интернет и повторите.");
  });

  it("the stale devices of a page", () => {
    expect(staleDevices(data("stale")).map((x) => x.id)).toEqual(["d3"]);
    expect(staleDevices(data("first"))).toEqual([]);
  });
});

describe("the 'new key needed' card", () => {
  it("names the device, says why, has a button per stale device and steps in a safe order", () => {
    const one = view(data("stale"), state("android", { returning: true }), actions());
    expect(text(one.querySelector(".stale .h3"))).toBe("Нужен новый ключ для «Pixel 7»");
    expect(text(one.querySelector(".stale .sm.mut"))).toBe("Серверы обновились — старый ключ этого устройства больше не работает.");
    expect([...one.querySelectorAll(".stale ol li")].map((li) => text(li.lastElementChild))).toEqual(["Нажмите «Получить новый ключ»", "Добавьте его в AmneziaVPN, как в первый раз", "Удалите старое подключение этого устройства — оно больше не работает"]);
    expect(one.querySelector(".stale")?.textContent).not.toContain("ниже");
    const two = data("many");
    two.amnezia!.devices.forEach((x) => (x.stale = true));
    const el = view(two, state("android", { returning: true }), actions());
    expect(text(el.querySelector(".stale .h3"))).toBe("Нужны новые ключи");
    expect([...el.querySelectorAll(".stale button")].map(text)).toEqual(["Новый ключ для «Pixel 7»", "Новый ключ для «MacBook»", "Новый ключ для «Windows»"]);
  });

  it("a key stale only because of a DNS says the key itself stays", () => {
    const el = view(data("stale-dns"), state("android", { returning: true }), actions());
    expect(text(el.querySelector(".stale .sm.mut"))).toBe("DNS сервера изменился — получите ключ заново. Сам ключ останется прежним.");
  });

  it("without self-service it says to ask, and has no buttons", () => {
    const el = view(data("stale", (d) => (d.amnezia!.self_service = false)), state("android"), actions());
    expect(text(el.querySelector(".stale .sm.mut"))).toBe("Ключи выдаёт администратор — напишите, если нужен новый.");
    expect(el.querySelector(".stale button")).toBeNull();
  });

  it("is not shown for a subscription that is not active", () => {
    const d = data("stale", (x) => (x.user.status = "expired"));
    expect(view(d, state("android"), actions()).querySelector(".stale")).toBeNull();
  });
});
