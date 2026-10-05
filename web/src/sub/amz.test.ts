import { describe, expect, it } from "vitest";
import { addModal, keyMode, newAmzState, type AmzState } from "./amnezia";
import { amzActions, type Api } from "./amz-actions";
import { ApiError, endpoint } from "./api";
import { cases } from "./dev-data";
import { awgConfig, awgDevice, canAddDevice, defaultLabel, mainProfile, nodeLabels, normalize, otherDevices, profileChoices, selfServe, versionsFor } from "./logic";
import { dict } from "./i18n";
import type { MgData } from "./types";
import { view, type Actions, type State } from "./view";

const t = dict.ru;
const cfg = (node: string, cc = "DE", server = ({ DE: "Германия", FI: "Финляндия" } as Record<string, string>)[cc] ?? cc) =>
  awgConfig({ node_id: node, server, country_code: cc, version: "3.1", conf: "[Interface]\nPrivateKey = x\n", vpn_key: "vpn://k", filename: `mistgate-${cc.toLowerCase()}.conf`, warnings: ["amnezia_desktop_mtu"] });

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
    ...over,
  };
}

function setup(name: keyof typeof cases, api: Api, f: (d: MgData) => void = () => {}) {
  const data = normalize(structuredClone(cases[name]!));
  f(data);
  const st = { lang: "ru" as const, amz: newAmzState("android", mainProfile(data.amnezia?.profiles ?? []), "android") };
  let draws = 0;
  const log: string[] = [];
  const a = amzActions({ data, st, render: () => draws++, api, reveal: (id) => log.push(`reveal ${id}`), focus: (k) => log.push(`focus ${k}`) });
  return { data, st, a, log, draws: () => draws };
}
const settle = () => new Promise((r) => setTimeout(r, 0));

const noop = () => {};
const pageActs = (amz: Actions["amz"]): Actions => ({ lang: noop, platform: noop, qrOpen: noop, closeAnn: noop, copy: noop, download: noop, amz });
const quiet = { add: noop, form: noop, create: noop, show: noop, renew: noop, node: noop, ask: noop, rotate: noop, remove: noop };
const pageState = (lang: "ru" | "en", f: (s: AmzState) => void = () => {}, here = "windows"): State => {
  const s: State = { lang, platform: "windows", qrOpen: false, annClosed: false, amz: newAmzState("windows", "p31", here) };
  f(s.amz);
  return s;
};
const keyWay = (el: HTMLElement) => el.querySelector<HTMLElement>("[data-way=key]")!;
const text = (el: Element | null | undefined) => el?.textContent ?? "";

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
});

describe("endpoint", () => {
  it("posts to the page's own origin, whatever host the data names", () => {
    const here = { origin: "https://mirror.example.net", href: "https://mirror.example.net/p/t" } as Location;
    expect(endpoint("https://sub.example.com/p/t/devices", "/dev_1/configs", here)).toBe("https://mirror.example.net/p/t/devices/dev_1/configs");
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
    expect(profileChoices(d.amnezia!.profiles, t, "ru", "AmneziaVPN")).toEqual([
      { id: "p31", label: "🇩🇪 Германия, 🇫🇮 Финляндия" },
      { id: "pw", label: "Запасной выход (если какой-то сайт не открывается)" },
      { id: "p20", label: "Для старых версий AmneziaVPN (до 5.0.1.5)" },
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
    expect(nodeLabels([cfg("de1"), cfg("fi1", "FI", "Финляндия")])).toEqual(["🇩🇪 Германия", "🇫🇮 Финляндия"]);
    const repeated = [cfg("de1", "DE", "Germany"), cfg("de2", "DE", "Germany 2"), cfg("x", "", "Server")];
    expect(nodeLabels(repeated, false)).toEqual(["Germany", "Germany 2", "Server"]);
    expect(nodeLabels(repeated).join(" ")).not.toMatch(/de1|de2/);
    // a config the old panel sent has a node_name and no server: the name is not shown
    expect(nodeLabels([awgConfig({ node_name: "de1", country_code: "" })], false)).toEqual(["1"]);
  });

  it("the app versions a key needs, for the device's platform only", () => {
    const all = [
      { client: "amnezia", app: "AmneziaVPN", min: "5.0.1.5" },
      { client: "amnezia", app: "AmneziaWG Android", min: "v3.1.20260814" },
      { client: "mihomo", app: "Mihomo", min: "v1.19.30" },
    ];
    expect(versionsFor("ios", all)).toEqual([{ client: "amnezia", app: "AmneziaVPN", min: "5.0.1.5" }]);
    expect(versionsFor("android", all).map((m) => `${m.app} ${m.min}`)).toEqual(["AmneziaVPN 5.0.1.5", "AmneziaWG 3.1.20260814"]);
    expect(t.needApp(versionsFor("android", all))).toBe("Нужна AmneziaVPN 5.0.1.5 или новее — или AmneziaWG 3.1.20260814 и новее");
  });
});

describe("actions", () => {
  it("add: the device appears with the name the page gave it, the count goes up, the dialog turns into its key", async () => {
    const api = fakeApi();
    const { data, st, a, log } = setup("many", api);
    const before = data.user.devices_used;
    a.add(true);
    expect(st.amz).toMatchObject({ adding: true });
    expect(st.amz.form.profile).toBe("p31"); // the main one, not the first in the list
    a.form({ platform: "windows", label: "" });
    a.create();
    expect(st.amz.busy).toBe("add");
    await settle();
    expect(api.calls).toEqual(["add p31 windows Windows 2"]); // "Windows" is taken by a device of the list
    expect(data.amnezia?.devices.at(-1)?.id).toBe("dev_new");
    expect(data.user.devices_used).toBe(before + 1);
    expect(st.amz).toMatchObject({ created: "dev_new", adding: true, open: null, busy: "", error: "" });
    a.add(false);
    expect(st.amz).toMatchObject({ created: null, adding: false });
    expect(log).toEqual(["reveal amz-row-dev_new"]);
    a.form({ label: "  Work PC " });
    a.create();
    await settle();
    expect(api.calls.at(-1)).toBe("add p31 windows Work PC");
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
    await settle();
    expect(api.calls).toEqual([]);
    expect(st.amz.open).toBeNull();
  });

  it("an expired password sends the page to the form instead of showing an error", async () => {
    let locked = 0;
    const data = normalize(structuredClone(cases.many!));
    const st = { lang: "ru" as const, amz: newAmzState("android", "p31") };
    const a = amzActions({ data, st, render: () => {}, api: fakeApi({ getConfigs: async () => Promise.reject(new ApiError(401, "locked")) }), reveal: () => {}, locked: () => locked++ });
    a.show("d3");
    await settle();
    expect(locked).toBe(1);
  });

  it("a refusal keeps the form and names the code where it happened", async () => {
    const api = fakeApi({ addDevice: async () => Promise.reject(new ApiError(409, "device_limit")) });
    const { data, st, a } = setup("many", api);
    const used = data.user.devices_used;
    a.add(true);
    a.create();
    await settle();
    expect(st.amz).toMatchObject({ error: "device_limit", errorAt: "add", busy: "", adding: true });
    expect(data.user.devices_used).toBe(used);
  });

  it("too many requests tells how many minutes", async () => {
    const { st, a } = setup("many", fakeApi({ getConfigs: async () => Promise.reject(new ApiError(429, "too_many_requests", 1800)) }));
    a.show("d3");
    await settle();
    expect(st.amz).toMatchObject({ error: "too_many_requests", errorAt: "d3", retryMin: 30, open: null });
  });

  it("show asks once and clears the outdated mark", async () => {
    const api = fakeApi();
    const { data, st, a } = setup("stale", api);
    expect(data.amnezia?.devices[0]?.stale).toBe(true);
    a.show("d3");
    await settle();
    expect(data.amnezia?.devices[0]?.stale).toBe(false);
    expect(st.amz.open).toBe("d3");
    a.show(null);
    a.show("d3");
    await settle();
    expect(api.calls).toEqual(["configs d3"]); // held in memory: no second write against the hourly budget
  });

  it("'new key needed': the key is fetched, then shown in the dialog; closing it scrolls to the device", async () => {
    const api = fakeApi();
    const { data, st, a, log } = setup("stale", api);
    a.renew("d3");
    expect(st.amz.busy).toBe("renew:d3");
    expect(st.amz.renew).toBeNull(); // no empty dialog while the key is on its way
    await settle();
    expect(api.calls).toEqual(["configs d3"]);
    expect(st.amz).toMatchObject({ renew: "d3", busy: "", adding: false });
    expect(data.amnezia?.devices[0]?.stale).toBe(false);
    a.add(false);
    expect(st.amz.renew).toBeNull();
    expect(log).toEqual(["reveal amz-row-d3"]);
  });

  it("'new key needed' that fails says so in its own card and opens nothing", async () => {
    const { st, a } = setup("stale", fakeApi({ getConfigs: async () => Promise.reject(new ApiError(409, "no_inbound")) }));
    a.renew("d3");
    await settle();
    expect(st.amz).toMatchObject({ renew: null, error: "no_inbound", errorAt: "renew:d3" });
    const el = view(normalize(cases.stale), { lang: "ru", platform: "android", qrOpen: false, annClosed: false, amz: st.amz }, pageActs(quiet));
    expect(text(el.querySelector(".stale .err"))).toBe("Этот вариант пока нигде не запущен — напишите в поддержку.");
    expect(el.querySelector("[data-way=key] .err")).toBeNull();
  });

  it("a question takes the keyboard to its safe answer, and cancelling gives it back", () => {
    const { a, log } = setup("many", fakeApi());
    a.ask({ id: "d4", kind: "remove" });
    a.ask(null);
    a.ask({ id: "d4", kind: "rotate" });
    a.ask(null);
    expect(log).toEqual(["focus amz-no-d4", "focus amz-del-d4", "focus amz-no-d4", "focus amz-rot-d4"]);
  });

  it("rotate replaces the key; remove drops the device and frees the slot", async () => {
    const api = fakeApi();
    const { data, st, a } = setup("many", api);
    a.show("d3");
    await settle();
    a.ask({ id: "d3", kind: "rotate" });
    a.rotate("d3");
    await settle();
    expect(st.amz.configs.d3).toHaveLength(1);
    expect(st.amz.confirm).toBeNull();
    const used = data.user.devices_used;
    a.remove("d4");
    await settle();
    expect(data.amnezia?.devices.map((x) => x.id)).not.toContain("d4");
    expect(data.devices.map((x) => x.id)).not.toContain("d4");
    expect(data.user.devices_used).toBe(used - 1);
  });

  it("a second click while a call is in flight does nothing", async () => {
    const api = fakeApi();
    const { a } = setup("many", api);
    a.show("d3");
    a.show("d4");
    await settle();
    expect(api.calls).toEqual(["configs d3"]);
  });
});

describe("the key way", () => {
  it("lists each key with what a friend needs: name, platform, when; no AWG, profile or address", () => {
    const el = keyWay(view(normalize(cases.many), pageState("ru"), pageActs(quiet)));
    const rows = [...el.querySelectorAll(".krow")];
    expect(rows.map((r) => text(r.querySelector(".kname")))).toEqual(["Pixel 7", "MacBook", "Windows"]);
    expect(rows.map((r) => text(r.querySelector(".meta")))).toEqual(["Android · в сети", "Mac · подключалось 12 дней назад", "ещё не подключалось"]);
    expect(el.textContent).not.toMatch(/AWG|Main|10\.66|fd66/);
    const acts = [...rows[0]!.querySelectorAll(".kact button")];
    expect(acts.map(text)).toEqual(["Показать ключ", "Заменить ключ", "Удалить"]);
    expect(acts.map((b) => (b as HTMLElement).dataset.k)).toEqual(["amz-show-d3", "amz-rot-d3", "amz-del-d3"]);
    expect(acts[2]!.classList.contains("bad")).toBe(true);
    expect(text(el.querySelector(".sub-h .cnt"))).toBe("занято 4 из 8");
  });

  it("the questions say what happens, with a red 'Remove' and a plain 'Cancel'", () => {
    const remove = keyWay(view(normalize(cases.many), pageState("ru", (s) => (s.confirm = { id: "d3", kind: "remove" })), pageActs(quiet)));
    expect(text(remove.querySelector(".ask p"))).toBe("Удалить «Pixel 7»? VPN на нём сразу перестанет работать.");
    expect([...remove.querySelectorAll(".ask button")].map((b) => [text(b), b.className])).toEqual([
      ["Отмена", "btn sec sm"],
      ["Удалить", "btn sm bad"],
    ]);
    const rotate = keyWay(view(normalize(cases.many), pageState("ru", (s) => (s.confirm = { id: "d3", kind: "rotate" })), pageActs(quiet)));
    expect(text(rotate.querySelector(".ask p"))).toBe("Заменить ключ «Pixel 7»? Старый перестанет работать сразу — новый нужно будет добавить в AmneziaVPN заново.");
    expect(text(rotate.querySelector(".ask .btn.pri"))).toBe("Заменить");
  });

  it("at the limit the button waits and one text says what to do", () => {
    const el = keyWay(view(normalize(cases.devices), pageState("ru"), pageActs(quiet)));
    expect(el.querySelector<HTMLButtonElement>("[data-k=amz-add]")?.disabled).toBe(true);
    expect(text(el.querySelector(".limit"))).toBe("Занято 3 из 3. Удалите устройство, которым больше не пользуетесь (кнопка «Удалить» в списке ниже), или напишите — добавим место.");
  });

  it("without self-service: rows without buttons, and the step says to ask, with the support button", () => {
    const el = keyWay(view(normalize(cases["amnezia-off"]), pageState("ru"), pageActs(quiet)));
    expect(el.querySelectorAll(".krow")).toHaveLength(3);
    expect(el.querySelector(".kact")).toBeNull();
    expect(el.querySelector("[data-k=amz-add]")).toBeNull();
    expect(text(el.querySelectorAll(".step-t")[1])).toBe("Попросите ключ");
    expect(text(el.querySelector(".step-d"))).toBe("Ключи выдаёт администратор — напишите, если нужен новый.");
    expect(el.querySelector("a.btn[href^='https://t.me/']")?.textContent).toBe("Написать");
  });

  it("the admin's preview (no address) shows the rows without buttons and does not say 'ask the admin'", () => {
    const d = normalize({ ...cases.many, amnezia: { ...cases.many!.amnezia, endpoints: "" } });
    const el = keyWay(view(d, pageState("ru"), pageActs(quiet)));
    expect(el.querySelector(".kact")).toBeNull();
    expect(el.querySelector<HTMLButtonElement>("[data-k=amz-add]")?.disabled).toBe(true);
    expect(el.textContent).not.toContain("Ключи выдаёт администратор");
  });

  it("no profile: the server is still being set up", () => {
    const el = keyWay(view(normalize(cases["amnezia-none"]), pageState("en"), pageActs(quiet)));
    expect(text(el.querySelector(".step-d"))).toBe("The server is still being set up — message the admin.");
  });

  it("'new key needed' names the device, has a button per stale device, and steps in a safe order", () => {
    const one = view(normalize(cases.stale), pageState("ru"), pageActs(quiet));
    expect(text(one.querySelector(".stale-t b"))).toBe("Нужен новый ключ для «Pixel 7»");
    expect([...one.querySelectorAll(".steps3 li")].map((li) => text(li.lastElementChild))).toEqual([
      "Нажмите «Получить новый ключ».",
      "Добавьте его в AmneziaVPN, как в первый раз.",
      "Удалите старое подключение этого устройства — оно больше не работает.",
    ]);
    expect(one.textContent).not.toContain("ниже");
    const two = normalize(cases.many);
    two.amnezia!.devices.forEach((x) => (x.stale = true));
    const el = view(two, pageState("ru"), pageActs(quiet));
    expect(text(el.querySelector(".stale-t b"))).toBe("Нужны новые ключи");
    expect([...el.querySelectorAll(".stale-act button")].map(text)).toEqual(["Новый ключ для «Pixel 7»", "Новый ключ для «MacBook»", "Новый ключ для «Windows»"]);
  });
});

describe("the key itself", () => {
  const open = (here: string, device = "d3") =>
    keyWay(
      view(
        normalize(cases.many),
        pageState(
          "ru",
          (s) => {
            s.open = device;
            s.configs[device] = [cfg("de1"), cfg("fi1", "FI")];
          },
          here,
        ),
        pageActs(quiet),
      ),
    ).querySelector(`#amz-row-${device} .cfg`)!;

  it("on this very phone the key is copied: the copy button leads, then three steps; the QR code is folded away", () => {
    expect(keyMode("android", "android")).toBe("phone");
    const c = open("android");
    expect(text(c.querySelector(".btn.pri"))).toBe("Скопировать ключ");
    expect([...c.querySelectorAll(".ksteps li")].map(text)).toEqual(["Откройте AmneziaVPN", "Нажмите «+» и вставьте ключ", "Нажмите «Продолжить»"]);
    expect(c.querySelector("details.qrx svg")).not.toBeNull();
    expect(text(c.querySelector("details.qrx summary"))).toContain("QR‑код для другого устройства");
    expect(text(c.querySelector(".vers"))).toContain("AmneziaWG 3.1.20260814"); // Android: its own app too
  });

  it("on this very computer the file is downloaded, and the page says why not the key", () => {
    const c = open("macos", "d4");
    expect(text(c.querySelector(".btn.pri"))).toBe("Скачать файл");
    expect(text(c.querySelector(".how"))).toBe("AmneziaVPN → «+» → «Файл с настройками подключения» → выберите скачанный файл");
    expect(text(c.querySelector(".hintbox"))).toBe("На компьютере добавляйте в AmneziaVPN файл, а не ключ: с ключом соединение может не заработать.");
    expect(text(c.querySelector(".vers"))).toBe("Нужна AmneziaVPN 5.0.1.5 или новее");
  });

  it("for another phone the QR code comes first, with what to press in the app", () => {
    const c = open("windows");
    expect(c.querySelector(".cfg-main .qr.big svg")).not.toBeNull();
    expect(text(c.querySelector(".cfg-side .mut"))).toBe("В AmneziaVPN: «+» → «QR-код»");
    expect(c.querySelector(".btn.pri")).toBeNull(); // nothing on this device to press first
  });

  it("the country choice explains itself", () => {
    const c = open("windows");
    expect([...c.querySelectorAll("select option")].map(text)).toEqual(["Германия", "Финляндия"]); // Windows draws no flags
    expect(text(c.querySelector(".fld .hint"))).toBe("Каждая страна — отдельное подключение. Добавьте одну; перестанет работать — добавьте другую.");
    expect([...open("android").querySelectorAll("select option")].map(text)).toEqual(["🇩🇪 Германия", "🇫🇮 Финляндия"]);
  });

  it("the dialog's form: the connection option only when there is a choice, named by countries; the device; its name", () => {
    const d = normalize(cases.many);
    const s = { lang: "ru" as const, amz: newAmzState("ios", "p31", "ios") };
    s.amz.adding = true;
    const box = document.createElement("div");
    box.append(...(addModal({ d, s, a: pageActs(quiet), t }) as Node[]));
    expect([...box.querySelectorAll(".addf .eyebrow")].map(text)).toEqual(["ВАРИАНТ ПОДКЛЮЧЕНИЯ", "ЧТО ЗА УСТРОЙСТВО", "НАЗВАНИЕ (НЕОБЯЗАТЕЛЬНО)"]);
    expect(text(box.querySelector("[data-k=amz-profile] option"))).toBe("🇩🇪 Германия, 🇫🇮 Финляндия");
    expect(box.querySelector<HTMLInputElement>("[data-k=amz-label]")?.placeholder).toBe("Например, телефон мамы");
    const single = normalize(cases.both);
    const one = document.createElement("div");
    one.append(...(addModal({ d: single, s, a: pageActs(quiet), t }) as Node[]));
    expect(one.querySelector("[data-k=amz-profile]")).toBeNull();
  });

  it("the new key of a stale device: what to add, and that the old connection goes (never named by the panel's node)", () => {
    const d = normalize(cases.stale);
    const s = { lang: "ru" as const, amz: newAmzState("android", "p31", "android") };
    s.amz.renew = "d3";
    s.amz.configs.d3 = [cfg("de1"), cfg("fi1", "FI")];
    const box = document.createElement("div");
    box.append(...(addModal({ d, s, a: pageActs(quiet), t }) as Node[]));
    expect(text(box.querySelector(".mt"))).toBe("Новый ключ для «Pixel 7»");
    expect(text(box.querySelector(".hintbox.calm")).replace(/\s/g, " ")).toBe("Старое подключение этого устройства в AmneziaVPN удалите — оно больше не работает.");
    expect(box.textContent).not.toMatch(/de1|fi1/);
    expect(text(box.querySelector(".cfg .btn.pri"))).toBe("Скопировать ключ");
    expect(box.querySelector(".cfg [data-autofocus]")).toBe(box.querySelector(".cfg .btn.pri"));
  });

  it("a label is text, never markup", () => {
    const evil = structuredClone(cases.many!);
    evil.amnezia!.devices[0]!.label = "<img src=x onerror=alert(1)>";
    const el = view(normalize(evil), pageState("en"), pageActs(quiet));
    expect(el.querySelector("img[onerror]")).toBeNull();
    expect(text(el.querySelector("[data-way=key] .kname"))).toBe("<img src=x onerror=alert(1)>");
  });
});
