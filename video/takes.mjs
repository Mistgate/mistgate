// The takes: what happens on screen, in page time. Each take is one continuous shot of the real UI on the mocks of
// mocks.mjs; the composition decides the camera, the speed and the captions. Buttons are found by the app's own words
// in the take's language (i18n.mjs), so one script films every language.
import { sub, t } from "./i18n.mjs";
import { person, world } from "./mocks.mjs";

const q = JSON.stringify;
/** The first element matching sel whose text is exactly text (or starts with it), optionally inside a dialog. */
const byText = (sel, text, { within = "document", prefix = false } = {}) =>
  `[...${within}.querySelectorAll(${q(sel)})].find((e) => ${prefix ? `e.textContent.trim().startsWith(${q(text)})` : `e.textContent.trim() === ${q(text)}`})`;
// the open modal: the last dialog that is a window (a toast is a small dialog too)
const dialog = `[...document.querySelectorAll("[role=dialog]")].filter((d) => d.getBoundingClientRect().height > 120).at(-1)`;
const inDialog = (sel) => `${dialog}?.querySelector(${q(sel)})`;
/** The input of a TextField by its label text. */
const field = (label) => `[...${dialog}.querySelectorAll("label")].find((l) => l.textContent.trim().startsWith(${q(label)}))?.parentElement.querySelector("input")`;

async function until(s, expr, sec = 20) {
  for (let i = 0; i < sec * 30; i++) {
    if (await s.evaluate(`!!(${expr})`)) return;
    await s.wait(1 / 30);
  }
  throw new Error(`waited in vain for ${expr}`);
}

/** The first element whose own text (spaces normalised: the page puts no-break spaces after short words) is text. */
const findText = (text) => `[...document.querySelectorAll("body *")].find((e) => [...e.childNodes].filter((n) => n.nodeType === 3).map((n) => n.textContent).join("").replace(/\\s+/g, " ").trim() === ${q(text.replace(/\s+/g, " "))})`;
/** findText, but only an element that is on screen (the person's page keeps a twin of some buttons for the other width). */
const findShown = (text) => `[...document.querySelectorAll("body *")].find((e) => e.getBoundingClientRect().width > 0 && [...e.childNodes].filter((n) => n.nodeType === 3).map((n) => n.textContent).join("").replace(/\\s+/g, " ").trim() === ${q(text.replace(/\s+/g, " "))})`;
/** Scrolls so the element's top sits at `at` of the viewport height. */
async function bring(s, expr, at = 0.15, sec = 1.0) {
  const r = await s.rect(expr);
  await s.scrollTo(Math.max(0, (await s.evaluate("window.scrollY")) + r.y - s.vp.height * at), sec);
}
const SUB = process.env.SUB_ORIGIN || "http://127.0.0.1:5294";
const phone = { width: 390, height: 844, dpr: 2, mobile: true };

export const takes = {
  // Profiles: a new MTProto profile in the wizard, Fake TLS as www.example.com, on every node and in the groups at once
  "tg-admin": {
    cursorFrom: { x: 1180, y: 820 },
    async run(s) {
      const w = world(s.lang);
      const T = (k, v) => t(s.lang, k, v);
      s.mock(w.table);
      // the button counts the nodes ("on 4 nodes": the plural endings differ by language): its words up to the number
      const create = T("wiz.create", { n: 4 }).split(" 4")[0];
      await s.open("/profiles", `document.querySelector("main h1")`);
      s.mark("list");
      await s.wait(1.0);
      await s.click(byText("main button, main a", T("profiles.create"), { prefix: true }), 1.0);
      await until(s, findText(T("wiz.t.tg.t")));
      await s.wait(0.5);
      s.mark("cards");
      const card = `${findText(T("wiz.t.tg.t"))}.closest("[role=radio]")`;
      await s.rectMark("cards", `${card}.parentElement`);
      await s.click(card, 0.9);
      await s.wait(0.6);
      await s.click(byText("main button", T("wiz.go")), 0.7);
      await until(s, `document.querySelector("main input")`);
      await s.wait(0.6);
      s.mark("form");
      await s.rectMark("formTop", `document.querySelector("main input").closest("section, div[class*=rounded]").parentElement`);
      await s.wait(1.4);
      // the settings: the name in the TLS handshake lives under "All settings"
      await s.click(byText("main button", T("wiz.all")), 0.9);
      const site = findText(T("profiles.f.mtproto.fake_tls.kind.enum.site"));
      await until(s, site, 3);
      await s.wait(0.4);
      await bring(s, site, 0.45, 0.6);
      await s.rectMark("tlsRow", `${site}.closest("[role=radio], button").parentElement.parentElement.parentElement`);
      s.mark("tls");
      await s.click(`${site}.closest("[role=radio], button")`, 0.9);
      await until(s, `[...document.querySelectorAll("main input")].length > 2`, 3);
      await s.wait(0.2);
      const field = `[...document.querySelectorAll("main input")].at(-1)`;
      await s.click(field, 0.5, 0.3);
      await s.type("www.example.com", 15);
      await s.wait(0.6);
      s.mark("typed");
      await s.rectMark("preview", `${findText("tg://proxy?server=node.example.com&port=443&secret=ee0123456789abcdef0123456789abcdef7777772e6578616d706c652e636f6d")}?.closest("section, div[class*=rounded]") ?? document.querySelector("main pre, main code")`);
      await s.wait(1.0);
      await s.click(byText("main button", T("wiz.go")), 0.8);
      // the nodes, then the people: everything is ticked already
      await until(s, findText("de1"), 5);
      await s.wait(1.2);
      s.mark("nodes");
      await s.click(byText("main button", T("wiz.go")), 0.8);
      await until(s, byText("main button", create, { prefix: true }), 5);
      await s.wait(1.2);
      s.mark("people");
      s.hold("ProfileService/CreateProfile", 8);
      await s.click(byText("main button", create, { prefix: true }), 0.9);
      s.mark("create");
      // made, added to the groups, put on the four nodes: the last page of the wizard
      await until(s, byText("main button, main a", T("wiz.open"), { prefix: true }), 12);
      await s.wait(0.8);
      s.mark("deployed");
      await s.wait(2.2);
    },
  },
  // Health: the open alerts, the doctor's fix for de1's journal, the doctor green
  "health-admin": {
    // at 2 the success effect of the fix (a field of dots over the whole window) is too big for the headless Chrome: it dies
    viewport: { width: 1600, height: 1000, dpr: 1.5, mobile: false },
    cursorFrom: { x: 1180, y: 820 },
    async run(s) {
      const w = world(s.lang);
      const T = (k, v) => t(s.lang, k, v);
      w.state.alerts = true;
      s.mock(w.table);
      await s.open("/health", `document.querySelector("main h1")`);
      const row = (node) => `[...document.querySelectorAll("main button")].find((e) => e.textContent.includes(${q(node)}) && e.getBoundingClientRect().width > 500)`;
      s.mark("list");
      await s.wait(1.6);
      await s.click(row("nl1"), 1.0, 0.3);
      await s.wait(0.6);
      s.mark("nl1");
      await s.wait(1.8);
      await s.click(row("de1"), 0.9, 0.3);
      await s.wait(0.6);
      s.mark("de1");
      await s.wait(1.4);
      s.hold("HealthService/ApplyFix", 20);
      await s.click(byText("main button", T("health.fix.journald_vacuum.label")), 0.9);
      await until(s, byText("[role=dialog] button", T("hl.fix.apply")), 5);
      await s.wait(0.4);
      s.mark("plan");
      await s.rectMark("dlgPlan", dialog);
      await s.wait(1.8);
      s.hold("HealthService/ApplyFix", 36);
      await s.click(byText("[role=dialog] button", T("hl.fix.apply")), 0.8);
      await s.wait(0.3);
      s.mark("fixing");
      await until(s, `!document.querySelector("[role=dialog]")`, 8);
      await s.wait(0.5);
      s.mark("fixed");
      await s.wait(2.4);
      s.mark("after");
      await s.click(byText("main [role=tab], main button", T("hl.tab.doctor"), { prefix: true }), 0.9);
      await s.wait(1.2);
      s.mark("doctor");
      await s.wait(2.2);
    },
  },
  // Users: a new person, carol, and the one link that is hers (the link, the QR code)
  "link-admin": {
    cursorFrom: { x: 1180, y: 780 },
    async run(s) {
      const w = world(s.lang);
      const T = (k, v) => t(s.lang, k, v);
      w.state.carol = false;
      s.mock(w.table);
      await s.open("/users", `document.querySelector("main h1")`);
      s.mark("list");
      await s.wait(1.2);
      await s.click(`[...document.querySelectorAll("main button")].find((b) => b.textContent.trim() === ${q(T("ov.btn.person"))} && b.getBoundingClientRect().top < 160)`, 1.0);
      await until(s, inDialog("input"));
      await s.wait(0.6);
      s.mark("form");
      await s.rectMark("dlgForm", dialog);
      await s.wait(0.8);
      await s.click(inDialog("input"), 0.6, 0.3);
      await s.type("carol", 9);
      await s.wait(1.0);
      await s.click(byText("[role=dialog] button", T("users.createBtn")), 0.8);
      await until(s, byText("[role=dialog] button", T("ul.act.link")));
      await s.wait(0.6);
      s.mark("added");
      await s.rectMark("dlgAdded", dialog);
      await s.wait(1.6);
      await s.click(byText("[role=dialog] button", T("ul.act.link")), 0.8);
      await until(s, `[...document.querySelectorAll("[role=dialog]")].some((d) => d.textContent.includes("Subscription link") || d.querySelector("svg"))`, 5);
      await s.wait(1.2);
      s.mark("link");
      await s.rectMark("dlgLink", dialog);
      await s.wait(1.6);
      await s.move(byText("[role=dialog] a, [role=dialog] button", T("users.openPage"), { prefix: true }), 0.8);
      s.mark("open");
      await s.wait(1.2);
    },
  },
  // carol's first visit to her page: Add a device, an app, one tap that puts every server into it, then her servers
  "link-phone": {
    viewport: phone,
    cursorFrom: { x: 300, y: 900 },
    async run(s) {
      const L = (k) => sub(s.lang, k);
      s.page((d) => person(s.lang, d, { name: "carol" }));
      await s.open(`/sub.html?case=first&lang=${s.lang}&as=ios`, `document.querySelector("h1, main")`, SUB);
      s.mark("top");
      await s.wait(1.6);
      await s.click(findShown(L("awgAdd")), 0.9);
      await until(s, findShown(L("adChoose")), 3);
      await s.wait(0.9);
      s.mark("add");
      await s.wait(1.0);
      await s.click(findShown(L("adChoose")), 0.8);
      await until(s, findShown(L("adNext")), 3);
      await s.wait(0.9);
      s.mark("app");
      await s.wait(0.9);
      await s.click(findShown(L("adNext")), 0.8);
      await until(s, findShown(L("addOne")), 3);
      await s.wait(0.9);
      s.mark("connect");
      await s.rectMark("addOne", findShown(L("addOne")));
      await s.wait(1.6);
      await s.click(findShown(L("awgDone")), 0.8);
      await s.wait(0.9);
      await bring(s, findShown(L("tabServers")), 0.05, 0.9);
      s.mark("servers");
      await s.wait(2.0);
    },
  },
  // alice's page with the Telegram proxy: the Telegram tab, a card per link, Open in Telegram
  "tg-phone": {
    viewport: phone,
    cursorFrom: { x: -100, y: -100 },
    async run(s) {
      const L = (k) => sub(s.lang, k);
      s.page((d) => person(s.lang, d, { telegram: true }));
      await s.open(`/sub.html?case=telegram&lang=${s.lang}&as=ios`, `document.querySelector("h1, main")`, SUB);
      s.mark("top");
      await s.wait(1.0);
      const tab = `[...document.querySelectorAll("button")].find((b) => [${q(L("tabTelegramShort"))}, ${q(L("tabTelegram"))}].some((w) => b.textContent.trim().startsWith(w)))`;
      await bring(s, tab, 0.12, 0.9);
      await s.click(tab, 0.6);
      await s.wait(0.9);
      s.mark("tg");
      await s.wait(2.4);
      const dev = `[...document.querySelectorAll("button")].find((b) => b.textContent.trim().startsWith(${q(L("tabDevices"))}))`;
      await s.click(dev, 0.7);
      await s.wait(0.9);
      s.mark("devices");
      await s.wait(1.8);
    },
  },
  // alice's page on her phone: her servers, Singapore among them
  "hero-phone": {
    viewport: phone,
    cursorFrom: { x: -100, y: -100 },
    async run(s) {
      s.page((d) => person(s.lang, d, { sg1: true }));
      await s.open(`/sub.html?case=return&lang=${s.lang}&as=ios`, `document.querySelector("h1, main")`, SUB);
      s.mark("top");
      await s.wait(1.4);
      // the Singapore card: the element with the country's name, up to its card
      const name = new Intl.DisplayNames([s.lang === "zh" ? "zh-CN" : s.lang], { type: "region" }).of("SG");
      const card = `(() => { let e = [...document.querySelectorAll("main *, body *")].find((x) => x.children.length === 0 && x.textContent.trim() === ${q(name)}); while (e && e.getBoundingClientRect().height < 50) e = e.parentElement; return e; })()`;
      const r = await s.rect(card);
      const y = (await s.evaluate("window.scrollY")) + r.y + r.h - s.vp.height * 0.82;
      await s.scrollTo(y, 1.6);
      await s.rectMark("sg", card);
      s.mark("sg");
      await s.wait(2.0);
    },
  },
  // Overview → New node → install sg1 over SSH (the window's four steps) → its Profiles tab → a profile put on it
  "hero-admin": {
    cursorFrom: { x: 1180, y: 780 },
    async run(s) {
      const w = world(s.lang);
      const T = (k, v) => t(s.lang, k, v);
      s.mock(w.table);
      // the node window (it has the rail): a popover of the country list is a dialog too
      const win = `document.querySelector("[role=dialog]:has(.an-m)")`;
      const inWin = (sel) => `${win}?.querySelector(${q(sel)})`;
      const winBtn = (text) => byText("[role=dialog] button", text, { within: win });
      const plain = "input:not([type=radio]):not([type=checkbox])";
      const label = (text) => `[...${win}.querySelectorAll("label")].find((l) => l.textContent.trim().startsWith(${q(text)}))?.parentElement.querySelector("input")`;
      await s.open("/", `document.querySelector("main h1")`);
      s.mark("overview");
      await s.rectMark("grid", `document.querySelector("main a[href*='/nodes/']").closest("section")`);
      await s.rectMark("addBtn", byText("main button", T("ov.btn.node")));
      await s.wait(1.2);
      await s.click(byText("main button", T("ov.btn.node")), 1.1);
      await until(s, inWin(plain));
      await s.wait(0.6);
      s.mark("modal");
      await s.rectMark("dlgServer", win);
      // step 1: the server's address, its name, the country
      await s.click(inWin(plain), 0.6, 0.3);
      await s.type("203.0.113.50", 13);
      await s.wait(0.2);
      await s.click(label(T("node.add.name")), 0.5, 0.4);
      await s.selectAll();
      await s.type("sg1", 10);
      await s.wait(0.2);
      await s.click(byText("[role=dialog] button", T("node.add.countryNone"), { within: win }), 0.5, 0.4);
      await until(s, `document.querySelector("[role=listbox]")`, 3);
      await s.wait(0.2);
      await s.type("SG", 6);
      await s.wait(0.3);
      await s.click(`document.querySelector("[role=listbox] [role=option]")`, 0.4);
      await s.wait(0.5);
      s.mark("server");
      await s.rectMark("dlgServerFilled", win);
      s.hold("ProvisioningService/GetSSHFingerprint", 14);
      await s.click(winBtn(T("node.ssh.checkServer")), 0.6);
      // step 2: the server's key, the way in (the panel's own key: no password)
      await until(s, inWin("input[type=checkbox]"));
      await s.wait(0.6);
      s.mark("key");
      await s.rectMark("dlgKey", win);
      await s.click(byText("label, [role=checkbox]", T("node.ssh.confirmKey"), { within: win }), 0.55);
      await s.wait(0.5);
      s.mark("way");
      await s.wait(1.0);
      s.hold("ProvisioningService/CheckSSH", 16);
      await s.click(winBtn(T("node.ssh.continue")), 0.6);
      // step 3: the check of the server, the plan of the install, the consent
      await until(s, inWin(".an-sum"), 10);
      await s.wait(0.6);
      s.mark("preflight");
      await s.rectMark("dlgCheck", win);
      await s.wait(1.2);
      await s.click(byText("[role=radio]", T("ns.mode.all"), { within: win, prefix: true }), 0.7);
      await s.wait(0.6);
      await s.click(byText("label, [role=checkbox]", T("win.node.agreeTitle"), { within: win, prefix: true }), 0.7);
      await s.wait(0.3);
      s.mark("agree");
      await s.click(winBtn(T("node.ssh.startInstall")), 0.6);
      // step 4: the install walks its stages; the page of the node comes next
      s.mark("install");
      await until(s, winBtn(T("node.ssh.openNode")), 60);
      s.mark("installed");
      await s.rectMark("dlgDone", win);
      await s.wait(1.2);
      await s.click(winBtn(T("win.node.addProfile")), 0.8);
      await until(s, `document.querySelector("main h1")?.textContent.includes("sg1") && ${byText("main button", T("node.profiles.add"))}`);
      await s.wait(0.8);
      s.mark("node");
      await s.rectMark("nodeHead", `document.querySelector("main h1").closest("div").parentElement.parentElement`);
      await s.wait(0.8);
      await s.click(byText("main button", T("node.profiles.add")), 0.8);
      await until(s, byText("[role=dialog] button", T("node.profiles.put")));
      await s.wait(0.6);
      s.mark("addProfile");
      await s.rectMark("dlgAdd", dialog);
      await s.wait(0.5);
      await s.click(`${dialog}.querySelector("input[type=checkbox], [role=checkbox]")`, 0.7);
      await s.wait(0.5);
      // the button counts what is ticked ("Put 1", in Turkish "1 kur")
      await s.click(`[...document.querySelectorAll("[role=dialog] button")].find((e) => e.textContent.toLowerCase().includes(${q(T("node.profiles.put").toLowerCase())}))`, 0.6);
      // the profile's card in the page (not in the dialog): the moment it is on the node
      const row = `[...document.querySelectorAll("main *")].find((e) => e.children.length === 0 && e.textContent.trim() === "VLESS · Reality" && !e.closest("[role=dialog]"))?.closest("section, article, li, [class*=rounded]")`;
      await until(s, row);
      s.mark("profileAdded");
      await s.rectMark("inbound", row);
      // the header's problem count follows a poll; the refetch on focus (an alt-tab) brings it now
      await s.wait(0.3);
      await s.evaluate(`document.dispatchEvent(new Event("visibilitychange"))`);
      await until(s, findText(T("health.ok")));
      s.mark("healthy");
      await s.wait(1.4);
    },
  },
};
