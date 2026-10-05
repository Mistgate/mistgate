import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { addModal, copyButton, newAmzState } from "./amnezia";
import { amzActions, type Api } from "./amz-actions";
import { cases } from "./dev-data";
import { dict } from "./i18n";
import { appsFor, awgConfig, awgDevice, normalize, platformsOf, qrApp, ways } from "./logic";
import { createModal } from "./modal";
import type { MgData } from "./types";
import { view, type Actions, type State } from "./view";

const t = dict.ru;

function actions(over: Partial<Actions> = {}): Actions & { log: string[] } {
  const log: string[] = [];
  const noop = () => {};
  return {
    log,
    lang: noop,
    platform: (p) => log.push(`platform ${p}`),
    qrOpen: noop,
    closeAnn: noop,
    copy: (text, _m, done) => {
      log.push(`copy ${text}`);
      done?.();
    },
    download: (f) => log.push(`download ${f}`),
    amz: { add: (o) => log.push(`add ${o}`), form: noop, create: noop, show: noop, renew: noop, node: noop, ask: noop, rotate: noop, remove: noop },
    ...over,
  };
}

const state = (platform: State["platform"], lang: "ru" | "en" = "ru"): State => ({ lang, platform, qrOpen: false, annClosed: false, amz: newAmzState("windows", "p31", "windows") });
const data = (name: keyof typeof cases, f: (d: MgData) => void = () => {}) => {
  const d = normalize(structuredClone(cases[name]!));
  f(d);
  return d;
};
const way = (el: HTMLElement, w: "link" | "key") => el.querySelector<HTMLElement>(`[data-way=${w}]`)!;
const text = (el: Element | null | undefined) => el?.textContent ?? "";

describe("server load", () => {
  const loads = (server_loads: MgData["server_loads"]) => data("both", (d) => void (d.server_loads = server_loads));
  it("says a level per server, never a rate, and points a busy server's users to the calmest other one", () => {
    const el = view(loads([{ name: "Эстония", level: "high" }, { name: "Германия", level: "medium" }, { name: "Россия", level: "low" }]), state("ios"), actions());
    const card = el.querySelector(".server-loads")!;
    expect([...card.querySelectorAll(".server-load-pct")].map(text)).toEqual(["Высокая", "Средняя", "Низкая"]);
    expect(text(card)).not.toMatch(/Мбит|Mbps|\d%/);
    expect(text(card.querySelector(".server-load-notice"))).toBe(t.serverLoadTry("Эстония", "Россия"));
  });

  it("only warns when every server is busy, and is gone when no server has a known capacity", () => {
    const busy = view(loads([{ name: "Эстония", level: "high" }]), state("ios"), actions());
    expect(text(busy.querySelector(".server-load-notice.high"))).toBe(t.serverLoadBusy);
    expect(view(loads([]), state("ios"), actions()).querySelector(".server-loads")).toBeNull();
  });
});

describe("two ways, side by side", () => {
  it("both ways of a user who has both, each with its own name, line and icon; the platform is asked once above them", () => {
    const el = view(data("both"), state("ios"), actions());
    expect([...el.querySelectorAll(".ways > .way")].map((w) => (w as HTMLElement).dataset.way)).toEqual(["link", "key"]);
    expect(text(way(el, "link").querySelector(".way-t"))).toBe("Подписка — Happ");
    expect(text(way(el, "link").querySelector(".way-ht .mut"))).toBe(t.linkD);
    expect(text(way(el, "key").querySelector(".way-t"))).toBe("Ключ AmneziaVPN");
    expect(text(way(el, "key").querySelector(".way-ht .mut"))).toBe(t.keyD);
    expect(el.querySelectorAll(".plats")).toHaveLength(1);
    expect(text(el.querySelector(".two-ways"))).toBe(t.twoWays);
    expect(el.querySelector(".ways")?.classList.contains("solo")).toBe(false);
  });

  it("a user with one way sees only that one, the whole width, without the 'either way' line", () => {
    const happ = view(data("happ"), state("ios"), actions());
    expect(happ.querySelectorAll(".way")).toHaveLength(1);
    expect(happ.querySelector(".ways")?.classList.contains("solo")).toBe(true);
    expect(happ.querySelector(".two-ways")).toBeNull();
    const keys = view(data("keys-only"), state("windows"), actions());
    expect([...keys.querySelectorAll(".way")].map((w) => (w as HTMLElement).dataset.way)).toEqual(["key"]);
  });

  it("the way of the platform's recommended app leads; with nothing recommended the subscription does", () => {
    const d = data("both", (x) => x.apps.forEach((a) => (a.recommended = false)));
    expect(ways(d, "ios")).toEqual(["link", "key"]);
    d.apps.find((a) => a.platform === "ios" && a.kind === "amnezia")!.recommended = true;
    expect(ways(d, "ios")).toEqual(["key", "link"]);
    expect(ways(d, "android")).toEqual(["link", "key"]);
  });

  it("the platform buttons switch both ways", () => {
    const a = actions();
    const d = data("multi");
    expect(platformsOf(d)).toEqual(["ios", "android", "windows", "macos"]);
    const el = view(d, state("ios"), a);
    expect([...el.querySelectorAll(".plat")].map((b) => b.textContent)).toEqual(["iPhone", "Android", "Windows", "Mac"]);
    el.querySelector<HTMLButtonElement>("[data-k=plat-windows]")!.click();
    expect(a.log).toEqual(["platform windows"]);
    expect(text(way(view(d, state("windows"), a), "link").querySelector(".way-t"))).toBe("Подписка — Happ, Example Client");
  });
});

describe("the subscription way", () => {
  it("two numbered steps: install (Download, the store under it) and add (the app's add link)", () => {
    const el = way(view(data("happ"), state("ios"), actions()), "link");
    const steps = [...el.querySelectorAll(".steps .step")];
    expect(steps.map((s) => text(s.querySelector(".step-t")))).toEqual(["Установите Happ", "Добавьте подписку"]);
    expect(steps.map((s) => text(s.querySelector(".n")))).toEqual(["1", "2"]);
    const dl = steps[0]!.querySelector<HTMLAnchorElement>("a.btn.dl")!;
    expect([text(dl.querySelector("b")), text(dl.querySelector("small"))]).toEqual(["Скачать", "App Store"]); // never "Скачать Happ · с са…"
    expect(dl.getAttribute("href")).toMatch(/^https:\/\/apps\.apple\.com\//);
    const add = steps[1]!.querySelector<HTMLAnchorElement>("a.btn.pri")!;
    expect(add.getAttribute("href")).toMatch(/^happ:\/\/add\//);
    expect(text(add)).toBe("Добавить в Happ");
  });

  it("right under the add button: 'won't open? copy the link' and where to paste it, whatever the QR option says", () => {
    const a = actions();
    const d = data("plain"); // show_qr off
    const el = way(view(d, state("ios"), a), "link");
    const copy = el.querySelector<HTMLButtonElement>(".fallback .tlink")!;
    expect(text(copy)).toBe("Не открывается? Скопировать ссылку");
    expect(text(el.querySelector(".fallback .hint"))).toBe("В Happ: «+» → «Вставить из буфера»");
    copy.click();
    expect(a.log).toEqual([`copy ${d.subscription_url}`]);
    expect(text(copy)).toBe("Скопировано"); // it says so on itself
    expect(el.querySelector(".qr")).toBeNull();
  });

  it("says that every server shows up in the app, only when there is more than one", () => {
    expect(text(way(view(data("happ"), state("ios"), actions()), "link").querySelector(".all-servers"))).toBe("В Happ появятся все ваши серверы (3) — выберите любой");
    expect(way(view(data("plain"), state("ios"), actions()), "link").querySelector(".all-servers")).toBeNull();
  });

  it("the QR code is for another device: beside the steps on a computer, folded on a phone, with what it does", () => {
    const el = way(view(data("happ"), state("ios"), actions()), "link");
    expect(el.querySelector(".qrside.only-w svg")).not.toBeNull();
    expect(text(el.querySelector(".qrside b"))).toBe("Подключить телефон");
    expect(text(el.querySelector(".qrside p"))).toBe("Наведите камеру телефона — откроется эта страница. Или в Happ: «+» → «Сканировать QR»");
    expect(text(el.querySelector("details.qrx.only-m summary"))).toContain("Подключить другое устройство (QR‑код)");
    expect(qrApp(data("happ"))).toBe("Happ");
  });

  it("other link apps of the platform are listed with their own buttons, never folded; the recommended one leads", () => {
    const a = actions();
    const d = data("multi");
    expect(appsFor(d, "windows", "happ").map((x) => x.name)).toEqual(["Happ", "Example Client"]);
    const el = way(view(d, state("windows"), a), "link");
    expect(text(el.querySelector(".step .badge"))).toBe("рекомендуем");
    const alt = el.querySelector(".alts .alt")!;
    expect(text(alt.querySelector(".alt-t b"))).toBe("Example Client");
    expect(alt.closest("details")).toBeNull();
    const copy = [...alt.querySelectorAll("button")].find((b) => text(b) === "Скопировать ссылку")!;
    copy.click();
    expect(a.log).toEqual([`copy ${d.subscription_url}`]);
    expect(text(alt.querySelector(".hint"))).toBe("В Example Client: добавить подписку → вставить ссылку");
  });

  it("a platform without a link app says so and still gives the link", () => {
    const el = way(view(data("both"), state("linux"), actions()), "link");
    expect(text(el.querySelector(".step-d"))).toBe("Для Linux приложения нет — выберите другое устройство выше.");
    expect(el.querySelector("button.btn.sec")).not.toBeNull();
  });

  it("what uses the link: the one shared device, named for what it is, not guessed as one app", () => {
    const el = way(view(data("happ"), state("ios"), actions()), "link");
    expect(text(el.querySelector(".sub-list .sub-t"))).toBe("Подключено через подписку");
    const row = el.querySelector(".sub-list .krow")!;
    expect(text(row.querySelector(".kname"))).toBe("Приложения по ссылке");
    expect([...row.querySelectorAll(".meta")].map(text)).toEqual(["в сети", "Все устройства с этой ссылкой занимают одно место"]);
    // a device the server knows (a model) is named by it; one without anything else by the app
    const named = data("happ", (d) => (d.devices = [{ id: "h1", platform: "ios", model: "iPhone 15", app: "happ", last_seen_unix: 0, online: false }, { id: "h2", platform: "", model: "", app: "happ", last_seen_unix: 0, online: false }]));
    const rows = [...way(view(named, state("ios"), actions()), "link").querySelectorAll(".sub-list .krow .kname")].map(text);
    expect(rows).toEqual(["iPhone 15", "Приложения по ссылке"]);
  });

  it("the page copy speaks of what an app does; the names come from the settings", () => {
    for (const lang of ["ru", "en"] as const) {
      const dd = dict[lang];
      for (const s of [dd.linkD, dd.keyD, dd.twoWays, dd.noOpen, dd.copyLink, dd.qrHow(""), dd.noProfile, dd.staleD, dd.keysByAdmin, dd.stepAddSub, dd.stepAddDev]) {
        expect(s).not.toMatch(/Happ|Amnezia|Mihomo/i);
      }
    }
  });

  it("with no usable app the page says so", () => {
    const el = view(normalize({ ...cases.happ!, apps: [] }), state(null), actions());
    expect(text(el.querySelector(".conn-none"))).toBe(t.noApps);
    expect(el.querySelector(".way")).toBeNull();
  });
});

describe("the add-a-device entry", () => {
  it("one button, 'Add a device', opens the dialog", () => {
    const a = actions();
    const el = view(data("many"), state("windows"), a);
    const btns = [...el.querySelectorAll("button")].filter((b) => text(b) === "Добавить устройство");
    expect(btns).toHaveLength(1);
    btns[0]!.click();
    expect(a.log).toEqual(["add true"]);
  });

  it("after Create the dialog shows the new device's key, and closing leaves the device listed", async () => {
    const d = data("many");
    const st = { lang: "ru" as const, amz: newAmzState("android", "p31", "android") };
    const api: Api = {
      addDevice: async (_e, b) => ({
        device: awgDevice({ id: "dev_new", platform: b.platform, label: b.label, profile_id: b.profile_id, profile_name: "Main", version: "3.1" }),
        configs: [awgConfig({ node_name: "de1", country_code: "DE", version: "3.1", conf: "[Interface]\nPrivateKey = x\n", vpn_key: "vpn://k", filename: "mistgate-de.conf" })],
      }),
      getConfigs: async () => ({ configs: [] }),
      rotateKey: async () => ({ configs: [] }),
      revokeDevice: async () => {},
    };
    const amz = amzActions({ data: d, st, render: () => {}, api, reveal: () => {} });
    const tools = actions({ amz });
    amz.add(true);
    amz.form({ platform: "windows", label: "Work PC" });
    amz.create();
    await new Promise((r) => setTimeout(r, 0));

    const box = document.createElement("div");
    box.append(...(addModal({ d, s: st, a: tools, t }) as Node[]));
    expect(text(box.querySelector(".mt"))).toBe("Ключ для «Work PC»");
    expect(box.querySelector("form")).toBeNull();
    // a key for a computer looked at from a phone: open the page there; the file and the key are still at hand
    expect(text(box.querySelector(".hintbox.calm"))).toBe(t.openThere);
    const btns = [...box.querySelectorAll(".cfg-actions button")];
    expect(btns.map(text)).toEqual(["Скачать файл", "Скопировать ключ"]);
    btns[0]!.dispatchEvent(new MouseEvent("click"));
    btns[1]!.dispatchEvent(new MouseEvent("click"));
    expect(tools.log).toEqual(["download mistgate-de.conf", "copy vpn://k"]);
    // the dialog has one accent button at most: "Done" is not it
    expect(box.querySelector("[data-k=modal-done]")?.classList.contains("sec")).toBe(true);

    box.querySelector<HTMLButtonElement>("[data-k=modal-done]")!.click();
    expect(st.amz).toMatchObject({ adding: false, created: null });
    expect(view(d, state("windows"), actions()).querySelectorAll("[data-way=key] .krow")).toHaveLength(d.amnezia!.devices.length);
    expect(d.amnezia!.devices.map((x) => x.label)).toContain("Work PC");
  });
});

describe("the copy button", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("turns into 'Скопировано' for a moment and goes back; a failed copy changes nothing", () => {
    let ok = true;
    const tools = { copy: (_x: string, _m: string, done?: () => void) => ok && done?.(), download: () => {} };
    const b = copyButton(tools, { text: "k", label: "Скопировать ключ", done: "Скопировано", toast: "x" });
    b.click();
    expect(b.textContent).toBe("Скопировано");
    expect(b.classList.contains("ok")).toBe(true);
    vi.advanceTimersByTime(1900);
    expect(b.textContent).toBe("Скопировать ключ");
    expect(b.classList.contains("ok")).toBe(false);
    ok = false;
    b.click();
    expect(b.textContent).toBe("Скопировать ключ");
  });
});

describe("the dialog", () => {
  // jsdom has no showModal: a stand-in that tracks `open` and fires "close" like the browser
  beforeEach(() => {
    HTMLDialogElement.prototype.showModal = function (this: HTMLDialogElement) {
      this.setAttribute("open", "");
    };
    HTMLDialogElement.prototype.close = function (this: HTMLDialogElement) {
      if (!this.open) return;
      this.removeAttribute("open");
      this.dispatchEvent(new Event("close"));
    };
  });
  afterEach(() => document.body.replaceChildren());

  function setup() {
    const root = document.createElement("div");
    const opener = document.createElement("button");
    opener.dataset.k = "opener";
    root.append(opener);
    document.body.append(root);
    const closed: number[] = [];
    const m = createModal({ onClose: () => closed.push(1), refocus: (k) => root.querySelector<HTMLElement>(`[data-k="${k}"]`)?.focus(), label: () => "Новое устройство" });
    const btn = (k: string) => {
      const b = document.createElement("button");
      b.dataset.k = k;
      return b;
    };
    return { root, opener, m, closed, btn };
  }

  it("opens modally from the opener, labels itself, and focus goes to the dialog", () => {
    const { opener, m, btn } = setup();
    opener.focus();
    m.sync(true, [btn("first"), btn("second")]);
    expect(m.el.open).toBe(true);
    expect(m.el.getAttribute("aria-label")).toBe("Новое устройство");
    expect(document.activeElement).toBe(m.el.querySelector("[data-k=first]"));
  });

  it("a redraw keeps the focus on the same control; the content is replaced, the dialog is not", () => {
    const { m, btn } = setup();
    m.sync(true, [btn("first"), btn("second")]);
    m.el.querySelector<HTMLElement>("[data-k=second]")!.focus();
    const el = m.el;
    m.sync(true, [btn("first"), btn("second")]);
    expect(m.el).toBe(el);
    expect(document.activeElement).toBe(m.el.querySelector("[data-k=second]"));
  });

  it("Esc (the close event) tells the page and gives the focus back to the opener's new copy", () => {
    const { root, opener, m, closed, btn } = setup();
    opener.focus();
    m.sync(true, [btn("first")]);
    const again = document.createElement("button");
    again.dataset.k = "opener";
    root.replaceChildren(again); // the page was drawn again
    m.el.dispatchEvent(new Event("cancel", { cancelable: true }));
    m.el.close();
    expect(closed).toEqual([1]);
    expect(document.activeElement).toBe(root.querySelector("[data-k=opener]"));
  });

  it("a tap that did not focus the opener (Safari) still gives the focus back to the control that was used", () => {
    const { root, m, btn } = setup();
    const again = document.createElement("button");
    again.dataset.k = "used";
    root.append(again);
    const el = createModal({ onClose: () => {}, refocus: (k) => root.querySelector<HTMLElement>(`[data-k="${k}"]`)?.focus(), label: () => "x", lastUsed: () => "used" });
    (document.activeElement as HTMLElement | null)?.blur();
    el.sync(true, [btn("first")]);
    el.el.close();
    expect(document.activeElement).toBe(again);
    expect(m.el.open).toBe(false);
  });

  it("a click on the backdrop closes it, a click inside or a drag that ends on it does not", () => {
    const { m, closed, btn } = setup();
    m.sync(true, [btn("first")]);
    const inside = m.el.querySelector("[data-k=first]")!;
    inside.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    inside.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(m.el.open).toBe(true);
    inside.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true })); // selection started in the box...
    m.el.dispatchEvent(new MouseEvent("click")); // ...and released on the backdrop
    expect(m.el.open).toBe(true);
    m.el.dispatchEvent(new MouseEvent("pointerdown"));
    m.el.dispatchEvent(new MouseEvent("click"));
    expect(m.el.open).toBe(false);
    expect(closed).toEqual([1]);
  });

  it("the page's own state closes it too", () => {
    const { m, btn } = setup();
    m.sync(true, [btn("first")]);
    m.sync(false, []);
    expect(m.el.open).toBe(false);
  });
});
