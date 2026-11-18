import { brandName, hasCustomLogo, logoSrc } from "./brand";
import { devicesSection } from "./devices";
import { h, type Kid } from "./dom";
import { dict, htmlLang, langNames, langs, langShort, type Dict } from "./i18n";
import { icon, type IconName } from "./icons";
import { safeUrl, serversOf } from "./logic";
import { serversSection } from "./servers";
import { alertCard, announcement, heroCard } from "./status";
import type { Actions, Ctx, State, Tab } from "./state";
import { telegramSection } from "./telegram";
import { trustTunnelSection } from "./trusttunnel";
import type { Lang, MgData, Theme } from "./types";

// The user page as DOM, after the boards. Top to bottom: the bar (logo, name, language, support), the owner's announcement, the
// subscription card, the note under it, the tabs (servers, the Telegram proxies, TrustTunnel, the devices) and the open tab, the
// help card and the footer. Two layouts from one tree: elements marked only-m / only-w are shown below / from 720 px (sub.css);
// the rest is restyled by the same media query. A subscription that is not active shows its reason and the devices (they can be
// removed and renamed), no tabs.

export type { Actions, State } from "./state";
export { brandName, hasCustomLogo, logoSrc };

// The language menu is a native <details>: it opens without script. The page closes it on a click outside and on Esc
// (the focus goes back to its summary); one pair of listeners for the whole page, however often the tree is rebuilt.
let watching = false;
function watchLangMenu() {
  if (watching) return;
  watching = true;
  document.addEventListener("click", (e) => {
    for (const m of document.querySelectorAll<HTMLDetailsElement>("details.u-lang[open]")) if (!m.contains(e.target as Node)) m.open = false;
  });
  document.addEventListener("keydown", (e) => {
    if (e.key !== "Escape") return;
    const m = document.querySelector<HTMLDetailsElement>("details.u-lang[open]");
    if (!m) return;
    m.open = false;
    m.querySelector("summary")?.focus();
  });
}

/** The language menu (the board's .u-lang): a globe, the name of the language (its short code on a phone) and a chevron; the card lists every language by its own name. */
function langMenu(t: Dict, lang: Lang, setLang: (l: Lang) => void): HTMLElement {
  watchLangMenu();
  const menu = h("details", { class: "u-lang" });
  const row = (l: Lang) =>
    h(
      "button",
      { class: lang === l ? "on" : "", type: "button", role: "option", "aria-selected": lang === l, "data-k": `lang-${l}`, lang: htmlLang(l), on: { click: () => (lang !== l ? setLang(l) : (menu.open = false)) } },
      h("span", null, langNames[l]),
      h("span", { class: "cd", "aria-hidden": "true" }, langShort[l]),
      ck(),
    );
  menu.append(
    h("summary", { class: "u-btn", "aria-label": `${t.langGroup}: ${langNames[lang]}`, "data-k": "lang-menu" }, icon("globe"), h("span", { class: "lb", lang: htmlLang(lang) }, langNames[lang]), h("span", { class: "cd", lang: htmlLang(lang) }, langShort[lang]), icon("chev")),
    h("div", { class: "u-menu", role: "listbox", "aria-label": t.langGroup }, ...langs.map(row)),
  );
  return menu;
}

function ck(): SVGSVGElement {
  const i = icon("check");
  i.classList.add("ck");
  return i;
}

/** The top bar (the board's .u-hd): logo, name, the language menu and (when the page has one) the way to the support. Shared by the page and the password form. */
export function pageHeader(d: MgData, lang: Lang, setLang: (l: Lang) => void, support = ""): HTMLElement {
  const t = dict[lang];
  const wordmark = d.title.trim().toLowerCase() === d.brand.parts.join("").toLowerCase() || !d.title.trim();
  return h(
    "header",
    { class: "u-hd" },
    h("img", { class: "logo", src: logoSrc(d), alt: "", width: 28, height: 28, draggable: "false" }),
    wordmark && d.brand.parts[0] ? h("div", { class: "u-wm" }, d.brand.parts[0], h("b", null, d.brand.parts[1])) : h("div", { class: "u-wm plain" }, brandName(d)),
    langMenu(t, lang, setLang),
    support && h("a", { class: "u-btn only-w", href: support, target: "_blank", rel: "noopener noreferrer", "data-k": "support" }, icon("chat"), t.supportBtn),
    support && h("a", { class: "u-ic line only-m", href: support, target: "_blank", rel: "noopener noreferrer", "data-k": "support-m", "aria-label": t.supportBtn }, icon("chat")),
  );
}

/** The key the closed announcement is remembered under: a new text shows again. */
export function annKey(text: string): string {
  let n = 5381;
  for (let i = 0; i < text.length; i++) n = ((n * 33) ^ text.charCodeAt(i)) >>> 0;
  return `ann:${n.toString(36)}`;
}

// ---- the tabs ----

type TabInfo = { id: Tab; label: string; short: string; n: string; nShort: string; scr: boolean };

/** The tabs this person has: the servers, the Telegram proxies and TrustTunnel when they are on, the devices. */
export function tabsOf({ d, t }: Pick<Ctx, "d" | "t">): TabInfo[] {
  const used = d.user.devices_used;
  const limit = d.user.device_limit;
  const list: TabInfo[] = [{ id: "servers", label: t.tabServers, short: t.tabServers, n: String(serversOf(d).length), nShort: String(serversOf(d).length), scr: true }];
  if (d.access.telegram) list.push({ id: "telegram", label: t.tabTelegram, short: t.tabTelegramShort, n: String(d.telegram?.links.length ?? 0), nShort: String(d.telegram?.links.length ?? 0), scr: false });
  if (d.access.trust_tunnel) list.push({ id: "trusttunnel", label: t.tabTrustTunnel, short: t.tabTrustTunnel, n: String(d.trust_tunnel?.links.length ?? 0), nShort: String(d.trust_tunnel?.links.length ?? 0), scr: false });
  list.push({ id: "devices", label: t.tabDevices, short: t.tabDevices, n: limit > 0 ? `${used}/${limit}` : String(used), nShort: String(used), scr: false });
  return list;
}

/** The open tab: the one asked for if this person has it, else the first. */
export const openTab = (c: Pick<Ctx, "d" | "s" | "t">): Tab => (tabsOf(c).some((x) => x.id === c.s.tab) ? c.s.tab : "servers");

function tabs(c: Ctx): HTMLElement {
  const { a, t } = c;
  const on = openTab(c);
  return h(
    "nav",
    { class: "u-tabs", "aria-label": t.tabsAria },
    ...tabsOf(c).map((x) =>
      h(
        "button",
        { class: `u-tab${x.id === on ? " on" : ""}`, type: "button", "aria-pressed": x.id === on, "data-k": `tab-${x.id}`, on: { click: () => a.tab(x.id) } },
        h("span", { class: "only-w" }, x.label),
        h("span", { class: "only-m" }, x.short),
        h("span", { class: "n only-w", ...(x.scr ? { "data-scr": x.n, "data-scr-k": `tab-${x.id}` } : {}) }, x.n),
        h("span", { class: "n only-m" }, x.nShort),
      ),
    ),
  );
}

function panel(c: Ctx): Kid[] {
  switch (openTab(c)) {
    case "telegram":
      return telegramSection(c);
    case "trusttunnel":
      return trustTunnelSection(c);
    case "devices":
      return devicesSection(c);
    default:
      return serversSection(c);
  }
}

// ---- the end of the page ----

/** The help card (the board's "Не нашли ответ" row): the way to the support, when the page has one. */
function help({ t, support }: Ctx): HTMLElement | null {
  if (!support) return null;
  return h(
    "div",
    { class: "u-below" },
    h("section", { class: "u-card u-ask", "aria-label": t.helpT }, h("p", null, t.askNo), h("a", { class: "u-btn", href: support, target: "_blank", rel: "noopener noreferrer", "data-k": "help-write" }, icon("chat"), t.writeSupport)),
  );
}

const themeIcon: Record<Theme, IconName> = { auto: "system", light: "sun", dark: "moon" };

function footer({ s, a, t }: Ctx): HTMLElement {
  const themes: Theme[] = ["auto", "light", "dark"];
  return h(
    "footer",
    { class: "u-ft" },
    h("p", { class: "u-priv" }, icon("shield"), t.privacy),
    h(
      "div",
      { class: "u-theme", role: "radiogroup", "aria-label": t.themeL },
      ...themes.map((x) => h("button", { class: s.theme === x ? "on" : "", type: "button", role: "radio", "aria-checked": s.theme === x, "aria-label": t.themeAria[x], "data-k": `theme-${x}`, on: { click: () => a.theme(x) } }, icon(themeIcon[x]))),
    ),
  );
}

export function view(d: MgData, s: State, a: Actions): HTMLElement {
  const t = dict[s.lang];
  const support = d.options.show_support ? safeUrl(d.support_url) : "";
  const c: Ctx = { d, s, a, t, support };
  const active = d.user.status === "active";
  const children: Kid[] = [pageHeader(d, s.lang, a.lang, support), announcement(c), heroCard(c), alertCard(c), ...(active ? [tabs(c), ...panel(c)] : devicesSection(c)), help(c), footer(c)];
  return h("main", { class: "wrap" }, ...children);
}
