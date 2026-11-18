// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { amzActions, type Api } from "./amz-actions";
import { dialogOpen, dialogView } from "./dialogs";
import { devPage, sampleQr } from "./dev-data";
import { devLockScene, devScene } from "./dev-scene";
import { dnsActions } from "./dns-actions";
import { dict } from "./i18n";
import { awgConfig, awgDevice, asPlatform, mainProfile, normalize, safeUrl } from "./logic";
import { lockView, type LockState } from "./lock";
import { newAmzState, newDnsState, type Actions, type Ctx, type State, type Tab } from "./state";
import { actions, plain, text } from "./test-kit";
import { annKey, openTab, view } from "./view";

// The scenes: every state of the page and every window opens on a page of its own, by the parameters of the URL (dev-scene.ts), on
// the real parts. This is the list of those pages. `w` is the width of the window to look at it in (1280 a computer, 390 a phone);
// `tab` the tab that is open (the first when not said), `stage` the window that is open (null: none), `has` / `lacks` pieces of the page,
// `in` / `notIn` pieces of the window.

type Scene = { name: string; q: string; w: 1280 | 390; tab?: Tab; stage?: string | null; has?: string[]; lacks?: string[]; in?: string[]; notIn?: string[] };
const S = (name: string, q: string, w: 1280 | 390, o: Partial<Scene> = {}): Scene => ({ name, q, w, ...o });

export const scenes: Scene[] = [
  // ---- the states of the subscription ----
  S("active", "case=return", 1280, { has: [".u-tabs", ".u-hero-c", ".u-qr"], lacks: [".u-alert", ".u-ann"] }),
  S("active, a phone", "case=return", 390, { has: [".u-tabs", ".u-hero-c.only-m"] }),
  S("first visit", "case=first", 1280, { has: [".u-alert", ".u-ann"] }),
  S("first visit, a phone", "case=first", 390, { has: [".u-alert"] }),
  S("ends soon", "case=soon", 1280, { has: [".u-alert.warn", ".u-hero .u-dot.warn"] }),
  S("ends soon, a phone", "case=soon", 390, { has: [".u-alert.warn"] }),
  S("traffic used up, with the day it resets", "case=quota", 1280, { has: [".u-hero.solo", ".u-alert.warn"], lacks: [".u-tabs", ".u-qr", ".u-ann"] }),
  S("traffic used up, no reset", "case=quota-plain", 1280, { has: [".u-hero.solo", ".u-alert.warn"], lacks: [".u-tabs"] }),
  S("traffic used up, a phone", "case=quota", 390, { has: [".u-hero.solo"] }),
  S("ended", "case=expired", 1280, { has: [".u-hero.solo", ".u-alert", ".u-hero .u-dot.off"], lacks: [".u-tabs", ".u-ask"] }),
  S("ended, no support", "case=expired-plain", 1280, { has: [".u-alert"], lacks: [".u-alert a", ".u-ask"] }),
  S("ended, a phone", "case=expired", 390, { has: [".u-hero.solo"] }),
  S("turned off", "case=disabled", 1280, { has: [".u-hero.solo", ".u-alert"], lacks: [".u-tabs"] }),
  S("turned off, a phone", "case=disabled", 390, { has: [".u-hero.solo"] }),
  S("no end, no limit, no support, no QR", "case=plain", 1280, { lacks: [".u-qr", ".u-ask", ".u-ann", "header a"] }),
  // ---- the announcement, the support, the QR code ----
  S("announcement", "case=first", 1280, { has: [".u-ann"] }),
  S("announcement, a phone", "case=first", 390, { has: [".u-ann"] }),
  S("no announcement", "case=return", 1280, { lacks: [".u-ann"] }),
  S("long announcement, a long name", "case=long", 1280, { has: [".u-ann .u-ic"] }),
  S("long announcement, a phone", "case=long", 390, { has: [".u-ann"] }),
  S("no support link", "case=nosupport", 1280, { lacks: [".u-ask", "header a"] }),
  S("no QR code", "case=noqr", 1280, { has: [".u-hero-c"], lacks: [".u-qr"] }),
  // ---- the ways: the keys alone, the apps alone, both ----
  S("keys only", "case=keys-only", 1280, { has: [".u-tabs"] }),
  S("apps only", "case=happ&tab=devices", 1280, { tab: "devices" }),
  S("both ways", "case=both&tab=devices", 1280, { tab: "devices" }),
  // ---- the places of the devices ----
  S("0 of 20 devices", "case=dev-0&tab=devices", 1280, { tab: "devices" }),
  S("3 of 20 devices", "case=dev-3&tab=devices", 1280, { tab: "devices" }),
  S("16 of 20 devices, folded", "case=dev-16&tab=devices", 1280, { tab: "devices", has: [".u-more"] }),
  S("16 of 20 devices, unfolded", "case=dev-16&tab=devices&more=1", 1280, { tab: "devices" }),
  S("16 of 20 devices, a phone", "case=dev-16&tab=devices", 390, { tab: "devices" }),
  S("20 of 20 devices", "case=dev-20&tab=devices", 1280, { tab: "devices" }),
  S("devices, no limit", "case=dev-nolimit&tab=devices", 1280, { tab: "devices" }),
  // ---- the servers ----
  S("2 servers", "case=servers-2", 1280, { has: [".u-tbl"] }),
  S("11 servers", "case=servers-11", 1280, { has: [".u-tbl"] }),
  S("11 servers, a phone", "case=servers-11", 390, { has: [".u-tbl"] }),
  S("30 servers", "case=servers-30", 1280, { has: [".u-tbl"] }),
  S("30 servers, a phone", "case=servers-30", 390, { has: [".u-tbl"] }),
  // ---- the other tabs ----
  S("Telegram proxies", "case=telegram&tab=telegram", 1280, { tab: "telegram" }),
  S("Telegram proxies, the second one", "case=telegram&tab=telegram&proxy=2", 1280, { tab: "telegram" }),
  S("Telegram proxies, a phone", "case=telegram&tab=telegram", 390, { tab: "telegram" }),
  S("Telegram proxies, none yet", "case=telegram-soon&tab=telegram", 1280, { tab: "telegram" }),
  S("TrustTunnel", "case=trusttunnel&tab=trusttunnel", 1280, { tab: "trusttunnel" }),
  S("TrustTunnel, the second link", "case=trusttunnel&tab=trusttunnel&proxy=2", 1280, { tab: "trusttunnel" }),
  S("TrustTunnel, a phone", "case=trusttunnel&tab=trusttunnel", 390, { tab: "trusttunnel" }),
  S("TrustTunnel, none yet", "case=trusttunnel-soon&tab=trusttunnel", 1280, { tab: "trusttunnel" }),
  S("a tab this person has not got is the servers", "case=return&tab=telegram", 1280, { tab: "servers" }),
  // ---- the window "New device", step by step ----
  S("new device, the way", "case=both&sheet=pick&as=windows", 1280, { stage: "way" }),
  S("new device, the way, a phone", "case=both&sheet=pick&as=ios", 390, { stage: "way" }),
  S("new device, the app", "case=both&sheet=link&as=windows", 1280, { stage: "app" }),
  S("new device, the app, a phone", "case=both&sheet=link&as=ios", 390, { stage: "app" }),
  S("new device, connecting", "case=both&sheet=conn&as=windows", 1280, { stage: "conn" }),
  S("new device, connecting, a phone", "case=both&sheet=conn&as=android", 390, { stage: "conn" }),
  S("new device, the key form", "case=both&sheet=key&as=windows", 1280, { stage: "key" }),
  S("new device, the key form, a phone", "case=both&sheet=key&as=ios", 390, { stage: "key" }),
  S("new device, no place left (the link refuses)", "case=dev-20&sheet=pick&as=windows", 1280, { stage: "full" }),
  S("new device, no place left (said by the panel)", "case=return&sheet=full&as=windows", 1280, { stage: "full" }),
  S("new device, no place left, a phone", "case=dev-20&sheet=pick&as=ios", 390, { stage: "full" }),
  // ---- the key, in each of its kinds ----
  S("the key just made, on a phone", "case=both&sheet=made&as=ios", 390, { stage: "ready:dev_new:created" }),
  S("the key just made, on a computer", "case=both&sheet=made&as=windows", 1280, { stage: "ready:dev_new:created" }),
  S("the key of a device, on this phone", "case=amnezia&key=d3&as=android", 390, { stage: "ready:d3:shown" }),
  S("the key of a device, on this computer", "case=amnezia&key=d3&as=windows", 1280, { stage: "ready:d3:shown" }),
  S("the key of a device, scanned from another", "case=amnezia&key=d3&as=macos", 1280, { stage: "ready:d3:shown" }),
  S("the key of a device, the second server", "case=amnezia&key=d3&country=1&as=windows", 1280, { stage: "ready:d3:shown" }),
  S("the new key of a stale device", "case=stale&renew=d3&as=android", 390, { stage: "ready:d3:renew" }),
  S("the new key of a stale device, a computer", "case=stale&renew=d3&as=windows", 1280, { stage: "ready:d3:renew" }),
  // ---- the small windows of a device ----
  S("the menu of a key", "case=amnezia&menu=d3", 1280, { stage: "menu:d3", tab: "servers" }),
  S("the menu of a key, a phone", "case=amnezia&menu=d3&tab=devices", 390, { stage: "menu:d3", tab: "devices" }),
  S("the menu of an app's device", "case=hwid-full&menu=l1&tab=devices", 1280, { stage: "menu:l1", tab: "devices" }),
  S("the menu of an app's device, a phone", "case=hwid-full&menu=l1&tab=devices", 390, { stage: "menu:l1", tab: "devices" }),
  S("the name of a key", "case=amnezia&rename=d3&tab=devices", 1280, { stage: "rename:d3", tab: "devices" }),
  S("the name of an app's device, a phone", "case=hwid-full&rename=l1&tab=devices", 390, { stage: "rename:l1", tab: "devices" }),
  S("remove a key", "case=amnezia&ask=remove:d3&tab=devices", 1280, { stage: "confirm:remove:d3", tab: "devices" }),
  S("remove an app's device, a phone", "case=hwid-full&ask=remove:l1&tab=devices", 390, { stage: "confirm:remove:l1", tab: "devices" }),
  S("replace a key", "case=amnezia&ask=rotate:d3&tab=devices", 1280, { stage: "confirm:rotate:d3", tab: "devices" }),
  S("replace a key, a phone", "case=amnezia&ask=rotate:d3&tab=devices", 390, { stage: "confirm:rotate:d3", tab: "devices" }),
  S("a new key is needed", "case=stale&stale=d3&tab=devices", 1280, { stage: "stale:d3", tab: "devices" }),
  S("a new key is needed, a phone", "case=stale&stale=d3&tab=devices", 390, { stage: "stale:d3", tab: "devices" }),
  // ---- DNS ----
  S("DNS of a server", "case=dns-both&dns=de1&dnsall=0", 1280, { stage: "dns:pick" }),
  S("DNS of all servers", "case=dns-both&dns=de1&dnsall=1", 1280, { stage: "dns:pick" }),
  S("DNS of all servers, a phone", "case=dns-both&dns=de1&dnsall=1", 390, { stage: "dns:pick" }),
  S("DNS of the link apps", "case=dns-both&dns=link", 1280, { stage: "dns:pick" }),
  S("DNS of the link apps, a person with no keys", "case=dns-link&dns=link", 1280, { stage: "dns:pick" }),
  S("DNS of a server, a phone", "case=dns-both&dns=nl1&dnsall=0", 390, { stage: "dns:pick" }),
  S("DNS changed, with the keys to get again", "case=stale-dns&dns=nod_fra&dnsall=0&dnsdone=1", 1280, { stage: "dns:done", in: ['[data-k="dlg-get-d3"]'] }),
  S("DNS changed, with the keys to get again, a phone", "case=stale-dns&dns=nod_fra&dnsall=0&dnsdone=1", 390, { stage: "dns:done" }),
  S("DNS changed, no keys to get again", "case=dns-both&dns=de1&dnsall=0&dnsdone=1", 1280, { stage: "dns:done", notIn: [".a-opt"] }),
  S("DNS changed for all but one server", "case=dns-both&dns=de1&dnsall=1&dnsdone=1&dnsskip=nl1", 1280, { stage: "dns:done", in: [".u-hint"] }),
  S("DNS of the link apps changed", "case=dns-both&dns=link&dnsdone=1", 1280, { stage: "dns:done" }),
  // ---- the page's language ----
  S("English", "case=return&lang=en", 1280, {}),
  S("Chinese", "case=return&lang=zh", 1280, {}),
  S("Persian, right to left", "case=return&lang=fa", 1280, {}),
  S("Turkish", "case=return&lang=tr", 1280, {}),
  S("Persian, a phone, the menu of a key", "case=amnezia&lang=fa&menu=d3&tab=devices", 390, { stage: "menu:d3", tab: "devices" }),
  // ---- the password page ----
  S("the password page", "case=locked", 1280, {}),
  S("the password page, a phone", "case=locked", 390, {}),
  S("the password page, a wrong password", "case=locked&lockerr=wrong", 1280, {}),
  S("the password page, too short", "case=locked&lockerr=short", 390, {}),
  S("the password page, a letter of another keyboard", "case=locked&lockerr=latin", 1280, {}),
  S("the password page, too many tries", "case=locked&lockerr=later", 1280, {}),
  S("the password page, no network", "case=locked&lockerr=net", 390, {}),
];

// ---- the page as main.ts puts it together ----

const cfg = (node: string, cc: string) =>
  awgConfig({ node_id: `nod_${node}`, label: cc, country_code: cc, version: "3.1", conf: "[Interface]\nPrivateKey = x\n", vpn_key: "vpn://k", filename: `mistgate-${cc.toLowerCase()}.conf`, warnings: [], qr: sampleQr });
const api: Api = {
  addDevice: async (_e, b) => ({ device: awgDevice({ id: "dev_new", platform: b.platform, label: b.label, profile_id: b.profile_id, profile_name: "Main", version: "3.1" }), configs: [cfg("de1", "DE")] }),
  getConfigs: async () => ({ configs: [cfg("de1", "DE"), cfg("fi1", "FI")] }),
  rotateKey: async () => ({ configs: [cfg("de1", "DE")] }),
  revokeDevice: async () => {},
  renameDevice: async (_e, id, label) => ({ device: awgDevice({ id, label, platform: "android", profile_id: "p31" }), configs: [] }),
};
const dnsApi = { setDns: async () => ({ server: undefined, stale_devices: [] }) };
const settle = () => new Promise((r) => setTimeout(r, 0));

/** What main.ts builds for the URL: the data of the case, the state, the page's actions, the scene put in place. */
async function open(q: string) {
  const params = new URLSearchParams(q);
  const d = normalize(devPage(q));
  const want = asPlatform(params.get("as"));
  const here = want ?? "";
  const st: State = {
    lang: d.lang,
    tab: "servers",
    platform: want ?? "windows",
    detected: want,
    app: "",
    theme: "auto",
    devicesOpen: false,
    annClosed: false,
    returning: !q.includes("case=first"),
    amz: newAmzState(here || "other", mainProfile(d.amnezia?.profiles ?? []), here),
    dns: newDnsState(),
  };
  const a: Actions = { ...actions(), amz: amzActions({ data: d, st, render: () => {}, api, reveal: () => {}, focus: () => {} }), dns: dnsActions({ data: d, st, render: () => {}, api: dnsApi }) };
  devScene(params, d, st, a);
  await settle();
  const c: Ctx = { d, s: st, a, t: dict[st.lang], support: d.options.show_support ? safeUrl(d.support_url) : "" };
  return { d, st, a, c, params };
}

const known = ["case", "lang", "as", "theme", "tab", "more", "proxy", "sheet", "key", "country", "renew", "menu", "rename", "ask", "stale", "dns", "dnsall", "dnsdone", "dnsskip", "lockerr", "fail"];

describe("the scenes of the page", () => {
  it("each one names a case that exists and parameters the page reads", () => {
    for (const s of scenes) {
      const p = new URLSearchParams(s.q);
      expect(p.get("case"), s.name).toBeTruthy();
      expect(devPage(s.q).user.name, s.name).toBeDefined();
      for (const k of p.keys()) expect(known, `${s.name}: ${k}`).toContain(k);
    }
    expect(new Set(scenes.map((s) => s.name)).size).toBe(scenes.length);
  });

  describe.each(scenes.filter((s) => !s.q.includes("lockerr") && !s.q.includes("case=locked")))("$name ($w px)", (s) => {
    it("opens the tab and the window it names, and every part draws", async () => {
      const { c } = await open(s.q);
      expect(openTab(c)).toBe(s.tab ?? "servers");
      expect(dialogOpen(c)).toBe(!!s.stage);
      const page = view(c.d, c.s, c.a);
      for (const sel of s.has ?? []) expect(page.querySelector(sel), `has ${sel}`).not.toBeNull();
      for (const sel of s.lacks ?? []) expect(page.querySelector(sel), `lacks ${sel}`).toBeNull();
      if (s.stage) {
        const dlg = dialogView(c);
        expect(dlg.stage).toBe(s.stage);
        expect(dlg.label).not.toBe("");
        expect(dlg.content.length).toBeGreaterThan(0);
        const box = document.createElement("div");
        box.append(...(dlg.content as Node[]));
        expect(text(box)).not.toBe("");
        for (const sel of s.in ?? []) expect(box.querySelector(sel), `in ${sel}`).not.toBeNull();
        for (const sel of s.notIn ?? []) expect(box.querySelector(sel), `notIn ${sel}`).toBeNull();
      }
    });
  });

  it("each state of the subscription says its own word", async () => {
    const word = async (q: string) => {
      const { c } = await open(q);
      return text(view(c.d, c.s, c.a).querySelector(".u-hero .u-status b"));
    };
    expect(await word("case=return")).toBe("Активна");
    expect(await word("case=first")).toBe("Активна");
    expect(await word("case=soon")).toMatch(/^Заканчивается через \d+ дн/);
    expect(await word("case=quota")).toBe("Трафик исчерпан");
    expect(await word("case=expired")).toBe("Закончилась");
    expect(await word("case=disabled")).toBe("Отключена");
  });

  it("the sizes of the lists are the ones the scene says", async () => {
    for (const [n, count] of [["servers-2", 2], ["servers-11", 11], ["servers-30", 30]] as const) {
      const { c } = await open(`case=${n}`);
      expect(c.d.servers).toHaveLength(count);
      expect(view(c.d, c.s, c.a).querySelectorAll(".u-tr.only-w:not(.u-th)")).toHaveLength(count);
    }
    for (const [n, used, limit] of [["dev-0", 0, 20], ["dev-3", 3, 20], ["dev-16", 16, 20], ["dev-20", 20, 20], ["dev-nolimit", 5, 0]] as const) {
      const { d } = await open(`case=${n}`);
      expect([d.user.devices_used, d.user.device_limit, d.devices.length], n).toEqual([used, limit, used]);
    }
  });

  it("the language of the URL wins over the data's", async () => {
    const { c } = await open("case=return&lang=fa");
    expect(c.s.lang).toBe("fa");
    expect(view(c.d, c.s, c.a).querySelector(".u-status b")?.textContent).toBe(dict.fa.chip.active);
  });

  it("the key the URL asks for comes from the call, the one just made is a new device", async () => {
    const { st, d } = await open("case=both&sheet=made&as=ios");
    expect(st.amz.created).toBe("dev_new");
    expect(d.amnezia?.devices.map((x) => x.id)).toContain("dev_new");
  });

  it("a closed announcement is remembered by its key, the scene opens none closed", async () => {
    const { d, st } = await open("case=first");
    expect(st.annClosed).toBe(false);
    expect(annKey(d.announcement)).toMatch(/^ann:/);
  });
});

describe("the password page scenes", () => {
  const open2 = (q: string) => {
    const d = normalize(devPage(q));
    const st: LockState = { lang: d.lang, pw: "", busy: false, error: "", note: "" };
    devLockScene(new URLSearchParams(q), st);
    const t = dict[st.lang];
    return { d, st, t, el: lockView(d, st, { lang: () => {}, input: () => {}, submit: () => {} }) };
  };

  it("the plain page has no error and an empty field", () => {
    const { el, st } = open2("case=locked");
    expect(st.error).toBe("");
    expect(el.querySelector(".a-err")).toBeNull();
    expect(el.querySelector<HTMLInputElement>("#pw")?.value).toBe("");
  });

  it("each error is the one of its kind, under the field", () => {
    const err = (k: string) => open2(`case=locked&lockerr=${k}`);
    expect(text(err("wrong").el.querySelector(".a-err"))).toContain(err("wrong").t.dgLockWrong(2));
    expect(text(err("short").el.querySelector(".a-err"))).toContain(err("short").t.lockShort(3));
    expect(text(err("later").el.querySelector(".a-err"))).toContain(err("later").t.lockLater(10));
    expect(text(err("net").el.querySelector(".a-err"))).toContain(err("net").t.lockNet);
    expect(err("latin").el.querySelector(".a-err")).toBeNull();
    expect(text(err("latin").el.querySelector("#pw-note"))).toBe(plain(err("latin").t.lockLatin));
    expect(err("wrong").el.querySelector("#pw")?.getAttribute("aria-invalid")).toBe("true");
  });

  it("the language of the URL is the form's", () => {
    expect(open2("case=locked&lockerr=wrong&lang=en").st.lang).toBe("en");
  });
});
