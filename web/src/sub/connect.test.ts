import { describe, expect, it } from "vitest";
import { cases } from "./dev-data";
import { dict } from "./i18n";
import { appList, appKey, appNames, chosenApp, keyAppName, normalize, qrApp } from "./logic";
import { actions, data, plain, state, text } from "./test-kit";
import type { MgData } from "./types";
import { view } from "./view";

const t = dict.ru;
const tiles = (el: HTMLElement) => [...el.querySelectorAll(".plats .plat")];

describe("step 1: the device", () => {
  it("lists all five devices, platforms without an app included, and marks the chosen one", () => {
    const el = view(data("nolinux"), state("linux"), actions());
    expect(tiles(el).map(text)).toEqual(["iPhone", "Android", "Windows", "Mac", "Linux"]);
    expect(tiles(el).map((b) => b.getAttribute("aria-checked"))).toEqual(["false", "false", "false", "false", "true"]);
    expect(el.querySelector(".plats")?.getAttribute("role")).toBe("radiogroup");
  });

  it("a tap chooses the device", () => {
    const a = actions();
    const el = view(data("first"), state("ios"), a);
    el.querySelector<HTMLButtonElement>("[data-k=plat-windows]")!.click();
    expect(a.log).toEqual(["platform windows"]);
  });

  it("says it was detected; when the choice differs, offers the way back to the detected device", () => {
    const same = view(data("first"), state("ios"), actions());
    expect(text(same.querySelector(".only-m.hint"))).toBe("Определили по браузеру — если не так, выберите своё");
    const a = actions();
    const other = view(data("first"), state("linux", { detected: "ios" }), a);
    expect(text(other.querySelector(".step .hint:not(.only-m):not(.only-w)"))).toBe("Это не ваш телефон? Вернуть iPhone");
    other.querySelector<HTMLButtonElement>("[data-k=plat-back]")!.click();
    expect(a.log).toEqual(["platform ios"]);
    const pc = view(data("first"), state("macos", { detected: "windows" }), actions());
    expect(text(pc.querySelector("[data-k=plat-back]"))).toBe("Вернуть Windows");
    expect(view(data("first"), state("ios", { detected: null }), actions()).querySelector("[data-k=plat-back]")).toBeNull();
  });
});

describe("step 1: how to connect (two ways: the same cards as the sheet)", () => {
  const titles = (el: HTMLElement) => [...el.querySelectorAll(".step .step-t")].map(text);
  const nums = (el: HTMLElement) => [...el.querySelectorAll(".step .sn")].map(text);

  it("with both ways the first step asks: with an app (chosen, recommended) or with a key; the steps are numbered on", () => {
    const el = view(data("first"), state("ios"), actions());
    expect(titles(el)).toEqual(["Чем подключать", "Ваше устройство", "Приложение", "Как подключиться"]);
    expect(nums(el)).toEqual(["1", "2", "3", "4"]);
    const cards = [...el.querySelectorAll(".step")[0]!.querySelectorAll(".opt.go")];
    expect(cards.map((x) => x.getAttribute("data-k"))).toEqual(["way-link", "way-key"]);
    expect(cards.map((x) => x.getAttribute("aria-checked"))).toEqual(["true", "false"]);
    expect(el.querySelector(".step [role=radiogroup][aria-label='Чем подключать']")).not.toBeNull();
    expect(text(cards[0]!.querySelector(".opt-t"))).toBe("Через приложениеРекомендуем");
    expect(text(cards[1]!.querySelector(".opt-t"))).toBe("Ключом AmneziaVPN");
  });

  it("a card tells the page; the chosen one does nothing", () => {
    const a = actions();
    const el = view(data("first"), state("ios"), a);
    el.querySelector<HTMLButtonElement>("[data-k=way-link]")!.click(); // already chosen
    el.querySelector<HTMLButtonElement>("[data-k=way-key]")!.click();
    expect(a.log).toEqual(["way key"]);
  });

  it("the key way: the device, then install / add this device / paste the key; no app list, no QR code", () => {
    const el = view(data("first"), state("ios", { way: "key" }), actions());
    expect(titles(el)).toEqual(["Чем подключать", "Ваше устройство", "Как подключиться"]);
    expect(nums(el)).toEqual(["1", "2", "3"]);
    expect([...el.querySelectorAll(".how-i .how-t")].map(text)).toEqual(["Установите AmneziaVPN", "Добавьте это устройство", "Вставьте ключ в AmneziaVPN"]);
    expect(el.querySelector(".app")).toBeNull();
    expect(el.querySelector(".qr, [data-k=qr-row]")).toBeNull();
    expect(el.querySelector(".side .qrcard")).toBeNull();
    expect(el.querySelector("[data-k=way-key]")?.getAttribute("aria-checked")).toBe("true");
    expect(view(data("first"), state("windows", { way: "key" }), actions()).querySelector(".qrcard")).toBeNull();
  });

  it("one way only: no question; the steps are numbered from 1", () => {
    const link = view(data("happ"), state("ios"), actions());
    expect(titles(link)).toEqual(["Ваше устройство", "Приложение", "Как подключиться"]);
    expect(link.querySelector("[data-k=way-link]")).toBeNull();
    const keys = view(data("keys-only"), state("ios"), actions());
    expect(titles(keys)).toEqual(["Ваше устройство", "Как подключиться"]);
    expect(nums(keys)).toEqual(["1", "2"]);
    expect([...keys.querySelectorAll(".how-i .how-t")][1]).toBeDefined();
  });

  it("a key that cannot be had now is a quiet card, and the steps stay the link's even if the key was chosen", () => {
    const el = view(data("limit"), state("ios", { way: "key" }), actions());
    expect(el.querySelector("[data-k=way-key]")).toBeNull();
    expect(text(el.querySelector(".opt.off .opt-d"))).toBe("Занято 3 из 3. Удалите устройство, которым больше не пользуетесь, или напишите — добавим место.");
    expect(titles(el)).toContain("Приложение");
    expect(el.querySelector("[data-k=way-link]")?.getAttribute("aria-checked")).toBe("true");
  });

  it("the keys of the wizard and the cards are one design: the same words as the sheet", () => {
    const el = view(data("first"), state("ios"), actions());
    expect(text(el.querySelector("[data-k=way-link] .wayslot"))).toBe("Одно место на все приложения");
    expect(text(el.querySelector("[data-k=way-key] .wayslot"))).toBe("Занимает 1 место · свободно 3");
  });
});

describe("step 2: the app (link apps only)", () => {
  it("the recommended app big, the others under 'Other apps', never folded; no key app among them, no 'how' chip", () => {
    const el = view(data("first"), state("windows"), actions());
    const cards = [...el.querySelectorAll(".app")];
    expect(cards.map((c) => text(c.querySelector(".app-n")))).toEqual(["kl!ckРекомендуем", "Happ"]);
    expect(cards.map((c) => c.getAttribute("aria-checked"))).toEqual(["true", "false"]);
    expect(cards[0]!.classList.contains("min")).toBe(false);
    expect(cards[1]!.classList.contains("min")).toBe(true);
    expect(text(el.querySelector(".step .lbl"))).toBe("Другие приложения");
    expect(el.querySelectorAll(".app .way")).toHaveLength(0);
    expect(el.querySelector(".app-grid .app")).not.toBeNull();
    expect(el.textContent).not.toContain("по ссылке подписки");
    expect(text(cards[0]!.querySelector(".desc"))).toBe("Все ваши серверы в одном приложении, подписка обновляется сама");
    expect(el.querySelector("a[href^='klick://add?url=']")).not.toBeNull();
    const phone = view(data("first"), state("ios"), actions());
    expect([...phone.querySelectorAll(".app")].map((c) => text(c.querySelector(".app-n")))).toEqual(["Happ"]);
    expect(text(phone.querySelector(".app .desc"))).toBe(plain(t.linkD));
  });

  it("the lists in logic keep the kinds apart on request", () => {
    const d = data("first");
    expect(appList(d, "windows").map((x) => x.name)).toEqual(["kl!ck", "Happ", "AmneziaVPN"]);
    expect(appList(d, "windows", "happ").map((x) => x.name)).toEqual(["kl!ck", "Happ"]);
    expect(appList(d, "ios", "amnezia").map((x) => x.name)).toEqual(["AmneziaVPN"]);
  });

  it("choosing another app tells the page; choosing the chosen one does nothing", () => {
    const a = actions();
    const el = view(data("first"), state("windows"), a);
    el.querySelector<HTMLButtonElement>("[data-k='app-happ|Happ']")!.click();
    el.querySelector<HTMLButtonElement>("[data-k='app-happ|kl!ck']")!.click(); // the default: already chosen
    expect(a.log).toEqual(["app happ|Happ"]);
    expect(chosenApp(data("first"), "windows", "happ|Happ", "happ")?.name).toBe("Happ");
    expect(chosenApp(data("first"), "ios", "amnezia|AmneziaVPN", "happ")?.name).toBe("Happ"); // a key app never takes a link app's place
    expect(chosenApp(data("first"), "ios", "gone|X")?.name).toBe("Happ");
    expect(appKey(appList(data("first"), "ios")[0]!)).toBe("happ|Happ");
  });
  it("a platform without an app says so, still offers the link and the way to write", () => {
    const a = actions();
    const el = view(data("nolinux"), state("linux"), a);
    const box = el.querySelector(".noapps")!;
    expect(text(box.querySelector(".h3"))).toBe("Для Linux приложений пока нет");
    expect(text(box.querySelector(".sm"))).toBe("Скопируйте ссылку — она подойдёт любому приложению с подписками — или выберите другое устройство.");
    box.querySelector<HTMLButtonElement>("[data-k=link-copy]")!.click();
    expect(a.log).toEqual([`copy ${data("nolinux").subscription_url}`, "mark"]);
    expect(text(box.querySelector("a.btn"))).toBe("Написать");
    expect(el.querySelectorAll(".step")).toHaveLength(2); // no third step: nothing to do with an app that does not exist
  });

  it("nothing to connect with at all", () => {
    const el = view(data("noapps"), state("ios"), actions());
    expect(el.querySelector(".card[aria-label=Подключение]")).toBeNull();
  });

  it("the names of an app list, and the phone's QR app", () => {
    const d = data("multi");
    expect(appNames(appList(d, "windows"))).toEqual(["kl!ck", "AmneziaVPN", "Example Client"]);
    expect(qrApp(d)).toBe("Happ");
    expect(keyAppName(d, "windows")).toBe("AmneziaVPN");
  });
});

describe("step 3: a link app", () => {
  it("install (Download, the store under it), add with one tap, turn the VPN on; every server is there", () => {
    const el = view(data("first"), state("ios"), actions());
    const how = [...el.querySelectorAll(".how-i")];
    expect(how.map((i) => text(i.querySelector(".how-t")))).toEqual(["Установите Happ", "Добавьте подписку", "Включите VPN в Happ"]);
    const dl = how[0]!.querySelector<HTMLAnchorElement>("a.btn.dl")!;
    expect([text(dl.querySelector("b")), text(dl.querySelector("small"))]).toEqual(["Скачать", "App Store"]); // never "Скачать Happ · с са…"
    expect(dl.getAttribute("href")).toMatch(/^https:\/\/apps\.apple\.com\//);
    const add = how[1]!.querySelector<HTMLAnchorElement>("a.btn.pri")!;
    expect(add.getAttribute("href")).toMatch(/^happ:\/\/add\//);
    expect(text(add)).toBe("Добавить одним нажатием");
    expect(text(how[1]!.querySelector(".how-sub"))).toBe("Happ откроется уже с вашей подпиской");
    expect(text(how[2]!.querySelector(".hint"))).toBe("Все ваши серверы (3) уже там — выберите любой");
    expect(text(el.querySelector(".step:nth-of-type(3) .step-t, .step .step-t:last-of-type"))).not.toBe("");
  });

  it("'won't open?': copy the link and where to paste it, right under the add button; pressing add or copy marks this device", () => {
    const a = actions();
    const d = data("first");
    const el = view(d, state("ios"), a);
    const fb = el.querySelector(".fb")!;
    expect(text(fb.querySelector("b"))).toBe("Не открывается?");
    expect(text(fb.querySelector(".hint"))).toBe("Вставьте ссылку в Happ: «+» → «Вставить из буфера»");
    expect(fb.querySelector(".hint .q")).not.toBeNull(); // the buttons the person must find stay on one line
    const copy = fb.querySelector<HTMLButtonElement>(".tlink")!;
    expect(text(copy)).toBe("Скопировать");
    copy.click();
    expect(a.log).toEqual([`copy ${d.subscription_url}`, "mark"]);
    expect(text(copy)).toBe("Скопировано"); // it says so on itself
    el.querySelector<HTMLAnchorElement>("[data-k=link-add]")!.addEventListener("click", (e) => e.preventDefault());
    el.querySelector<HTMLAnchorElement>("[data-k=link-add]")!.click();
    expect(a.log.at(-1)).toBe("mark");
  });

  it("without an add scheme the copy button leads and the line says where to paste", () => {
    const el = view(data("custom"), state("ios"), actions());
    const how = [...el.querySelectorAll(".how-i")];
    expect(how.map((i) => text(i.querySelector(".how-t")))).toEqual(["Установите Happ", "Скопируйте ссылку и вставьте её в приложение", "Включите VPN"]);
    expect(text(how[1]!.querySelector("button.btn.pri"))).toBe("Скопировать ссылку");
    expect(text(how[1]!.querySelector(".how-sub"))).toBe("В Happ: добавить подписку → вставить ссылку");
    expect(el.querySelector("a[href^='happ://']")).toBeNull();
  });

  it("an app of the instance without a download link has no install step", () => {
    const el = view(data("first", (d) => d.apps.forEach((x) => (x.download_url = ""))), state("ios"), actions());
    expect([...el.querySelectorAll(".how-i .how-t")].map(text)).toEqual(["Добавьте подписку", "Включите VPN в Happ"]);
  });

  it("one server: no 'every server' line", () => {
    expect(view(data("plain"), state("ios"), actions()).querySelector(".how-i:last-child .hint")).toBeNull();
  });

  it("after the app fetched the subscription (and this device set it up) the step says so, and can show the steps again", () => {
    const a = actions();
    const el = view(data("return"), state("ios", { marked: true }), a);
    const s3 = el.querySelectorAll(".step")[3]!; // after the way, the device and the app
    expect(s3.querySelector(".sn.done")).not.toBeNull();
    expect(text(s3.querySelector(".note.ok"))).toBe("Готово — приложение получило подписку 3 часа назад. Включите VPN и выберите любой сервер.");
    expect(s3.querySelector(".how")).toBeNull();
    s3.querySelector<HTMLButtonElement>("[data-k=steps-again]")!.click();
    expect(a.log).toEqual(["steps-again"]);
    const again = view(data("return"), state("ios", { marked: true, stepsAgain: true }), actions());
    expect(again.querySelectorAll(".step")[3]!.querySelector(".how")).not.toBeNull();
    // not done when this device never set it up
    expect(view(data("return"), state("ios"), actions()).querySelectorAll(".step")[3]!.querySelector(".how")).not.toBeNull();
  });
});

describe("step 3: the key way", () => {
  const key = { way: "key" as const };
  it("install, add this device (its key is made), paste the key; the phone copies, the computer downloads the file", () => {
    const a = actions();
    const el = view(data("first"), state("ios", key), a);
    const how = [...el.querySelectorAll(".how-i")];
    expect(how.map((i) => text(i.querySelector(".how-t")))).toEqual(["Установите AmneziaVPN", "Добавьте это устройство", "Вставьте ключ в AmneziaVPN"]);
    expect(text(how[1]!.querySelector(".how-sub"))).toBe("Для него появится свой ключ");
    expect(text(how[2]!.querySelector(".how-sub"))).toBe("Скопируйте ключ → AmneziaVPN → «+» → вставьте → «Продолжить»");
    const add = how[1]!.querySelector<HTMLButtonElement>("[data-k=amz-add]")!;
    expect(text(add)).toBe("Добавить устройство");
    add.click();
    expect(a.log).toEqual(["add true key"]); // straight to the key form: the way is already chosen
    const pc = view(data("first"), state("windows", key), actions());
    expect(text(pc.querySelectorAll(".how-i")[2]!.querySelector(".how-sub"))).toBe("Скачайте файл → AmneziaVPN → «+» → «Файл с настройками подключения»");
    expect(el.querySelector("a[href^='happ://']")).toBeNull(); // a key app has no add link
  });

  it("no key app in the settings for the platform: the steps still say what to do, without an install button", () => {
    const el = view(data("first", (d) => (d.apps = d.apps.filter((x) => x.kind !== "amnezia"))), state("ios", key), actions());
    expect([...el.querySelectorAll(".how-i .how-t")].map(text)).toEqual(["Добавьте это устройство", "Вставьте ключ в AmneziaVPN"]);
  });

  // keys alone (no link app): the steps meet the states of the key way
  const alone = (name: string, f: (d: MgData) => void = () => {}) => view(data(name, (d) => ((d.access.happ = false), f(d))), state("ios"), actions());

  it("the button waits at the limit, with the words; the owner issues keys: ask; no profile: not yet", () => {
    const limit = alone("limit");
    expect(limit.querySelector<HTMLButtonElement>(".how-i [data-k=amz-add]")?.disabled).toBe(true);
    expect(text(limit.querySelector(".how-i .note.warn"))).toBe("Занято 3 из 3. Удалите устройство, которым больше не пользуетесь, или напишите — добавим место.");
    const admin = alone("amnezia-off");
    const s = [...admin.querySelectorAll(".how-i")].map((i) => text(i.querySelector(".how-t")));
    expect(s[1]).toBe("Попросите ключ");
    expect(text(admin.querySelectorAll(".how-i")[1]!.querySelector(".how-sub"))).toBe("Ключи AmneziaVPN выдаёт владелец — напишите, для какого устройства нужен.");
    const none = alone("amnezia-none");
    expect(text([...none.querySelectorAll(".how-i")][1]!.querySelector(".how-sub"))).toBe("Сервер ещё настраивается — напишите администратору.");
    expect(none.querySelector(".how-i [data-k=amz-add]")).toBeNull();
  });

  it("the admin's preview has no address: the button waits and nothing says 'ask the admin'", () => {
    const el = alone("first", (x) => (x.amnezia!.endpoints = ""));
    expect(el.querySelector<HTMLButtonElement>(".how-i [data-k=amz-add]")?.disabled).toBe(true);
    expect(el.textContent).not.toContain("Попросите ключ");
  });
});
describe("another device: the QR code of the link", () => {
  it("a phone folds it under 'Connect another device'; opened, it says what it does and copies the link", () => {
    const a = actions();
    const closed = view(data("first"), state("ios"), a);
    const row = closed.querySelector<HTMLButtonElement>("[data-k=qr-row]")!;
    expect(text(row)).toBe("Подключить другое устройствоQR-код откроет эту страницу на нём");
    expect(row.getAttribute("aria-expanded")).toBe("false");
    expect(closed.querySelector(".qrbox")).toBeNull();
    row.click();
    expect(a.log).toEqual(["qr true"]);
    const open = view(data("first"), state("ios", { qrOpen: true }), actions());
    expect(open.querySelector(".qrbox .qr svg")).not.toBeNull();
    expect(text(open.querySelector(".qrbox .hint"))).toBe("Наведите камеру — откроется эта страница. Или в Happ: «+» → «Сканировать QR»");
    expect(text(open.querySelector(".qrbox .tlink"))).toBe("Скопировать ссылку");
  });

  it("a computer shows it beside the steps, dark on white", () => {
    const el = view(data("first"), state("windows"), actions());
    const side = el.querySelector(".side .qrcard")!;
    expect(text(side.querySelector(".h3"))).toBe("Откройте на телефоне");
    expect(side.querySelector(".qr svg[role=img]")?.getAttribute("aria-label")).toBe("QR-код со ссылкой на эту страницу");
    expect(text(side.querySelector("button"))).toBe("Скопировать ссылку");
  });

  it("the option off, or no link app: no QR code at all", () => {
    for (const el of [view(data("plain"), state("ios"), actions()), view(data("first", (d) => (d.options.show_qr = false)), state("windows"), actions())]) {
      expect(el.querySelector(".qr")).toBeNull();
      expect(el.querySelector("[data-k=qr-row]")).toBeNull();
    }
  });
});

describe("the data of the cases", () => {
  it("every case is data the page can read", () => {
    for (const raw of Object.values(cases)) expect(normalize(raw).v).toBe(1);
  });
});
