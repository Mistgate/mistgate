import { describe, expect, it } from "vitest";
import { deviceModal } from "./add";
import { defaultWhere, keyView, newAmzState, staleDevices, whereOptions, type AmzState } from "./amnezia";
import { amzActions, type Api } from "./amz-actions";
import { ApiError, endpoint } from "./api";
import { cases } from "./dev-data";
import { dict } from "./i18n";
import { awgConfig, awgDevice, canAddDevice, defaultLabel, mainProfile, nodeLabels, normalize, otherDevices, profileChoices, selfServe, versionsFor } from "./logic";
import { actions, amzNoop, data, dialog, plain, state, text } from "./test-kit";
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
    expect(plain(t.needApp([{ app: "Hiddify", min: "2.0 only" }]))).toBe("Нужна Hiddify (только AWG 2.0)");
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
    expect(log).toEqual(["focus amz-no-d4", "focus amz-more-d4", "focus amz-no-d4", "focus amz-more-d4"]);
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
    expect(log.at(-1)).toBe("focus amz-more-d3");
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
    expect(names).toEqual(["Приложения без ID устройства", "Pixel 7", "MacBook", "Windows"]);
    expect(meta(row(el, "d3"))).toBe("Android · в сети");
    expect(meta(row(el, "d4"))).toBe("Mac · подключалось 12 дней назад");
    expect(meta(row(el, "d5"))).toBe("ещё не подключалось");
    expect(el.textContent).not.toMatch(/AWG|Main|10\.66|fd66/);
  });

  it("one block of actions on a phone and a computer: the main one and 'more'; the menu has rename, replace and a red remove", () => {
    const a = actions({ amz: { ...amzNoop, show: (id) => void a.log.push(`show ${id}`), menu: (id) => void a.log.push(`menu ${id}`) } });
    const el = rows("many", {}, a);
    expect(el.querySelectorAll(".dev-acts.only-m, .dev-acts.only-w, .links")).toHaveLength(0); // no second set of buttons for the other size
    const acts = row(el, "d3").querySelector(".dev-acts")!;
    expect(text(acts.querySelector(".btn"))).toBe("Показать ключ");
    expect([...acts.children].filter((x) => x.tagName === "BUTTON")).toHaveLength(2); // the main action and "more": not four words
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

  it("the menu items do what they say", () => {
    const a = actions({ amz: { ...amzNoop, ask: (c) => void a.log.push(`ask ${c?.kind}`), renameStart: (id) => void a.log.push(`rename ${id}`) } });
    const open = rows("many", { amz: { ...newAmzState("android", "p31", "android"), menu: "d3" } }, a);
    open.querySelector<HTMLButtonElement>("[data-k=amz-m-ren-d3]")!.click();
    open.querySelector<HTMLButtonElement>("[data-k=amz-m-rot-d3]")!.click();
    open.querySelector<HTMLButtonElement>("[data-k=amz-m-del-d3]")!.click();
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

  // a person with keys alone: no link app, so "Add a device" is the key form (the way is not asked)
  const keysAlone = (name: string, f: (d: MgData) => void = () => {}) => view(data(name, (d) => { d.access.happ = false; d.devices = d.devices.filter((x) => d.amnezia?.devices.some((k) => k.id === x.id)); f(d); }), state("android", { returning: true }), actions());

  it("add a device: one button for both ways that opens the dialog; the free slots under it", () => {
    const a = actions();
    const el = rows("many", {}, a);
    const btns = [...el.querySelectorAll("button")].filter((b) => text(b) === "Добавить устройство");
    expect(btns).toHaveLength(1);
    expect(btns[0]!.getAttribute("data-k")).toBe("dev-add");
    btns[0]!.click();
    expect(a.log).toEqual(["add true"]);
    expect(text(el.querySelector(".dev-hint"))).toBe("Свободно ещё 4 места");
    // no second block "connect one more device" under the card
    expect(el.querySelector("[data-k=more]")).toBeNull();
  });

  it("with no keys yet the list is the link's row; keys alone get the row that explains", () => {
    expect([...rows("amnezia-empty").querySelectorAll(".dev-n")].map(text)).toEqual(["Приложения по ссылке"]);
    const empty = keysAlone("amnezia-empty");
    expect([...empty.querySelectorAll(".dev-n")].map(text)).toEqual(["Ключи AmneziaVPN"]);
    expect(text(empty.querySelector(".dev-b .sm.mut"))).toBe("Пока ни одного. Ключ выдаётся на каждое устройство отдельно");
  });

  it("every slot taken: the link still connects, so 'Add' stays; only the words change", () => {
    const el = rows("limit");
    const btn = el.querySelector<HTMLButtonElement>("[data-k=dev-add]")!;
    expect(btn.disabled).toBe(false);
    expect(text(btn.closest(".dev")!.querySelector(".dev-hint"))).toBe("Все места заняты");
    expect(text(el.querySelector(".slots .only-m"))).toBe("3 из 3");
  });

  it("every slot taken and keys the only way: the words, 'Add' waits, and the way to write", () => {
    const el = keysAlone("limit");
    const slotRow = el.querySelector(".dev-x")!.closest(".dev")!;
    expect(text(slotRow.querySelector(".note.warn"))).toBe("Занято 3 из 3. Удалите устройство, которым больше не пользуетесь, или напишите — добавим место.");
    expect(slotRow.querySelector<HTMLButtonElement>("[data-k=dev-add]")?.disabled).toBe(true);
    expect(text(slotRow.querySelector("a.btn"))).toBe("Написать");
  });

  it("the owner issues the keys: rows without buttons; keys alone get 'ask for a key' and Write, with the link 'Add' still opens the sheet", () => {
    const both = rows("amnezia-off");
    expect(both.querySelectorAll(".dev-acts [data-k^=amz-]")).toHaveLength(0); // no key actions (the link row has its own: copy)
    expect(both.querySelector("[data-k=dev-add]")).not.toBeNull();
    const el = keysAlone("amnezia-off");
    expect(el.querySelectorAll(".dev-acts")).toHaveLength(0);
    expect(el.querySelector("[data-k=dev-add]")).toBeNull();
    const ask = [...el.querySelectorAll(".dev")].at(-1)!;
    expect(text(ask.querySelector(".dev-n"))).toBe("Попросите ключ");
    expect(text(ask.querySelector(".sm.mut"))).toBe("Ключи AmneziaVPN выдаёт владелец — напишите, для какого устройства нужен.");
    expect(text(ask.querySelector("a.btn"))).toBe("Написать");
  });

  it("no profile: keys are not available yet", () => {
    const el = keysAlone("amnezia-none");
    const last = [...el.querySelectorAll(".dev")].at(-1)!;
    expect(text(last.querySelector(".dev-n"))).toBe("Ключи пока недоступны");
    expect(text(last.querySelector(".sm.mut"))).toBe("Сервер ещё настраивается — напишите администратору.");
    expect(text(view(data("amnezia-none", (d) => (d.access.happ = false)), state("android", { returning: true }, "en"), actions()))).toContain("The server is still being set up — message the admin.");
  });

  it("the admin's preview (no address) shows the rows without buttons and does not say 'ask the owner'; the link branch still opens", () => {
    const d = data("many", (x) => (x.amnezia!.endpoints = ""));
    const p = view(d, state("android", { returning: true }), actions());
    expect(p.querySelectorAll(".dev-acts [data-k^=amz-]")).toHaveLength(0);
    expect(p.querySelector("[data-k=dev-add]")).not.toBeNull(); // the link needs no address
    expect(p.textContent).not.toContain("Попросите ключ");
    expect(rows("many").querySelectorAll(".dev-acts").length).toBeGreaterThan(0);
    // keys alone: nothing to press
    const keys = view(data("many", (x) => ((x.amnezia!.endpoints = ""), (x.access.happ = false))), state("android", { returning: true }), actions());
    expect(keys.querySelector("[data-k=dev-add]")).toBeNull();
  });

  it("only the shared place: one row of one line, and 'Add a device' is there for the next app", () => {
    const a = actions();
    const el = rows("happ", {}, a);
    expect([...el.querySelectorAll(".dev-n")].map(text)).toEqual(["Приложения без ID устройства"]);
    expect(text(el.querySelector(".dev .dev-m"))).toBe("одно место на всех·в сети"); // online: the state, else the time alone
    expect(el.querySelector(".dev .hint")).toBeNull();
    el.querySelector<HTMLButtonElement>("[data-k=dev-add]")!.click();
    expect(a.log).toEqual(["add true"]);
  });

  it("no hint under the title: a device is a place, whatever the way (the shared place says its own exception)", () => {
    const hint = (el: HTMLElement) => [...el.querySelectorAll(".shead")].find((x) => text(x.querySelector(".h2")) === "Мои устройства")?.querySelector(".sec-sub");
    expect(hint(rows("many"))).toBeNull();
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
  /** The window on the key of `id` for a person whose page is open on `here`; `over` may change the state of the keys first. */
  const modal = (here: "android" | "windows", id: string, mode: "open" | "renew" | "created" = "open", over: (s: AmzState) => void = () => {}, name = "return") => {
    const amz = { [mode]: id, configs: { [id]: [cfg("de1"), cfg("fi1", "FI")] }, here } as Partial<AmzState>;
    const r = dialog(data(name), { platform: here, amz });
    over(r.c.s.amz);
    const box = document.createElement("div");
    box.append(...(deviceModal(r.c) as Node[]));
    return box;
  };
  const wheres = (b: HTMLElement) => [...b.querySelectorAll(".a-b > .a-fld:first-child .a-seg [role=radio]")].map((x) => [text(x), x.getAttribute("aria-checked")]);
  const withKey = (extra: Partial<Parameters<typeof dialog>[1] & object> = {}, list = [cfg("de1")]) => dialog(data("return"), { platform: "windows", amz: { open: "d3", configs: { d3: list } }, ...extra });

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

  it("the frame of a key: its title says whose key it is, there is no count, the foot says it is in the list", () => {
    const b = modal("android", "d3");
    expect(text(b.querySelector(".a-h .t"))).toBe("Ключ для Pixel 7");
    expect(text(b.querySelector(".a-h .n"))).toBe("");
    expect([...b.querySelectorAll(".a-b > *")].map((x) => x.className)).toEqual(["a-fld", "a-steps", "a-meta"]);
    expect(text(b.querySelector(".a-f .u-hint"))).toBe("Устройство уже в списке — ключ можно показать снова");
    expect(text(b.querySelector(".a-f .u-btn"))).toBe("Готово");
  });

  it("on this very phone the key is copied: the buttons, then the steps; what the person must know", () => {
    const b = modal("android", "d3");
    expect(wheres(b)).toEqual([["Этот телефон", "true"], ["Другое устройство", "false"]]);
    expect([...b.querySelectorAll("ol.a-steps > .a-st")].map(text)).toEqual(["1Откройте AmneziaVPN", "2Нажмите «+» и вставьте ключ", "3Нажмите «Продолжить»"]);
    expect([...b.querySelectorAll(".u-acts > *")].map(text)).toEqual(["Скопировать ключСкопировано"]);
    expect(b.querySelector(".u-qr")).toBeNull();
    expect([...b.querySelectorAll(".a-meta p")].map(text)).toEqual(["Ключ личный, не пересылайте его", "Нужна AmneziaVPN 5.0.1.5 или новее — или AmneziaWG 3.1.20260814 и новее"]);
    expect(b.textContent).not.toContain("vpn://"); // the key is copied, never written out
  });

  it("on this very computer the file is downloaded, and the page says why not the key", () => {
    const b = modal("windows", "d5", "open", () => {}, "many");
    expect([...b.querySelectorAll(".u-acts > *")].map(text)).toEqual(["Скачать файл", "mistgate-de.conf"]);
    expect([...b.querySelectorAll("ol.a-steps > .a-st .tx")].map(text)).toEqual(["Скачайте файл с ключом", "Откройте AmneziaVPN, нажмите «+» и выберите «Файл с настройками подключения»"]);
    expect([...b.querySelectorAll(".a-meta p")].map(text)).toContain("На компьютере добавляйте файл, а не ключ — иначе соединение может не заработать.");
  });

  it("for a phone the QR code comes first, dark on white, with what to press in the app; the file and the key are still at hand", () => {
    const b = modal("windows", "d3");
    expect(wheres(b).map((x) => x[1])).toEqual(["false", "true", "false"]);
    const two = b.querySelector(".a-two")!;
    expect([...two.children].map((x) => x.className)).toEqual(["a-steps", "a-side"]);
    expect(two.querySelector(".a-side .u-qr svg")).not.toBeNull();
    expect(two.querySelector(".a-side .u-qr .u-qt img")).not.toBeNull();
    expect(two.querySelector(".a-side svg")?.getAttribute("aria-label")).toBe("QR-код ключа для AmneziaVPN");
    expect([...two.querySelectorAll("ol.a-steps .a-st .tx")].map(text)).toEqual(["Откройте AmneziaVPN на телефоне", "Нажмите «+», затем «QR-код»", "Наведите камеру на код"]);
    expect([...two.querySelectorAll(".u-acts > *")].map(text)).toEqual(["Скачать файл", "Скопировать ключСкопировано"]);
  });

  it("a key too long for a QR code says so, and the file and the key stay", () => {
    const long = awgConfig({ node_id: "x", label: "X", country_code: "DE", conf: "x".repeat(4000), vpn_key: "vpn://k", filename: "x.conf" });
    const b = modal("windows", "d3", "open", (s) => (s.configs.d3 = [long]));
    expect(b.querySelector(".u-qr, .a-two")).toBeNull();
    expect([...b.querySelectorAll(".a-meta p")].map(text).some((x) => x.includes("слишком длинный для QR-кода"))).toBe(true);
    expect(b.querySelectorAll(".u-acts > *")).toHaveLength(2);
  });

  it("for another computer: open this page there; the file can be moved by hand", () => {
    const b = modal("windows", "d4");
    expect([...b.querySelectorAll("ol.a-steps > .a-st .tx")].map((x) => x.firstChild?.textContent)).toEqual(["Откройте эту страницу на том компьютере", "Или перенесите файл сами"]);
    expect(text(b.querySelector("ol.a-steps .u-hint"))).toMatch(/^Проще всего так: на нужном компьютере/);
    expect([...b.querySelectorAll(".u-acts > *")].map(text)).toEqual(["Скачать файл"]);
  });

  it("the choice of where to add comes from the page's state, and a click on it goes to the page", () => {
    expect(modal("android", "d3", "open", (s) => (s.where.d3 = "other")).querySelector(".a-two .u-qr")).not.toBeNull();
    const log: string[] = [];
    const r = withKey({ a: actions({ amz: { ...amzNoop, where: (id, w) => log.push(`${id} ${w}`) } }) });
    r.box.querySelector<HTMLButtonElement>("[data-k=amz-where-other]")!.click();
    expect(log).toEqual(["d3 other"]);
  });

  it("the country choice: the names the app shows, one chip each, and it explains itself", () => {
    const fld = modal("android", "d3").querySelector(".a-b > .a-steps > .a-fld")!;
    expect(text(fld.querySelector(".a-lbl"))).toBe("Страна");
    expect([...fld.querySelectorAll(".a-chip")].map((x) => [text(x), x.getAttribute("aria-checked")])).toEqual([["Германия · Франкфурт", "true"], ["Финляндия", "false"]]);
    expect(text(fld.querySelector(".u-hint"))).toBe("Каждая страна — отдельное подключение. Добавьте одну; перестанет работать — добавьте другую.");
    const log: number[] = [];
    const r = withKey({ a: actions({ amz: { ...amzNoop, node: (_id, i) => log.push(i) } }) }, [cfg("de1"), cfg("fi1", "FI")]);
    r.box.querySelector<HTMLButtonElement>("[data-k=amz-node-d3-1]")!.click();
    expect(log).toEqual([1]);
    expect(modal("android", "d3", "open", (s) => (s.configs.d3 = [cfg("de1")])).querySelector("[id^=k-c-]")).toBeNull(); // one country: no choice
  });

  it("a key through an entry shares the node of the direct one: it is named as the app names it, says once that it is a new server, and is the connection to delete when renewed", () => {
    const via = awgConfig({ node_id: "de1", via_node_id: "nod_msk1", via_country: "ru", label: "Германия · Франкфурт · via RU", country_code: "DE", version: "3.1", conf: "[Interface]\nPrivateKey = y\n", vpn_key: "vpn://via", filename: "mistgate-de-via.conf", warnings: ["amnezia_desktop_mtu"] });
    expect([via.via_node_id, via.via_country]).toEqual(["nod_msk1", "RU"]);
    expect([cfg("de1").via_node_id, cfg("de1").via_country]).toEqual(["", ""]);
    expect(awgConfig({ node_id: "de1", via_country: "RU" }).via_country).toBe(""); // a country with no entry means nothing
    expect(nodeLabels([cfg("de1"), via])).toEqual(["🇩🇪 Германия · Франкфурт", "🇩🇪 Германия · Франкфурт · via RU"]);
    const open = (mode: "open" | "renew", pick: number) =>
      modal("android", "d3", mode, (s) => {
        s.configs.d3 = [cfg("de1"), via];
        s.node.d3 = pick;
      });
    const notes = (b: HTMLElement) => [...b.querySelectorAll(".a-meta p")].map(text);
    expect(notes(open("open", 0)).some((x) => x.startsWith("Это отдельный ключ"))).toBe(false);
    expect(notes(open("open", 1))).toContain("Это отдельный ключ для пути через RU: импортируйте его в AmneziaVPN как новый сервер.");
    expect([...open("open", 1).querySelectorAll(".a-steps > .a-fld .a-chip.on")].map(text)).toEqual(["Германия · Франкфурт · via RU"]);
    expect(text(open("renew", 1).querySelector("ol.a-steps > .a-st:last-child .tx"))).toContain("«Германия · Франкфурт · via RU»");
    expect(text(open("renew", 0).querySelector("ol.a-steps > .a-st:last-child .tx"))).toContain("«Германия»");
  });

  it("the new key of a stale device: what to add, and which old connection goes (by country, never by the panel's node)", () => {
    const b = modal("android", "d3", "renew", () => {}, "stale");
    expect(text(b.querySelector(".a-h .t"))).toBe("Новый ключ для Pixel 7");
    expect(text(b.querySelector(".a-f .u-hint"))).toBe("Старый ключ больше не работает");
    expect([...b.querySelectorAll("ol.a-steps > .a-st .tx")].map(text)).toEqual(["В AmneziaVPN нажмите «+», вставьте ключ и «Продолжить»", "Удалите старое подключение «Германия» — оно больше не подключается"]);
    expect(b.textContent).not.toMatch(/de1|fi1/);
    const pc = modal("windows", "d3", "renew", () => {}, "stale");
    expect([...pc.querySelectorAll(".a-meta p")].map(text)).toContain("Удалите старое подключение «Германия» — оно больше не подключается");
  });

  it("after Create the window shows the new device's key as the last step of the branch, with Back to the form", () => {
    const b = modal("android", "d3", "created", () => {}, "many");
    expect(text(b.querySelector(".a-h .n"))).toBe("3 из 3");
    expect(b.querySelector("form")).toBeNull();
    expect(b.querySelector("[data-k=add-back]")).not.toBeNull();
  });

  it("buttons copy and download what the key is", () => {
    const { box, a } = withKey();
    const [dl, cp] = [...box.querySelectorAll<HTMLButtonElement>(".a-two .u-acts button")];
    dl!.click();
    cp!.click();
    expect(a.log).toEqual(["download mistgate-de.conf", "copy vpn://k"]);
    box.querySelector<HTMLButtonElement>("[data-k=modal-done]")!.click();
    expect(a.log.at(-1)).toBe("add false");
  });

  it("five languages: the key reads the same way, none of the words missing", () => {
    for (const lang of ["ru", "en", "zh", "fa", "tr"] as const) {
      const { box } = withKey({ lang }, [cfg("de1"), cfg("fi1", "FI")]);
      expect(box.textContent, lang).not.toMatch(/undefined|\[object|NaN/);
      expect(box.querySelectorAll(".a-two .a-st"), lang).toHaveLength(3);
      expect(box.querySelectorAll(".a-meta p"), lang).toHaveLength(2);
    }
    expect(text(withKey({ lang: "fa" }).box.querySelector(".a-meta"))).toContain("کلید شخصی است");
  });

  it("a label is text, never markup", () => {
    const evil = structuredClone(cases.many!);
    evil.amnezia!.devices[0]!.label = "<img src=x onerror=alert(1)>";
    const el = view(normalize(evil), state("android", { returning: true }, "en"), actions());
    expect(el.querySelector("img[onerror]")).toBeNull();
    const { box } = dialog(normalize(evil), { amz: { open: "d3", configs: { d3: [cfg("de1")] } } });
    expect(box.querySelector("img[onerror]")).toBeNull();
    expect(text(box.querySelector(".a-h .t"))).toBe("Ключ для <img src=x onerror=alert(1)>");
  });
});

describe("the add-a-device dialog", () => {
  const form = (name = "many", amz: Partial<AmzState> = {}) => dialog(data(name), { amz: { pane: "key", ...amz } });

  it("the device (six choices), its name, and the version of the app only when there is a choice, with the countries of the chosen one", () => {
    const { box: b } = form();
    expect(text(b.querySelector(".a-h .t"))).toBe("Ключ AmneziaVPN");
    expect([...b.querySelectorAll(".a-chip")].map(text)).toEqual(["iPhone", "Android", "Windows", "Mac", "Linux", "Другое"]);
    expect(b.querySelector<HTMLElement>(".a-chip.on")?.textContent).toBe("iPhone");
    expect(b.querySelector<HTMLInputElement>("[data-k=amz-label]")?.placeholder).toBe("Например, телефон мамы");
    expect([...b.querySelectorAll(".a-ver .d")].map(text)).toEqual(["Обычный ключ, подходит почти всегда", "Если какой-то сайт не открывается", "Ключ для старых версий приложения"]);
    expect([...b.querySelectorAll(".a-incl li")].map(text)).toEqual(["Германия", "FIФинляндия"]);
    expect(b.querySelector("[data-k=modal-create]")?.textContent).toBe("Создать ключ");
    expect(form("both").box.querySelector(".a-vers")).toBeNull();
  });

  it("two profiles of one kind are told apart by their countries", () => {
    const d = data("many", (x) => x.amnezia!.profiles.push({ id: "p31b", name: "Second", version: "3.1", egress: "direct", countries: ["NL"] }));
    const { box } = dialog(d, { amz: { pane: "key" } });
    const main = [...box.querySelectorAll(".a-ver")].filter((x) => text(x.querySelector(".v")).startsWith("5.0.1.5"));
    expect(main.map((x) => text(x.querySelector(".d")))).toEqual(["Германия, Финляндия", "Нидерланды"]);
  });

  it("busy: 'One moment', and the button waits", () => {
    const btn = form("many", { busy: "add" }).box.querySelector<HTMLButtonElement>("[data-k=modal-create]")!;
    expect([text(btn), btn.disabled]).toEqual(["Секунду…", true]);
  });

  it("'all slots are used' is a step of its own with the way to write; any other error stays in the form", () => {
    const full = form("many", { error: "device_limit", errorAt: "add" });
    expect(full.stage).toBe("full");
    expect(text(full.box.querySelector(".a-f a"))).toBe("Написать в поддержку");
    const other = form("many", { error: "network", errorAt: "add" }).box;
    expect(other.querySelector(".a-err")?.getAttribute("role")).toBe("alert");
    expect(text(other.querySelector(".a-err"))).toBe("Нет связи. Проверьте интернет и повторите.");
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
