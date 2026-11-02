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
/** Scrolls so the element's top sits at `at` of the viewport height. */
async function bring(s, expr, at = 0.15, sec = 1.0) {
  const r = await s.rect(expr);
  await s.scrollTo(Math.max(0, (await s.evaluate("window.scrollY")) + r.y - s.vp.height * at), sec);
}
const SUB = process.env.SUB_ORIGIN || "http://127.0.0.1:5294";
const phone = { width: 390, height: 844, dpr: 2, mobile: true };

export const takes = {
  // Profiles: a new MTProto profile, Fake TLS as www.example.com, on every node and in the groups at once
  "tg-admin": {
    cursorFrom: { x: 1180, y: 820 },
    async run(s) {
      const w = world(s.lang);
      const T = (k, v) => t(s.lang, k, v);
      s.mock(w.table);
      await s.open("/profiles", `document.querySelector("main h1")`);
      s.mark("list");
      await s.wait(1.0);
      await s.click(byText("main button, main a", T("profiles.create"), { prefix: true }), 1.0);
      await until(s, findText("MTProto"));
      await s.wait(0.5);
      s.mark("cards");
      await s.rectMark("cards", `${findText("MTProto")}.closest("[role=radio]").parentElement`);
      await s.click(findText("MTProto"), 0.9);
      await s.wait(0.5);
      await s.click(byText("main button", T("profiles.pick.continue"), { prefix: true }), 0.7);
      await until(s, `document.querySelector("main input")`);
      await s.wait(0.6);
      s.mark("form");
      await s.rectMark("formTop", `document.querySelector("main input").closest("section, div[class*=rounded]").parentElement`);
      await s.wait(2.4);
      // the segmented choice of the Fake TLS name: its second option, "Another site", is the button before "Create"
      const buttons = `[...document.querySelectorAll("main button, main [role=radio]")]`;
      await s.rectMark("tlsRow", `${buttons}.at(-2).parentElement.parentElement.parentElement`);
      s.mark("tls");
      await s.click(`${buttons}.at(-2)`, 0.9);
      await until(s, `[...document.querySelectorAll("main input")].length > 2`, 3);
      await s.wait(0.2);
      // the new Site field opens under the form's sticky footer: bring it up first, as a person would scroll
      const site = `[...document.querySelectorAll("main input")].at(-1)`;
      const r = await s.rect(site);
      await s.scrollTo((await s.evaluate("window.scrollY")) + Math.max(0, r.y + r.h - s.vp.height * 0.72), 0.6);
      await s.rectMark("site", `${site}.closest("div").parentElement.parentElement`);
      await s.click(site, 0.5, 0.3);
      await s.type("www.example.com", 15);
      await s.wait(0.6);
      s.mark("typed");
      await s.rectMark("preview", `${findText("tg://proxy?server=node.example.com&port=443&secret=ee0123456789abcdef0123456789abcdef7777772e6578616d706c652e636f6d")}?.closest("section, div[class*=rounded]") ?? document.querySelector("main pre, main code")`);
      await s.wait(1.0);
      s.hold("ProfileService/CreateProfile", 8);
      await s.click(`${buttons}.at(-1)`, 0.8);
      s.mark("create");
      // made, added to the groups, put on the four nodes: the profile's page opens
      await until(s, `location.pathname.includes("/profiles/prf_tg") && document.querySelector("main h1")`, 10);
      await s.wait(0.8);
      s.mark("deployed");
      await s.rectMark("where", `document.querySelector("main h1").closest("div").parentElement.parentElement`);
      await s.wait(2.2);
    },
  },
  // Health: the folded alerts, one opened, the doctor's fix for de1's journal, the check green
  "health-admin": {
    cursorFrom: { x: 1180, y: 820 },
    async run(s) {
      const w = world(s.lang);
      const T = (k, v) => t(s.lang, k, v);
      w.state.alerts = true;
      s.mock(w.table);
      await s.open("/health", `document.querySelector("main h1")`);
      const row = (node) => `[...document.querySelectorAll("main button")].find((e) => e.textContent.includes(${q(node)}) && e.getBoundingClientRect().width > 600)`;
      const card = (node) => `${row(node)}.parentElement`;
      s.mark("list");
      await s.rectMark("alerts", `${row("de1")}.parentElement.parentElement`);
      await s.wait(1.4);
      await s.click(row("nl1"), 1.0, 0.3);
      await s.wait(0.6);
      s.mark("nl1");
      await s.rectMark("nl1Card", card("nl1"));
      await s.wait(1.8);
      await s.click(row("de1"), 0.9, 0.3);
      await s.wait(0.6);
      s.mark("de1");
      await s.rectMark("de1Card", card("de1"));
      await s.wait(1.4);
      s.hold("HealthService/ApplyFix", 20);
      await s.click(byText("main button", T("health.fix.journald_vacuum.label")), 0.8);
      await until(s, byText("[role=dialog] button", T("hl.fix.apply")), 5);
      await s.wait(0.4);
      s.mark("plan");
      await s.rectMark("dlgPlan", dialog);
      await s.wait(1.6);
      s.hold("HealthService/ApplyFix", 36);
      await s.click(byText("[role=dialog] button", T("hl.fix.apply")), 0.8);
      await s.wait(0.3);
      s.mark("fixing");
      await until(s, findText(T("hl.fix.fixed")), 6);
      s.mark("fixed");
      await s.rectMark("fixedCard", card("de1"));
      await s.wait(2.2);
      s.mark("after");
      await s.wait(0.8);
      await s.click(byText("main [role=tab], main button", T("hl.tab.doctor"), { prefix: true }), 0.9);
      await s.wait(1.0);
      s.mark("doctor");
      await s.wait(2.0);
    },
  },
  // Users: a new person, carol, and the one link that is hers
  "link-admin": {
    cursorFrom: { x: 1180, y: 780 },
    async run(s) {
      const w = world(s.lang);
      const T = (k, v) => t(s.lang, k, v);
      w.state.carol = false;
      s.mock(w.table);
      await s.open("/users", `document.querySelector("main h1")`);
      s.mark("list");
      await s.rectMark("table", `document.querySelector("main table, main [role=table], main ul")`);
      await s.wait(1.0);
      await s.click(byText("main button", T("users.create"), { prefix: true }), 1.0);
      await until(s, inDialog("input"));
      await s.wait(0.4);
      s.mark("form");
      await s.rectMark("dlgForm", dialog);
      await s.click(inDialog("input"), 0.6, 0.3);
      await s.type("carol", 9);
      await s.wait(0.6);
      await s.click(byText("[role=dialog] button", T("users.createBtn")), 0.8);
      await until(s, byText("[role=dialog] a", T("users.openPage"), { prefix: true }));
      await s.wait(0.5);
      s.mark("link");
      await s.rectMark("dlgLink", dialog);
      await s.wait(1.5);
      await s.move(byText("[role=dialog] a", T("users.openPage"), { prefix: true }), 0.8);
      s.mark("open");
      await s.wait(1.0);
    },
  },
  // carol's first visit to her page: one tap adds the link, a QR code takes the page to another device, her servers and devices
  "link-phone": {
    viewport: phone,
    cursorFrom: { x: 300, y: 900 },
    async run(s) {
      const L = (k) => sub(s.lang, k);
      s.page((d) => person(s.lang, d, { name: "carol" }));
      await s.open(`/sub.html?case=first&lang=${s.lang}&as=ios`, `document.querySelector("h1, main")`, SUB);
      s.mark("top");
      await s.wait(1.2);
      await bring(s, findText(L("howT")), 0.12, 1.1);
      s.mark("how");
      await s.rectMark("addOne", findText(L("addOne")));
      await s.wait(1.4);
      await bring(s, findText(L("qrOtherT")), 0.25, 0.9);
      await s.click(findText(L("qrOtherT")), 0.4);
      await until(s, `${findText(L("qrOtherT"))}.closest("section, details, div[class]").parentElement.querySelector("svg rect, canvas, img")`, 3);
      await s.wait(0.5);
      s.mark("qr");
      await s.wait(1.8);
      await bring(s, findText(L("serversT")), 0.06, 1.0);
      s.mark("servers");
      await s.wait(1.4);
      await bring(s, findText(L("devicesT")), 0.08, 0.9);
      s.mark("devices");
      await s.wait(1.6);
    },
  },
  // alice's page with the Telegram proxy: a card per link, Open in Telegram; the proxy among her devices
  "tg-phone": {
    viewport: phone,
    cursorFrom: { x: -100, y: -100 },
    async run(s) {
      const L = (k) => sub(s.lang, k);
      s.page((d) => person(s.lang, d, { telegram: true }));
      await s.open(`/sub.html?case=telegram&lang=${s.lang}&as=ios`, `document.querySelector("h1, main")`, SUB);
      s.mark("top");
      await s.wait(1.0);
      await bring(s, findText(L("telegramT")), 0.08, 1.2);
      s.mark("tg");
      await s.wait(2.2);
      await bring(s, findText(L("devicesT")), 0.12, 0.9);
      s.mark("devices");
      await s.wait(1.6);
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
      const card = `(() => { let e = [...document.querySelectorAll("main *, body *")].find((x) => x.children.length === 0 && x.textContent.trim() === ${q(name)}); while (e && e.getBoundingClientRect().height < 120) e = e.parentElement; return e; })()`;
      const r = await s.rect(card);
      const y = (await s.evaluate("window.scrollY")) + r.y + r.h - s.vp.height * 0.82;
      await s.scrollTo(y, 1.6);
      await s.rectMark("sg", card);
      s.mark("sg");
      await s.wait(2.0);
    },
  },
  // Overview → Add node → install sg1 over SSH → open it
  "hero-admin": {
    cursorFrom: { x: 1180, y: 780 },
    async run(s) {
      const w = world(s.lang);
      const T = (k, v) => t(s.lang, k, v);
      s.mock(w.table);
      await s.open("/", `document.querySelector("main h1")`);
      s.mark("overview");
      await s.rectMark("grid", `document.querySelector("main a[href*='/nodes/']").closest(".grid")`);
      await s.rectMark("addBtn", byText("main button", T("node.add.button")));
      await s.wait(1.2);
      await s.click(byText("main button", T("node.add.button")), 1.1);
      await until(s, byText("[role=dialog] button", T("node.add.sshInstall")));
      await s.wait(0.5);
      s.mark("modal");
      await s.rectMark("dlgChoose", dialog);
      await s.click(byText("[role=dialog] button", T("node.add.sshInstall")), 0.8);
      await until(s, inDialog("input"));
      await s.wait(0.4);
      s.mark("host");
      await s.rectMark("dlgHost", dialog);
      await s.click(inDialog("input:not([type=number])"), 0.6, 0.3);
      await s.type("203.0.113.50", 13);
      await s.wait(0.25);
      s.hold("ProvisioningService/GetSSHFingerprint", 14);
      await s.click(byText("[role=dialog] button", T("node.ssh.checkServer")), 0.6);
      await until(s, inDialog("input[type=checkbox]"));
      await s.wait(0.5);
      s.mark("key");
      await s.rectMark("dlgKey", dialog);
      await s.click(inDialog("input[type=checkbox]"), 0.55);
      s.mark("name");
      await s.click(field(T("node.add.name")), 0.45, 0.4);
      await s.selectAll();
      await s.type("sg1", 10);
      await s.click(`${dialog}.querySelector('[aria-label=${q(T("node.add.country"))}]')`, 0.45, 0.4);
      await until(s, `document.querySelector("[role=listbox]")`, 3);
      await s.wait(0.2);
      await s.press("SG", 0.1);
      await s.wait(0.3);
      await s.key("Enter");
      await s.wait(0.15);
      s.mark("password");
      await s.click(inDialog("input[type=password]"), 0.55, 0.3);
      await s.type("demo-only-pass", 20);
      await s.wait(0.1);
      s.hold("ProvisioningService/CheckSSH", 16);
      await s.click(byText("[role=dialog] button", T("node.ssh.continue")), 0.7);
      await until(s, byText("[role=dialog] button", T("node.ssh.startInstall")));
      await s.wait(0.6);
      s.mark("preflight");
      await s.rectMark("dlgCheck", dialog);
      await s.wait(1.0);
      await s.click(inDialog("input[type=password]"), 0.7, 0.3);
      await s.type("demo-only-pass", 18);
      await s.click(inDialog("input[type=checkbox]"), 0.6);
      await s.wait(0.2);
      await s.click(byText("[role=dialog] button", T("node.ssh.startInstall")), 0.6);
      s.mark("install");
      await until(s, byText("[role=dialog] button", T("node.ssh.openNode")), 40);
      s.mark("installed");
      await s.rectMark("dlgDone", dialog);
      await s.wait(1.0);
      await s.click(byText("[role=dialog] button", T("node.ssh.openNode")), 0.8);
      await until(s, `document.querySelector("main h1")?.textContent.includes("sg1")`);
      s.mark("node");
      await s.rectMark("nodeHead", `document.querySelector("main h1").closest("div").parentElement.parentElement`);
      await s.wait(1.2);
      // the banner's button opens the Profiles tab, its empty state the dialog
      await s.click(byText("main button", T("node.profiles.add")), 0.8);
      await s.wait(0.6);
      s.mark("profilesTab");
      await s.rectMark("empty", byText("main button", T("node.profiles.add")) + ".closest('div').parentElement");
      await s.click(byText("main button", T("node.profiles.add")), 0.7);
      await until(s, byText("[role=dialog] button", T("node.profiles.addDo")));
      await s.wait(0.6);
      s.mark("addProfile");
      await s.rectMark("dlgAdd", dialog);
      await s.wait(0.8);
      await s.click(byText("[role=dialog] button", T("node.profiles.addDo")), 0.7);
      // the profile's row in the page (not in the dialog): the moment it is on the node
      const row = `[...document.querySelectorAll("main *")].find((e) => e.children.length === 0 && e.textContent.trim() === "VLESS · Reality" && !e.closest("[role=dialog]"))?.closest("li, article, section, [class*=rounded]")`;
      await until(s, row);
      s.mark("profileAdded");
      await s.rectMark("inbound", row);
      // the header's problem count follows the Overview's 10 s poll; the refetch on focus (an alt-tab) brings it now
      await s.wait(0.3);
      await s.evaluate(`document.dispatchEvent(new Event("visibilitychange"))`);
      await until(s, findText(T("health.ok")));
      s.mark("healthy");
      await s.wait(1.4);
    },
  },
};
