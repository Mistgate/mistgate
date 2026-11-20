import { describe, expect, it } from "vitest";
import { deviceModalStage } from "./add";
import { newAmzState } from "./amnezia";
import { amzActions, type Api } from "./amz-actions";
import { cases, sampleQr } from "./dev-data";
import { dict } from "./i18n";
import { awgConfig, defaultPane, keyAvailability } from "./logic";
import type { AddPane } from "./state";
import { actions, amzNoop, data, dialog, plain, state, text } from "./test-kit";
import type { Lang, MgData } from "./types";
import { view } from "./view";

// "New device": the dialog of the board in steps (way, app, conn, key, ready, full), and what each state of the person does to them.

const noApi = {} as Api;
const keys = (el: Element) => [...el.querySelectorAll("[data-k]")].map((x) => x.getAttribute("data-k")!);
const dupes = (list: string[]) => list.filter((k, i) => list.indexOf(k) !== i);
const cfg = (cc = "DE") => awgConfig({ node_id: `n-${cc}`, label: cc === "DE" ? "Германия" : "Нидерланды", country_code: cc, version: "3.1", conf: "[Interface]\nPrivateKey = x\n", vpn_key: "vpn://k", filename: "x.conf", warnings: [], qr: sampleQr });
const at = (pane: AddPane) => ({ pane });
/** The class of every element of a step, in order: what the board's step has too. */
const shape = (el: Element) => [...el.querySelectorAll("*")].filter((x) => !x.closest("svg")).map((x) => x.className).filter(Boolean);
const lang = ["ru", "en", "zh", "fa", "tr"] as const;

describe("where the window opens", () => {
  it("with both ways it asks which; with one it goes straight to that way", () => {
    expect(defaultPane(data("many"))).toBe("pick");
    expect(defaultPane(data("happ"))).toBe("link");
    expect(defaultPane(data("keys-only"))).toBe("key");
    expect(defaultPane(data("noapps"))).toBe("key"); // nothing to choose: the old form
  });

  it("the actions: open at the default pane or at the one asked, move between panes, closing is one thing for every pane", () => {
    const d = data("many");
    const st = { lang: "ru" as const, amz: newAmzState("ios", "p31", "ios") };
    let draws = 0;
    const a = amzActions({ data: d, st, render: () => draws++, api: noApi, reveal: () => {} });
    a.add(true);
    expect(st.amz).toMatchObject({ adding: true, pane: "pick" });
    for (const p of ["link", "conn", "link", "key", "pick"] as const) {
      a.pane(p);
      expect(st.amz.pane).toBe(p);
    }
    st.amz.error = "network";
    a.pane("key");
    expect(st.amz).toMatchObject({ pane: "key", error: "" });
    st.amz.created = "d3"; // "Back" from the new key: the device stays in the list, the form is there again
    a.pane("key");
    expect(st.amz.created).toBeNull();
    a.add(false);
    expect(st.amz.adding).toBe(false);
    a.add(true, "key");
    expect(st.amz).toMatchObject({ adding: true, pane: "key" });
    a.add(false);
    const only = { lang: "ru" as const, amz: newAmzState("ios", "p31", "ios") };
    amzActions({ data: data("happ"), st: only, render: () => {}, api: noApi, reveal: () => {} }).add(true);
    expect(only.amz.pane).toBe("link");
    expect(draws).toBeGreaterThan(0);
  });
});

describe("the frame of every step", () => {
  it("one section: the head (Back or a gap, the title that takes the focus, the count, Close), the body, the foot", () => {
    const { box, label } = dialog(data("many"), { platform: "windows", amz: at("link") });
    expect(box.children).toHaveLength(1);
    const dlg = box.firstElementChild!;
    expect(dlg.tagName).toBe("SECTION");
    expect([dlg.getAttribute("role"), dlg.getAttribute("aria-modal"), dlg.getAttribute("aria-label")]).toEqual(["dialog", "true", "Через приложение"]);
    expect(label).toBe("Через приложение");
    const head = dlg.querySelector(".a-h")!;
    expect(head.querySelector(".t")?.tagName).toBe("H2");
    expect(head.querySelector(".t")?.getAttribute("tabindex")).toBe("-1");
    expect(head.querySelector(".t")?.hasAttribute("data-autofocus")).toBe(true);
    expect(text(head.querySelector(".n"))).toBe("2 из 3");
    expect(head.querySelector("[data-k=add-back]")?.getAttribute("aria-label")).toBe("Назад");
    expect(head.querySelector("[data-k=modal-x]")?.getAttribute("aria-label")).toBe("Закрыть");
    const first = dialog(data("many"), { amz: at("pick") }).box.querySelector(".a-h")!;
    expect(first.querySelector("[data-k=add-back]")).toBeNull(); // nothing to go back to
    expect(first.querySelector("[data-k=modal-x]")).not.toBeNull();
  });

  it("Close closes the window; Back and Next go to the pane the board names", () => {
    const a = actions();
    const d = data("many");
    dialog(d, { a, amz: at("pick") }).box.querySelector<HTMLButtonElement>("[data-k=modal-x]")!.click();
    dialog(d, { a, amz: at("link") }).box.querySelector<HTMLButtonElement>("[data-k=add-back]")!.click();
    dialog(d, { a, amz: at("link") }).box.querySelector<HTMLButtonElement>("[data-k=add-next]")!.click();
    dialog(d, { a, amz: at("conn") }).box.querySelector<HTMLButtonElement>("[data-k=add-back]")!.click();
    dialog(d, { a, amz: at("key") }).box.querySelector<HTMLButtonElement>("[data-k=add-back]")!.click();
    dialog(d, { a, amz: at("conn") }).box.querySelector<HTMLButtonElement>("[data-k=add-done]")!.click();
    expect(a.log).toEqual(["add false", "pane pick", "pane conn", "pane link", "pane pick", "add false"]);
  });

  it("the count of a branch: of three with the choice of the way, of two without it (the choice is skipped)", () => {
    const both = data("many");
    const line = (d: MgData, pane: AddPane, amz: object = {}) => {
      const r = dialog(d, { platform: "windows", amz: { pane, ...amz } });
      return `${r.stage} ${text(r.box.querySelector(".a-h .n"))} ${text(r.box.querySelector(".a-h .t"))}`.trim();
    };
    expect(line(both, "pick")).toBe("way 1 из 3 Новое устройство");
    expect(line(both, "link")).toBe("app 2 из 3 Через приложение");
    expect(line(both, "conn")).toBe("conn 3 из 3 Подключите kl!ck");
    expect(line(both, "key")).toBe("key 2 из 3 Ключ AmneziaVPN");
    const happ = data("happ");
    expect(line(happ, "link")).toBe("app 1 из 2 Через приложение");
    expect(line(happ, "conn")).toBe("conn 2 из 2 Подключите kl!ck");
    expect(line(data("keys-only"), "key")).toBe("key 1 из 2 Ключ AmneziaVPN");
    expect(text(dialog(data("keys-only"), { amz: { created: "d3", configs: { d3: [cfg()] } } }).box.querySelector(".a-h .n"))).toBe("2 из 2");
  });

  it("the key of the step changes with the step and with the key shown, not with a choice made inside one", () => {
    const d = data("devices-rich");
    const stage = (amz: object, platform: "ios" | "windows" = "ios") => dialog(d, { platform, amz }).stage;
    expect([stage(at("pick")), stage(at("link")), stage(at("conn")), stage(at("key"))]).toEqual(["way", "app", "conn", "key"]);
    expect(stage(at("link"), "windows")).toBe(stage(at("link"))); // another platform chosen in the same step
    expect(stage({ pane: "key", form: { profile: "p31", platform: "android", label: "x", list: false } })).toBe("key");
    const keyOf = (id: string, mode: "open" | "renew" | "created") => stage({ [mode]: id, configs: { [id]: [cfg()] } });
    expect(keyOf("d3", "open")).toBe("ready:d3:shown");
    expect(keyOf("d7", "open")).toBe("ready:d7:shown");
    expect(keyOf("d3", "renew")).toBe("ready:d3:renew");
    expect(keyOf("d3", "created")).toBe("ready:d3:created");
    expect(deviceModalStage(dialog(data("devices-full"), { amz: at("pick") }).c)).toBe("way");
  });

  it("a pane that cannot be gone on (no key way, no app for the device) is the step of the way", () => {
    expect(dialog(data("happ"), { amz: at("key") }).stage).toBe("way"); // no AmneziaVPN
    expect(dialog(data("amnezia-off"), { amz: at("key") }).stage).toBe("way"); // the owner issues the keys
    expect(dialog(data("keys-only"), { amz: at("link") }).stage).toBe("way");
    expect(dialog(data("nolinux"), { platform: "linux", amz: at("conn") }).stage).toBe("app"); // no app on Linux: nothing to connect, the step of the app says so
  });
});

describe("step way: how the device connects", () => {
  it("the places, and two cards of the board: with an app (recommended, first) and with a key", () => {
    const { box, label, stage } = dialog(data("devices-rich"));
    expect([label, stage]).toEqual(["Новое устройство", "way"]);
    const slots = box.querySelector(".a-slots")!;
    expect(text(slots.children[0])).toBe("Устройство займёт одно место");
    expect(text(slots.querySelector(".r"))).toBe("свободно 5 из 10");
    expect(slots.querySelector(".u-prog")?.getAttribute("aria-label")).toBe("Занято 5 из 10 мест");
    expect(slots.querySelector<HTMLElement>(".u-prog i")?.style.width).toBe("50%");
    const [link, key] = [...box.querySelectorAll<HTMLButtonElement>(".a-ways > button.a-way")];
    expect([link!.className, key!.className]).toEqual(["a-way rec", "a-way"]);
    expect([link!.dataset.k, key!.dataset.k]).toEqual(["add-way-link", "add-way-key"]);
    expect(text(link!.querySelector(".top .tag"))).toBe("Рекомендуем");
    expect(text(link!.querySelector(".t"))).toBe("Через приложение");
    expect(text(link!.querySelector(".d"))).toBe("Одна ссылка в Happ");
    expect([...link!.querySelectorAll(".pts > span")].map(text)).toEqual(["Все серверы сразу", "Обновляется сама", "Подключается в одно нажатие"]);
    expect(text(link!.querySelector(".go"))).toBe("Выбрать");
    expect(text(key!.querySelector(".t"))).toBe("Ключом AmneziaVPN");
    expect(text(key!.querySelector(".d"))).toBe("Для роутера и приложения AmneziaVPN");
    expect([...key!.querySelectorAll(".pts > span")].map(text)).toEqual(["Отдельный ключ на каждое устройство", "Файл, QR-код или текст ключа", "Есть ключ и для старых версий"]);
    expect(key!.querySelector(".top .tag")).toBeNull();
    expect(key!.querySelector(".a-app.mint svg")).not.toBeNull();
  });

  it("the tiles of the apps: the instance's logo for its own app, the letters of the name in sky, lav, mint for the others", () => {
    const win = dialog(data("first"), { platform: "windows" }).box;
    expect([...win.querySelectorAll("[data-k=add-way-link] .a-app")].map((x) => `${x.className} ${text(x)}`)).toEqual(["a-app sky k!", "a-app lav H"]);
    expect(text(win.querySelector("[data-k=add-way-link] .d"))).toBe("Одна ссылка в kl!ck или Happ"); // the names come from the settings
    const host = dialog(data("first", (d) => (d.apps.find((x) => x.platform === "windows" && x.name === "kl!ck")!.name = "Mistgate")), { platform: "windows" }).box;
    const tiles = [...host.querySelectorAll("[data-k=add-way-link] .a-app")];
    expect(tiles.map((x) => x.tagName)).toEqual(["IMG", "SPAN"]);
    expect((tiles[0] as HTMLImageElement).src).toMatch(/^data:image\/svg\+xml/);
    expect(tiles[1]!.className).toBe("a-app sky"); // the logo is not one of the three tones
    expect(text(host.querySelector("[data-k=add-way-link] .d"))).toBe("Одна ссылка в Mistgate или Happ");
  });

  it("places: bar and words with a limit (full when none is left), only the line without one", () => {
    const limited = dialog(data("first", (d) => ((d.user.device_limit = 2), (d.user.devices_used = 1)))).box;
    expect(text(limited.querySelector(".a-slots .r"))).toBe("свободно 1 из 2");
    expect(limited.querySelector<HTMLElement>(".a-slots .u-prog i")?.style.width).toBe("50%");
    const unlimited = dialog(data("first", (d) => (d.user.device_limit = 0))).box;
    expect(text(unlimited.querySelector(".a-slots"))).toBe("Устройство займёт одно место");
    expect(unlimited.querySelector(".a-slots .u-prog")).toBeNull();
    const none = dialog(data("devices-full")).box;
    expect(none.querySelector(".a-slots .u-prog i")?.className).toBe("full");
    expect(text(none.querySelector(".a-slots .r"))).toBe("свободно 0 из 5");
  });

  it("a card tells the page which way was taken", () => {
    const a = actions();
    const { box } = dialog(data("many"), { a });
    box.querySelector<HTMLButtonElement>("[data-k=add-way-link]")!.click();
    box.querySelector<HTMLButtonElement>("[data-k=add-way-key]")!.click();
    expect(a.log).toEqual(["pane link", "pane key"]);
  });

  it("every place taken but the link still connects: only the key way waits (a quiet card with the reason and 'Write')", () => {
    const { box, stage } = dialog(data("devices-full"));
    expect(stage).toBe("way");
    expect(box.querySelector("[data-k=add-way-link]")).not.toBeNull();
    expect(box.querySelector("[data-k=add-way-key]")).toBeNull();
    const off = box.querySelector(".a-way.off")!;
    expect(off.tagName).toBe("DIV");
    expect(text(off.querySelector(".t"))).toBe("Ключом AmneziaVPN");
    expect(text(off.querySelector(".d"))).toBe("Занято 5 из 5. Удалите устройство, которым больше не пользуетесь, или напишите — добавим место.");
    expect(off.querySelector("a.a-lnk")?.getAttribute("href")).toBe("https://t.me/example_support");
    expect(off.querySelector("a")?.getAttribute("rel")).toBe("noopener noreferrer");
    expect(off.querySelector(".pts")).toBeNull();
    expect(keyAvailability(data("devices-full"))).toBe("limit");
  });

  it("the owner issues the keys, no profile, the preview: the key way says why; without support there is no 'Write'", () => {
    const off = (name: string, support?: string) => dialog(data(name), { support }).box.querySelector(".a-way.off")!;
    expect(text(off("amnezia-off").querySelector(".d"))).toBe("Ключи AmneziaVPN выдаёт владелец — напишите, для какого устройства нужен.");
    expect(text(off("amnezia-none").querySelector(".d"))).toBe("Сервер ещё настраивается — напишите администратору.");
    const preview = off("preview");
    expect(text(preview.querySelector(".d"))).toBe("В предпросмотре ключи не создаются.");
    expect(preview.querySelector("a")).toBeNull();
    expect(off("amnezia-off", "").querySelector("a")).toBeNull();
    expect(["amnezia-off", "amnezia-none", "preview"].map((n) => keyAvailability(data(n)))).toEqual(["admin", "noprofile", "preview"]);
  });

  it("five languages: the same shape, the words of the language, none of them missing", () => {
    const ru = shape(dialog(data("devices-rich")).box);
    const title = { ru: "Новое устройство", en: "New device", zh: "新设备", fa: "دستگاه جدید", tr: "Yeni cihaz" };
    for (const l of lang) {
      const { box } = dialog(data("devices-rich"), { lang: l });
      expect(shape(box), l).toEqual(ru);
      expect(text(box.querySelector(".a-h .t")), l).toBe(plain(title[l]));
      expect(box.textContent, l).not.toMatch(/undefined|\[object|NaN/);
    }
    expect(text(dialog(data("devices-rich"), { lang: "en" }).box.querySelector("[data-k=add-way-key] .t"))).toBe("With a key");
    expect(text(dialog(data("devices-rich"), { lang: "en" }).box.querySelector(".a-slots .r"))).toBe("5 of 10 free");
  });
});

describe("step app: the device and the app", () => {
  it("the five devices as chips (the browser's guess marked), then the apps: the recommended one first, each with its tile", () => {
    const { box } = dialog(data("devices-rich"), { platform: "windows", amz: at("link") });
    expect(text(box.querySelector(".a-lbl"))).toBe("Устройствоопределили по браузеру");
    const seg = box.querySelector(".a-seg")!;
    expect(seg.getAttribute("role")).toBe("radiogroup");
    expect([...seg.querySelectorAll(".a-chip")].map(text)).toEqual(["iPhone", "Android", "Windows", "Mac", "Linux"]);
    expect([...seg.querySelectorAll(".a-chip")].map((x) => x.getAttribute("aria-checked"))).toEqual(["false", "false", "true", "false", "false"]);
    expect(seg.querySelector(".a-chip.on")?.getAttribute("role")).toBe("radio");
    expect(seg.querySelector(".a-chip svg")).not.toBeNull();
    const opts = [...box.querySelectorAll(".a-opts > button.a-opt")];
    expect(opts.map((x) => text(x.querySelector(".t")))).toEqual(["kl!ckРекомендуем", "Happ"]); // AmneziaVPN is the other way
    expect(opts.map((x) => x.getAttribute("aria-checked"))).toEqual(["true", "false"]);
    expect(opts[0]!.querySelector(".tag")?.textContent).toBe("Рекомендуем");
    expect(text(opts[0]!.querySelector(".d"))).toBe("Все ваши серверы в одном приложении, подписка обновляется сама"); // the settings' own words
    expect(text(opts[1]!.querySelector(".d"))).toBe("Одна личная ссылка со всеми вашими серверами — обновляется сама"); // an app without a description
    expect(box.querySelector(".a-opts")?.getAttribute("aria-labelledby")).toBe("a-app");
    expect(text(box.querySelector("#a-app"))).toBe("Приложение");
  });

  it("the choices go to the page as they do elsewhere; the chosen app is not a click", () => {
    const a = actions();
    const { box } = dialog(data("devices-rich"), { platform: "windows", amz: at("link"), a });
    box.querySelector<HTMLButtonElement>("[data-k=add-plat-android]")!.click();
    box.querySelector<HTMLButtonElement>("[data-k='add-app-happ|Happ']")!.click();
    box.querySelector<HTMLButtonElement>("[data-k='add-app-happ|kl!ck']")!.click();
    expect(a.log).toEqual(["platform android", "app happ|Happ"]);
  });

  it("a platform that is not the browser's: no guess marked, the way back to it", () => {
    const a = actions();
    const { box } = dialog(data("devices-rich"), { platform: "windows", amz: at("link"), a, over: { detected: "ios", platform: "windows" } });
    expect(text(box.querySelector(".a-lbl"))).toBe("Устройство");
    const hint = box.querySelector(".u-hint")!;
    expect(text(hint)).toBe("Это не ваш телефон? Вернуть iPhone");
    hint.querySelector<HTMLButtonElement>("[data-k=add-plat-back]")!.click();
    expect(a.log).toEqual(["platform ios"]);
    expect(dialog(data("devices-rich"), { platform: "windows", amz: at("link"), over: { detected: null } }).box.querySelector("[data-k=add-plat-back]")).toBeNull();
  });

  it("a platform without apps still has the link to copy and the way to write; its foot says Done, not Next", () => {
    const d = data("nolinux", (x) => ((x.amnezia = cases.both!.amnezia), (x.access.amnezia = true)));
    const a = actions();
    const { box } = dialog(d, { platform: "linux", amz: at("link"), a });
    expect(box.querySelector(".a-opts")).toBeNull();
    expect(text(box.querySelector(".a-nil .t"))).toBe("Для Linux приложений пока нет");
    box.querySelector<HTMLButtonElement>("[data-k=add-link-copy-plain]")!.click();
    expect(a.log).toEqual([`copy ${d.subscription_url}`, "mark"]);
    expect(box.querySelector(".a-nil a")?.getAttribute("href")).toBe("https://t.me/example_support");
    expect(box.querySelector("[data-k=add-next]")).toBeNull();
    box.querySelector<HTMLButtonElement>("[data-k=add-done]")!.click();
    expect(a.log.at(-1)).toBe("add false");
  });

  it("the foot of the step has the one button: Next", () => {
    const foot = dialog(data("devices-rich"), { amz: at("link") }).box.querySelector(".a-f")!;
    expect([...foot.children].map((x) => `${x.tagName} ${text(x)}`)).toEqual(["BUTTON Дальше"]);
  });
});

describe("step conn: connect", () => {
  const conn = (name: string, platform: "ios" | "windows" | "linux" = "windows", f: (d: MgData) => void = () => {}, a = actions()) => dialog(data(name, f), { platform, amz: at("conn"), a });

  it("install and add the subscription; the QR code of this page beside them (a phone has it hidden by the stylesheet)", () => {
    const { box, label } = conn("devices-rich");
    expect(label).toBe("Подключите kl!ck");
    expect(box.querySelector(".a-b > .a-two")).not.toBeNull();
    const steps = [...box.querySelectorAll(".a-steps > .a-st")];
    expect(steps.map((x) => text(x.querySelector(".a-n")))).toEqual(["1", "2"]);
    expect(steps.map((x) => x.querySelector(".tx")?.firstChild?.textContent)).toEqual(["Установите kl!ck", "Добавьте подписку"]);
    const get = box.querySelector("a[data-k=add-link-get]")!;
    expect(text(get)).toBe("Скачать с сайта");
    expect(get.getAttribute("href")).toMatch(/^https:\/\/github\.com\//);
    expect(get.getAttribute("rel")).toBe("noopener noreferrer");
    expect(text(steps[1]!.querySelector(".u-hint"))).toBe("kl!ck откроется уже с вашей подпиской");
    const one = steps[1]!.querySelector<HTMLAnchorElement>("a.u-btn.pri")!;
    expect(text(one)).toBe("Добавить одним нажатием");
    expect(one.href).toMatch(/^klick:\/\/add\?url=/);
    expect(text(steps[1]!.querySelector(".a-alt"))).toBe("Не открывается?Скопировать ссылкуСкопировано");
    expect(steps[1]!.querySelector(".a-alt button.a-lnk.u-cpb")).not.toBeNull();
    const side = box.querySelector(".a-two > .a-side.pg")!;
    expect(side.querySelector(".u-qr svg")).not.toBeNull();
    expect(side.querySelector(".u-qr .u-qt img")).not.toBeNull(); // the logo in the middle
    expect(text(side.querySelector(".u-hint"))).toBe("Если это телефон, наведите на код камеру");
    expect(side.querySelector("svg")?.getAttribute("aria-label")).toBe("QR-код со ссылкой на эту страницу");
    expect(box.textContent).not.toContain(data("devices-rich").subscription_url); // the link is copied and scanned, never written out
    const foot = box.querySelector(".a-f")!;
    expect(text(foot)).toBe("Устройство появится в списке после первого обновленияГотово");
  });

  it("the buttons do what they say: 'Add' marks the device, 'Copy' copies the link and marks, 'Done' closes", () => {
    const a = actions();
    const { box, c } = conn("devices-rich", "windows", () => {}, a);
    box.querySelector<HTMLButtonElement>("[data-k=add-link-copy]")!.click();
    expect(a.log).toEqual([`copy ${c.d.subscription_url}`, "mark"]);
    expect(box.querySelector("[data-k=add-link-copy]")?.getAttribute("data-cp")).toBe("done");
    box.querySelector<HTMLAnchorElement>("[data-k=add-link-add]")!.addEventListener("click", (e) => e.preventDefault());
    box.querySelector<HTMLAnchorElement>("[data-k=add-link-add]")!.click();
    box.querySelector<HTMLButtonElement>("[data-k=add-done]")!.click();
    expect(a.log.slice(2)).toEqual(["mark", "add false"]);
  });

  it("the phone's apps come from a store: the word says which; without a download there is no such step", () => {
    expect(text(conn("devices-rich", "ios").box.querySelector("a[data-k=add-link-get]"))).toBe("Скачать в App Store");
    expect(conn("devices-rich", "ios").label).toBe("Подключите Happ");
    const play = dialog(data("devices-rich"), { platform: "android", amz: at("conn") }).box;
    expect(text(play.querySelector("a[data-k=add-link-get]"))).toBe("Скачать в Google Play");
    const bare = conn("devices-rich", "windows", (d) => d.apps.forEach((x) => (x.download_url = ""))).box;
    expect(bare.querySelector("[data-k=add-link-get]")).toBeNull();
    expect([...bare.querySelectorAll(".a-st .a-n")].map(text)).toEqual(["1"]); // the numbers follow the steps there are
  });

  it("an app that has no add address: copy the link, paste it in the app", () => {
    const { box } = dialog(data("multi"), { platform: "windows", amz: at("conn"), over: { app: "happ|Example Client" } });
    expect(text(box.querySelector(".a-steps .a-st:last-child .tx"))).toContain("Скопируйте ссылку и вставьте её в приложение");
    expect(box.querySelector("a[data-k=add-link-add]")).toBeNull();
    expect(box.querySelector("button.u-btn.u-cpb")).not.toBeNull();
    expect(text(box.querySelector(".a-st:last-child .u-hint"))).toBe("В Example Client: добавить подписку → вставить ссылку");
  });

  it("no QR option: no side, the steps stand alone; a hostile link never becomes a link", () => {
    const { box } = conn("devices-rich", "windows", (d) => ((d.options.show_qr = false), (d.apps[2]!.add_url = "javascript:alert(1)")));
    expect(box.querySelector(".a-two, .a-side, .u-qr")).toBeNull();
    expect(box.querySelector(".a-b > ol.a-steps")).not.toBeNull();
    expect(box.querySelector("a[href^=javascript]")).toBeNull();
  });
});

describe("step key: the form of a new device", () => {
  const form = (name = "many", amz: object = {}, platform: "ios" | "windows" = "ios") => dialog(data(name), { platform, amz: { pane: "key", ...amz } });

  it("what device, its name, the version of the app, the key to make; the foot makes the key", () => {
    const { box, label } = form();
    expect(label).toBe("Ключ AmneziaVPN");
    const f = box.querySelector("form.a-b")!;
    expect(text(f.children[0]!.querySelector(".a-lbl"))).toBe("Что за устройство");
    const chips = [...f.querySelectorAll(".a-seg .a-chip")];
    expect(chips.map(text)).toEqual(["iPhone", "Android", "Windows", "Mac", "Linux", "Другое"]);
    expect(chips.map((x) => x.getAttribute("aria-checked"))).toEqual(["true", "false", "false", "false", "false", "false"]);
    expect(chips.map((x) => x.getAttribute("data-k"))).toEqual(["amz-platform-ios", "amz-platform-android", "amz-platform-windows", "amz-platform-macos", "amz-platform-linux", "amz-platform-other"]);
    const name = f.querySelector<HTMLInputElement>("input.a-inp")!;
    expect([name.maxLength, name.id, name.placeholder, name.getAttribute("dir")]).toEqual([40, "a-nm", "Например, телефон мамы", "auto"]);
    expect(text(f.querySelector("label.a-lbl[for=a-nm]"))).toBe("Названиенеобязательно");
    // two versions: the new one first, as the page orders them; the spare exit is a key of the new version, not a version
    const vers = [...f.querySelectorAll(".a-vers > button.a-ver")];
    expect(vers.map((x) => x.className)).toEqual(["a-ver on", "a-ver"]);
    expect(vers.map((x) => x.getAttribute("role"))).toEqual(["radio", "radio"]);
    expect(text(f.querySelector("#nd-v"))).toBe("Версия AmneziaVPN на устройствеуказана в настройках приложения");
    expect(box.querySelector(".a-f button.u-btn.pri[data-k=modal-create]")).not.toBeNull();
    expect(text(box.querySelector(".a-f"))).toBe("Создать ключ");
  });

  it("the window of the key step is the frame that scrolls inside (.key), no other step is", () => {
    const frame = (amz: object) => dialog(data("many"), { amz }).box.querySelector("section.a-dlg")!.className;
    expect(frame(at("key"))).toBe("a-dlg key");
    expect(frame(at("pick"))).toBe("a-dlg");
    expect(frame({ created: "d3", configs: { d3: [cfg()] } })).toBe("a-dlg");
  });

  it("the versions say what they are; inside a version the keys are told apart by name, by the servers and countries they hold", () => {
    const { box } = form("key-many", { form: { profile: "p31", platform: "windows", label: "", list: false } });
    const vers = [...box.querySelectorAll(".a-vers > .a-ver")];
    expect(vers.map((x) => text(x.querySelector(".v")))).toEqual(["5.0.1.5 и новееAWG 3.1", "До 5.0.1.5AWG 2.0"]);
    expect(vers.map((x) => x.querySelector(".pr")?.className)).toEqual(["pr new", "pr"]);
    expect(vers.map((x) => text(x.querySelector(".d")))).toEqual(["Подходит почти всегда", "Для старых версий приложения"]);
    const fld = box.querySelector(".a-sets")!.closest(".a-fld")!;
    expect(text(fld.querySelector(".a-lbl"))).toBe("Какой ключ создатьПоказать серверы");
    const sets = [...fld.querySelectorAll(".a-sets > button.a-set")];
    expect(sets.map((x) => [text(x.querySelector(".t")), text(x.querySelector(".d")), x.getAttribute("aria-checked")])).toEqual([
      ["AWG3.1", "10 серверов в 7 странах", "true"],
      ["AWG 3.1 (new)", "2 сервера, Эстония", "false"],
    ]);
    expect(fld.querySelector(".a-sets")?.getAttribute("role")).toBe("radiogroup");
    // the flags of the countries, once each, drawn in CSS or as the two letters; no list until it is asked for
    expect([...sets[0]!.querySelectorAll(".a-fls .fl2")].map((x) => x.className.replace(" only-w", ""))).toEqual(["fl2 by", "fl2 de", "fl2 ee", "fl2 nl", "fl2 pl", "fl2 ro", "fl2 ru"]);
    expect(fld.querySelector(".a-srv")).toBeNull();
    // the old version: three keys, the last through the spare exit
    const old = form("key-many", { form: { profile: "p20w", platform: "windows", label: "", list: false } }).box;
    expect([...old.querySelectorAll(".a-sets > button.a-set")].map((x) => [text(x.querySelector(".t")), x.querySelector(".u-tag.warp") !== null, x.classList.contains("on")])).toEqual([
      ["AWG2", false, false],
      ["AWG 2.0 (new)", false, false],
      ["AWG 2.0 (new) · WARPЗапасной выход", true, true],
    ]);
    expect(text(old.querySelector(".a-vers .a-ver.on .v"))).toBe("До 5.0.1.5AWG 2.0");
  });

  it("the servers of the chosen key unfold under the choice: the country, what tells it from the others, the domain", () => {
    const open = form("key-many", { form: { profile: "p31", platform: "windows", label: "", list: true } }).box;
    const more = open.querySelector<HTMLButtonElement>("[data-k=amz-srv-more]")!;
    expect([text(more), more.getAttribute("aria-expanded"), more.classList.contains("on")]).toEqual(["Скрыть серверы", "true", true]);
    expect([...open.querySelectorAll(".a-srv li")].map(text)).toEqual([
      "Беларусьby1.example.com",
      "ГерманияDE-1de1.example.com",
      "ГерманияDE-2de2.example.com",
      "ГерманияDE-3203.0.113.24",
      "ЭстонияEE-1est1.example.com",
      "ЭстонияEE-2est2.example.com",
      "Нидерландыnl1.example.com",
      "Польшаpl1.example.com",
      "РумынияБухарестro1.example.com",
      "РоссияRU-1msk1.example.com",
    ]);
    // the second key of the version holds two servers only; the list follows the choice
    const second = form("key-many", { form: { profile: "p31b", platform: "windows", label: "", list: true } }).box;
    expect([...second.querySelectorAll(".a-srv li")].map(text)).toEqual(["ЭстонияEE-1est1.example.com", "ЭстонияEE-2est2.example.com"]);
    expect(second.querySelector("[data-k=amz-set-p31b]")?.classList.contains("on")).toBe(true);
    expect(text(form("key-many").box.querySelector("[data-k=amz-srv-more]"))).toBe("Показать серверы");
  });

  it("one profile: the version is told, not chosen, and the one key is a card with nothing to press", () => {
    const { box } = form("key-one", { form: { profile: "p31", platform: "windows", label: "", list: false } });
    expect(box.querySelector(".a-vers")).toBeNull();
    expect(text(box.querySelector(".a-vone"))).toBe("5.0.1.5 и новееAWG 3.1Для более старых версий приложения ключа нет");
    const fld = box.querySelector(".a-sets")!.closest(".a-fld")!;
    expect(text(fld.querySelector(".a-lbl"))).toBe("Серверы в ключеПоказать серверы");
    expect(fld.querySelector(".a-sets")?.className).toBe("a-sets solo");
    expect(fld.querySelector(".a-sets")?.getAttribute("role")).toBe("group");
    const card = fld.querySelector(".a-set")!;
    expect([card.tagName, text(card), card.querySelector(".a-rd")]).toEqual(["DIV", "1 сервер, Эстония", null]);
    // a mark is there only when the person has twins: the only server of Estonia is "Estonia" alone
    const open = form("key-one", { form: { profile: "p31", platform: "windows", label: "", list: true } }).box;
    expect([...open.querySelectorAll(".a-srv li")].map(text)).toEqual(["Эстонияest1.example.com"]);
  });

  it("the only profile of the old app says so, and gives no choice", () => {
    const d = data("key-one", (x) => (x.amnezia!.profiles[0]!.version = "2.0"));
    const { box } = dialog(d, { amz: { pane: "key" } });
    expect(text(box.querySelector(".a-vone"))).toBe("До 5.0.1.5AWG 2.0Для старых версий приложения");
    expect(box.querySelector(".a-vone .pr.new")).toBeNull();
  });

  it("a person with one profile of each version sees the choice of version and one card of each", () => {
    const { box } = form("key-pair", { form: { profile: "p20", platform: "windows", label: "", list: false } });
    expect([...box.querySelectorAll(".a-vers > .a-ver")].map((x) => x.className)).toEqual(["a-ver", "a-ver on"]);
    const fld = box.querySelector(".a-sets")!.closest(".a-fld")!;
    expect(text(fld.querySelector(".a-lbl"))).toBe("Серверы в ключеПоказать серверы");
    expect(text(fld.querySelector(".a-set"))).toBe("3 сервера в 3 странах");
    expect(text(form("key-pair").box.querySelector(".a-set"))).toBe("4 сервера в 4 странах");
  });

  it("an older panel names no servers: the key says its countries, with no list to show", () => {
    const d = data("key-pair", (x) => {
      x.servers = [];
      x.amnezia!.profiles[0]!.countries = ["DE", "EE"];
    });
    const { box } = dialog(d, { amz: { pane: "key" } });
    expect(text(box.querySelector(".a-set"))).toBe("В ключе 2 страны");
    expect(box.querySelector("[data-k=amz-srv-more]")).toBeNull();
    // no countries either: nothing to say about the servers
    const none = dialog(data("key-one", (x) => ((x.servers = []), (x.amnezia!.profiles[0]!.countries = []))), { amz: { pane: "key" } }).box;
    expect(none.querySelector(".a-sets")).toBeNull();
  });

  it("the choices go to the page as they do elsewhere", () => {
    const forms: object[] = [];
    const r = dialog(data("key-many"), { a: actions({ amz: { ...amzNoop, form: (p: object) => forms.push(p) } }), amz: at("key") });
    r.box.querySelector<HTMLButtonElement>("[data-k=amz-platform-android]")!.click();
    r.box.querySelector<HTMLButtonElement>("[data-k=amz-ver-old]")!.click();
    r.box.querySelector<HTMLButtonElement>("[data-k=amz-ver-new]")!.click(); // the version that is on: the choice of its key stays
    r.box.querySelector<HTMLButtonElement>("[data-k=amz-set-p31b]")!.click();
    r.box.querySelector<HTMLButtonElement>("[data-k=amz-srv-more]")!.click();
    expect(forms).toEqual([{ platform: "android" }, { profile: "p20" }, { profile: "p31" }, { profile: "p31b" }, { list: true }]);
  });

  it("the profile of a device chosen before is not on the page any more: the first key is shown", () => {
    const { box } = form("key-many", { form: { profile: "gone", platform: "windows", label: "", list: false } });
    expect(text(box.querySelector(".a-ver.on .v"))).toBe("5.0.1.5 и новееAWG 3.1");
    expect(text(box.querySelector(".a-set.on .t"))).toBe("AWG3.1");
  });

  it("'Create' asks the page once; busy says so and waits; an error stays in the form by its code", () => {
    let made = 0;
    const act = actions({ amz: { ...amzNoop, create: () => made++ } });
    const r = dialog(data("many"), { a: act, amz: at("key") });
    r.box.querySelector<HTMLButtonElement>("[data-k=modal-create]")!.click();
    r.box.querySelector("form")!.dispatchEvent(new Event("submit", { cancelable: true }));
    expect(made).toBe(2);
    const busy = dialog(data("many"), { a: act, amz: { pane: "key", busy: "add" } }).box;
    const btn = busy.querySelector<HTMLButtonElement>("[data-k=modal-create]")!;
    expect([btn.disabled, text(btn), btn.querySelector(".spin") !== null]).toEqual([true, "Секунду…", true]);
    btn.click();
    busy.querySelector("form")!.dispatchEvent(new Event("submit", { cancelable: true }));
    expect(made).toBe(2); // a second press while the call is in flight does nothing
    const err = dialog(data("many"), { amz: { pane: "key", error: "network", errorAt: "add" } }).box.querySelector(".a-err")!;
    expect(err.getAttribute("role")).toBe("alert");
    expect(text(err)).toBe("Нет связи. Проверьте интернет и повторите.");
    expect(text(dialog(data("many"), { amz: { pane: "key", error: "too_many_requests", errorAt: "add", retryMin: 7 } }).box.querySelector(".a-err"))).toContain("через 7 мин");
    expect(dialog(data("many"), { amz: { pane: "key", error: "network", errorAt: "other" } }).box.querySelector(".a-err")).toBeNull();
  });

  it("a name is text, never markup", () => {
    const { box } = form("many", { form: { profile: "p31", platform: "ios", label: '<img src=x onerror="alert(1)">', list: false } });
    expect(box.querySelector("img")).toBeNull();
    expect(box.querySelector<HTMLInputElement>("input")!.value).toBe('<img src=x onerror="alert(1)">');
  });
});

describe("step full: no place left", () => {
  it("the figure, the bar, the way out; 'Write' only when the page has a link; 'Open devices' closes the window and opens the tab", () => {
    const a = actions();
    const { box, stage, label } = dialog(data("hwid-dense"), { a });
    expect([stage, label]).toEqual(["full", "Свободных мест нет"]);
    expect(text(box.querySelector(".a-h .n"))).toBe("");
    expect(text(box.querySelector(".a-empty .u-fig"))).toBe("5 из 5мест занято");
    expect(box.querySelector(".a-empty .u-prog")?.getAttribute("aria-label")).toBe("Занято 5 из 5 мест");
    expect(box.querySelector<HTMLElement>(".a-empty .u-prog i")?.style.width).toBe("100%");
    expect(text(box.querySelector(".a-lead"))).toBe("Удалите устройство, которым больше не пользуетесь, или напишите в поддержку, добавим место");
    expect([...box.querySelectorAll(".a-f > *")].map((x) => `${x.tagName} ${text(x)}`)).toEqual(["A Написать в поддержку", "BUTTON Открыть устройства"]);
    box.querySelector<HTMLButtonElement>("[data-k=add-full-devices]")!.click();
    expect(a.log).toEqual(["add false", "tab devices"]);
    const mute = dialog(data("hwid-dense"), { support: "" }).box;
    expect(mute.querySelector("a")).toBeNull();
    expect(text(mute.querySelector(".a-lead"))).toBe("Удалите устройство, которым больше не пользуетесь");
  });

  it("when it shows: the limit is reached and neither a key nor the link can take the device; or the panel says so", () => {
    const stage = (name: string, f: (d: MgData) => void = () => {}, amz: object = {}) => dialog(data(name, f), { amz }).stage;
    expect(stage("hwid-dense")).toBe("full"); // keys at the limit, the link refuses over it
    expect(stage("keys-only", (d) => ((d.user.device_limit = 2), (d.user.devices_used = 2)))).toBe("full");
    expect(stage("devices-full")).toBe("way"); // the link only counts: it still connects
    expect(stage("hwid-full")).toBe("full"); // a link alone, refused over the limit
    expect(stage("first", (d) => (d.user.device_limit = 0), { error: "device_limit", errorAt: "add" })).toBe("way"); // no limit, no such answer
    expect(stage("first", () => {}, { pane: "key", error: "device_limit", errorAt: "add" })).toBe("full"); // the page did not know yet
    expect(stage("first", () => {}, { pane: "key", error: "device_limit", errorAt: "rename:x" })).toBe("key");
    // the figure is never more than the limit
    expect(text(dialog(data("hwid-dense", (d) => (d.user.devices_used = 9))).box.querySelector(".a-empty .u-fig"))).toBe("5 из 5мест занято");
  });
});

describe("the key of a device in the window", () => {
  it("'Show key' and 'New key needed' open the key itself, without a count; the key just made has its place in the branch", () => {
    const d = data("devices-rich");
    const cfgs = { d3: [cfg()] };
    const shown = dialog(d, { amz: { open: "d3", configs: cfgs } });
    expect([shown.stage, shown.label, text(shown.box.querySelector(".n"))]).toEqual(["ready:d3:shown", "Ключ для iPhone", ""]);
    expect(shown.box.querySelector("[data-k=add-back]")).toBeNull();
    const renew = dialog(d, { amz: { renew: "d3", configs: cfgs } });
    expect([renew.stage, renew.label]).toEqual(["ready:d3:renew", "Новый ключ для iPhone"]);
    const made = dialog(d, { amz: { created: "d3", configs: cfgs } });
    expect([made.stage, text(made.box.querySelector(".n"))]).toEqual(["ready:d3:created", "3 из 3"]);
    expect(made.a.log).toEqual([]);
    made.box.querySelector<HTMLButtonElement>("[data-k=add-back]")!.click();
    expect(made.a.log).toEqual(["pane key"]);
    expect(text(made.box.querySelector(".a-f"))).toBe("Устройство уже в списке — ключ можно показать сноваГотово");
  });
});

describe("the keys of the page stay unique (the page puts the keyboard back by them)", () => {
  const names = Object.keys(cases).filter((n) => n !== "locked");
  // walks every sample state, page and pane: ~1.5 s alone, far more on a loaded CI machine
  it("every state and every pane of the window: no key twice, and none shared with the page", { timeout: 60_000 }, () => {
    for (const name of names) {
      for (const platform of ["ios", "windows"] as const) {
        const d = data(name);
        const page = keys(view(d, state(platform, { returning: name !== "first" && name !== "happ" }), actions()));
        for (const pane of ["pick", "link", "conn", "key"] as const) {
          const s = keys(dialog(d, { platform, amz: at(pane) }).box);
          expect(dupes(s), `${name} ${platform} ${pane}`).toEqual([]);
          expect(s.filter((k) => page.includes(k)), `${name} ${platform} ${pane} shared with the page`).toEqual([]);
        }
      }
    }
  });

  it("the full step and the five languages carry no key twice either", () => {
    for (const l of lang as readonly Lang[]) for (const pane of ["pick", "link", "conn", "key"] as const) expect(dupes(keys(dialog(data("hwid-dense"), { lang: l, amz: at(pane) }).box))).toEqual([]);
    expect(dict.ru.adCount(1, 3)).toBe("1 из 3");
  });
});
