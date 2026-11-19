import { describe, expect, it } from "vitest";
import { chip, linkApp, qrOn } from "./connect";
import { appKey, appList, chosenApp, keyAppName } from "./logic";
import { data, dialog, state, text } from "./test-kit";
import { appTile, linkApps } from "./ways";

// The pieces of the link way of the "new device" window: the choice chip, the apps and their tiles, the steps of connecting.
// The window itself (the frame, the other steps) is add.test.ts.

const app = (name: string, platform = "windows", over: object = {}) => ({ platform, kind: "happ", name, download_url: "", add_url: "", description: "", recommended: false, ...over }) as ReturnType<typeof appList>[number];

describe("the chip of the board (.a-chip)", () => {
  it("an icon and a word; a radio that says whether it is chosen; a click calls what it was given", () => {
    let hit = 0;
    const on = chip({ ico: "phone", label: "iPhone", on: true, k: "x-ios", click: () => hit++ });
    const off = chip({ label: "Mac", on: false, k: "x-mac", click: () => hit++ });
    expect([on.getAttribute("role"), on.getAttribute("aria-checked"), on.dataset.k, on.type]).toEqual(["radio", "true", "x-ios", "button"]);
    expect([off.getAttribute("aria-checked"), off.querySelector("svg")]).toEqual(["false", null]);
    expect(text(on)).toBe("iPhone");
    on.click();
    off.click();
    expect(hit).toBe(2);
  });
});

describe("the apps of the link way", () => {
  it("the recommended app first; no key app among them; a platform with no app has none", () => {
    const d = data("first");
    expect(appList(d, "windows").map((x) => x.name)).toEqual(["kl!ck", "Happ", "AmneziaVPN"]);
    expect(appList(d, "windows", "happ").map((x) => x.name)).toEqual(["kl!ck", "Happ"]);
    expect(appList(d, "ios", "amnezia").map((x) => x.name)).toEqual(["AmneziaVPN"]);
    expect(linkApp({ d, s: state("windows") })?.name).toBe("kl!ck");
    expect(linkApp({ d, s: state("windows", { app: "happ|Happ" }) })?.name).toBe("Happ");
    expect(linkApp({ d, s: state("windows", { app: "amnezia|AmneziaVPN" }) })?.name).toBe("kl!ck"); // a key app never takes a link app's place
    expect(linkApp({ d: data("nolinux"), s: state("linux") })).toBeUndefined();
    expect(chosenApp(d, "ios", "gone|X")?.name).toBe("Happ");
    expect(appKey(appList(d, "ios")[0]!)).toBe("happ|Happ");
  });

  it("the way card names each app once: the platform's, else every link app the person may use", () => {
    expect(linkApps({ d: data("first"), s: state("windows") }).map((x) => x.name)).toEqual(["kl!ck", "Happ"]);
    expect(linkApps({ d: data("first"), s: state("linux") }).map((x) => x.name)).toEqual(["Happ", "kl!ck"]); // the recommended ones first, in the order of the settings
    const twice = data("first", (d) => d.apps.push(app("Happ", "windows")));
    expect(linkApps({ d: twice, s: state("windows") }).map((x) => x.name)).toEqual(["kl!ck", "Happ"]);
  });

  it("the name of the key app", () => {
    const d = data("multi");
    expect(appList(d, "windows").map((a) => a.name)).toEqual(["kl!ck", "AmneziaVPN", "Example Client"]);
    expect(keyAppName(d, "windows")).toBe("AmneziaVPN");
  });
});

describe("the tile of an app", () => {
  const d = data("first", (x) => (x.title = "Mistgate"));
  const list = [app("kl!ck"), app("Mistgate"), app("Happ"), app("Example Client"), app("Fourth")];

  it("the instance's own app wears its logo (an image, never inlined markup); the others their letters in sky, lav, mint, sky", () => {
    const tiles = list.map((_, i) => appTile(d, list, i));
    expect(tiles.map((x) => x.tagName)).toEqual(["SPAN", "IMG", "SPAN", "SPAN", "SPAN"]);
    expect(tiles.map((x) => x.className)).toEqual(["a-app sky", "a-app", "a-app lav", "a-app mint", "a-app sky"]);
    expect(tiles.map(text)).toEqual(["k!", "", "H", "E", "F"]);
    expect((tiles[1] as HTMLImageElement).src).toMatch(/^data:image\/svg\+xml,/);
    expect(tiles[1]!.getAttribute("alt")).toBe("");
    expect(tiles[0]!.getAttribute("aria-hidden")).toBe("true");
  });

  it("a name that is not Latin or has no letters still gets a mark", () => {
    const odd = [app("Ключ"), app("!!!"), app("日本")];
    expect(odd.map((_, i) => text(appTile(d, odd, i)))).toEqual(["К", "!", "日"]);
  });
});

describe("the steps of connecting", () => {
  const conn = (name: string, platform: "ios" | "windows" | "linux" = "windows", f: Parameters<typeof data>[1] = () => {}, over: object = {}) => dialog(data(name, f), { platform, amz: { pane: "conn" }, over });

  it("the QR code is asked for by the page's option and a link app, nothing else", () => {
    expect(qrOn({ d: data("first") })).toBe(true);
    expect(qrOn({ d: data("first", (d) => (d.options.show_qr = false)) })).toBe(false);
    expect(qrOn({ d: data("first", (d) => (d.subscription_url = "")) })).toBe(false);
    expect(qrOn({ d: data("keys-only") })).toBe(false);
    expect(qrOn({ d: data("plain") })).toBe(false);
  });

  it("the steps are numbered as they are: install (only with a download), add the subscription", () => {
    const { box } = conn("first");
    expect([...box.querySelectorAll(".a-steps > .a-st")].map((x) => text(x.querySelector(".a-n")))).toEqual(["1", "2"]);
    const noGet = conn("first", "windows", (d) => d.apps.forEach((x) => (x.download_url = ""))).box;
    expect([...noGet.querySelectorAll(".a-steps > .a-st")].map((x) => text(x.querySelector(".a-n")))).toEqual(["1"]);
    expect(text(noGet.querySelector(".a-st .tx"))).toContain("Добавьте подписку");
  });

  it("the chosen app is the one the steps are about", () => {
    const { box, label } = conn("first", "windows", () => {}, { app: "happ|Happ" });
    expect(label).toBe("Подключите Happ");
    expect(text(box.querySelector(".a-st .tx"))).toContain("Установите Happ");
    expect(box.querySelector("a[href^='happ://add/']")).not.toBeNull();
  });

  it("a phone has no QR side of its own to scan (the phone is the device): the side is marked for the stylesheet to hide there", () => {
    const side = conn("first", "ios").box.querySelector(".a-side");
    expect(side?.classList.contains("pg")).toBe(true);
  });

  it("the link is never written out, in any language", () => {
    const d = data("first");
    for (const lang of ["ru", "en", "zh", "fa", "tr"] as const) {
      const box = dialog(d, { lang, platform: "windows", amz: { pane: "conn" } }).box;
      expect(box.textContent, lang).not.toContain(d.subscription_url);
      expect(box.querySelector(".a-st a.u-btn.pri")?.getAttribute("href"), lang).toMatch(/^klick:\/\/add/);
    }
  });

  it("the footer's words follow the language", () => {
    expect(text(dialog(data("first"), { lang: "en", amz: { pane: "conn" } }).box.querySelector(".a-f"))).toBe("The device shows up in the list after its first updateDone");
  });
});
