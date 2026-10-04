import badge from "../assets/mistgate-badge.svg?raw";
import { h, type Kid } from "./dom";
import { dict, type Dict } from "./i18n";
import { alts, copyButton, downloadButton, keyWay, staleCard, steps, wayHead, type AmzActions, type AmzState, type Tools } from "./amnezia";
import { icon } from "./icons";
import {
  appNames,
  appsFor,
  deviceIcon,
  fmtAgo,
  hero,
  isShared,
  isTelegram,
  otherDevices,
  platformWord,
  platformsOf,
  qrApp,
  safeAddUrl,
  safeUrl,
  stateText,
  ways,
  type Hero,
} from "./logic";
import { qrSvg } from "./qr";
import type { AppEntry, Device, Lang, MgData, Platform } from "./types";

// The user page as DOM. Two layouts from one tree: elements marked only-m / only-w are shown below / from
// 720 px (sub.css); the rest is shared and restyled by the same media query.
//
// Under the hero the page has the two ways to connect, side by side and of equal weight:
// the subscription (one link with every server, for Happ and the like) and the AmneziaVPN key (one per device). Each says
// what it is in one line, shows its own steps, and lists the devices that use it. A user who has one way sees only that one.

export type State = { lang: Lang; platform: Platform | null; qrOpen: boolean; annClosed: boolean; amz: AmzState };
export type Actions = {
  lang(l: Lang): void;
  platform(p: Platform): void;
  qrOpen(open: boolean): void;
  closeAnn(): void;
} & Tools & { amz: AmzActions };

export const brandName = (d: MgData) =>
  d.title.trim() || (d.brand.parts.join("") || "VPN").replace(/^./, (c) => c.toUpperCase());

// The built-in Mistgate badge (the admin's one). An <img> sees no page CSS variables, so the instance accent
// (a validated hex, see logic.ts) is written into the badge, which derives its other tones from it.
const builtin = (accent: string) => badge.replace("var(--mg-accent,#c3b2fd)", accent);

/** The instance uploaded its own logo (otherwise the page wears the built-in badge). */
export const hasCustomLogo = (d: MgData) => /^<svg[\s>]/i.test(d.brand.logo_svg.trim());

/** src for the logo <img> and the favicon: the instance's (server-sanitised) SVG, else the built-in badge. Never inlined as markup. */
export function logoSrc(d: MgData): string {
  let svg = d.brand.logo_svg.trim();
  if (!hasCustomLogo(d)) svg = builtin(d.brand.accent || "#b8acf2");
  else if (!/xmlns\s*=/.test(svg)) svg = svg.replace(/^<svg/i, '<svg xmlns="http://www.w3.org/2000/svg"');
  return "data:image/svg+xml," + encodeURIComponent(svg);
}

const bar = (pct: number, cls = "") => h("div", { class: "bar" }, h("i", { class: cls, style: { width: `${pct}%` } }));
const dot = () => h("i", { class: "dot" });
const chip = (text: string) => h("span", { class: "chip" }, dot(), text);

function mbps(bps: number, lang: Lang): string {
  return new Intl.NumberFormat(lang === "ru" ? "ru-RU" : "en-US", { maximumFractionDigits: 1 }).format(bps / 1_000_000);
}

function serverLoadCard(d: MgData, lang: Lang, t: Dict): HTMLElement | null {
  if (d.server_loads.length === 0) return null;
  const measured = d.server_loads.filter((server) => server.load_percent !== undefined);
  const busiest = measured[0];
  const busiestPercent = busiest?.load_percent ?? 0;
  const alternative = busiestPercent >= 80
    ? measured.filter((server) => server !== busiest && (server.load_percent ?? 100) < 80).sort((a, b) => (a.load_percent ?? 100) - (b.load_percent ?? 100))[0]
    : undefined;
  const notice = busiestPercent >= 80
    ? alternative
      ? t.serverLoadTry(busiest!.name, busiestPercent, alternative.name, alternative.load_percent ?? 0)
      : t.serverLoadBusy
    : null;
  return h(
    "section",
    { class: "card server-loads", "aria-label": t.serverLoadTitle },
    h("div", { class: "server-load-head" }, h("b", null, t.serverLoadTitle), h("p", { class: "mut sm" }, t.serverLoadIntro)),
    h(
      "div",
      { class: "server-load-list" },
      ...d.server_loads.map((server) => {
        const high = server.load_percent !== undefined && server.load_percent >= 80;
        return h(
          "div",
          { class: "server-load-row" },
          h("div", { class: "server-load-top" },
            h("b", null, server.name),
            h("span", { class: `server-load-pct${high ? " high" : server.load_percent === undefined ? " unknown" : ""}` },
              server.load_percent === undefined ? t.serverLoadUnknown : `${server.load_percent}%`, high && h("small", null, t.serverLoadBusy)),
          ),
          server.load_percent !== undefined && h("div", { class: "server-load-bar" }, h("i", { class: high ? "high" : "", style: { width: `${server.load_percent}%` } })),
          h("span", { class: "mut sm" }, t.serverLoadRates(mbps(server.rx_bps, lang), mbps(server.tx_bps, lang), server.capacity_mbps)),
        );
      }),
    ),
    notice && h("p", { class: `server-load-notice${alternative ? "" : " high"}`, role: "status" }, notice),
  );
}

/** The top bar: logo, name and the language switch. Shared by the page and the password form. */
export function pageHeader(d: MgData, lang: Lang, setLang: (l: Lang) => void): HTMLElement {
  const t = dict[lang];
  const wordmark = d.title.trim().toLowerCase() === d.brand.parts.join("").toLowerCase() || !d.title.trim();
  return h(
    "header",
    { class: "hd" },
    h("img", { class: "logo", src: logoSrc(d), alt: "", width: 32, height: 32, draggable: "false" }),
    wordmark && d.brand.parts[0]
      ? h("span", { class: "name wm" }, d.brand.parts[0], h("b", null, d.brand.parts[1]))
      : h("span", { class: "name" }, brandName(d)),
    h(
      "button",
      { class: "lang", type: "button", "data-k": "lang", "aria-label": t.langLabel, on: { click: () => setLang(lang === "ru" ? "en" : "ru") } },
      h("span", { class: lang === "ru" ? "on" : "" }, "RU"),
      h("span", { class: lang === "en" ? "on" : "" }, "EN"),
    ),
  );
}

/** The key the closed announcement is remembered under: a new text shows again. */
export function annKey(text: string): string {
  let n = 5381;
  for (let i = 0; i < text.length; i++) n = ((n * 33) ^ text.charCodeAt(i)) >>> 0;
  return `ann:${n.toString(36)}`;
}

export function view(d: MgData, s: State, a: Actions): HTMLElement {
  const t = dict[s.lang];
  const hr = hero(d, s.lang);
  const active = d.user.status === "active";
  const support = d.options.show_support ? safeUrl(d.support_url) : "";
  const name = brandName(d);
  const hello = d.user.name ? t.hi(d.user.name) : t.hiAnon;
  const why = stateText(d, s.lang, name);

  // the big "message support" button only where something is wrong; an active page has the support card
  const supportBtn = (cls: string) =>
    support && !active ? h("a", { class: `btn pri ${cls}`, href: support, target: "_blank", rel: "noopener noreferrer" }, t.writeSupport) : null;
  const explain = why ? h("p", { class: "why" }, support ? `${why[0]} ${why[1]}` : why[0]) : null;

  const ann =
    d.options.show_announcement &&
    d.announcement &&
    !s.annClosed &&
    h(
      "div",
      { class: "ann", role: "note" },
      h("i", { class: "dot" }),
      h("span", null, d.announcement),
      h("button", { class: "x", type: "button", "data-k": "ann-x", "aria-label": t.close, on: { click: () => a.closeAnn() } }, icon("close")),
    );

  const children: Kid[] = [
    pageHeader(d, s.lang, a.lang),
    ann,
    heroMobile(hr, hello, t, explain, supportBtn("full")),
    heroWeb(hr, hello, t),
    explain && h("div", { class: `note only-w tone-${d.user.status}` }, h("p", null, support ? `${why?.[0]} ${why?.[1]}` : why?.[0]), supportBtn("")),
  ];

  if (active) children.push(serverLoadCard(d, s.lang, t), staleCard({ d, s, a, t }), ...connect(d, s, a, t, support));
  // the support card on an active page; a page with a problem has the button in the hero already
  if (support && active) {
    children.push(
      h(
        "section",
        { class: "card sup only-m" },
        h("div", { class: "supt" }, h("b", null, t.help), h("span", { class: "mut" }, isTelegram(support) ? t.helpTg : t.helpAny)),
        h("a", { class: "btn sec sm", href: support, target: "_blank", rel: "noopener noreferrer" }, t.write),
      ),
    );
  }
  children.push(
    h(
      "footer",
      { class: "foot" },
      h("span", { class: "privacy" }, t.privacy),
      support && active && h("span", { class: "only-w mut" }, t.help),
      support && active && h("a", { class: "flink only-w", href: support, target: "_blank", rel: "noopener noreferrer" }, t.writeWeb(isTelegram(support))),
    ),
  );
  return h("main", { class: "wrap" }, ...children);
}

function heroMobile(hr: Hero, hello: string, t: Dict, explain: HTMLElement | null, btn: HTMLElement | null) {
  return h(
    "section",
    { class: `hero only-m tone-${hr.state}` },
    h("div", { class: "r1" }, h("span", { class: "hello" }, hello), !hr.word && chip(t.chip[hr.state])),
    h(
      "div",
      { class: `bigrow${hr.word ? " word" : ""}` },
      h("span", { class: hr.word ? "big w" : "big mono" }, hr.big),
      h("div", { class: "bigtxt" }, hr.unit && h("b", null, hr.unit), hr.sub && h("span", { class: "mut" }, hr.sub)),
    ),
    hr.showTraffic &&
      h(
        "div",
        { class: "tr" },
        h("div", { class: "trl" }, h("span", { class: "mut" }, t.traffic), h("span", { class: "mono" }, hr.line)),
        hr.pct !== null && bar(hr.pct, hr.state === "limited" ? "warn" : ""),
        hr.caption && h("span", { class: "mut sm" }, hr.caption),
      ),
    explain,
    btn,
  );
}

function heroWeb(hr: Hero, hello: string, t: Dict) {
  return h(
    "section",
    { class: `herow only-w tone-${hr.state}${hr.state === "active" ? " calm" : ""}${hr.showTraffic ? "" : " one"}` },
    h("div", { class: "hw-main" }, h("span", { class: "chipl" }, dot(), t.chipLong[hr.state]), h("span", { class: "hello34" }, hello)),
    h(
      "div",
      { class: "hw-cell" },
      h("span", { class: "eyebrow" }, hr.word ? t.termL : t.leftL),
      h("span", { class: `hw-num${hr.word ? " w" : ""}` }, h("b", { class: hr.word ? "" : "mono" }, hr.big), hr.unit && h("span", null, hr.unit)),
      !hr.word && h("div", { class: "bar slim" }, h("i", { style: { width: `${hr.termPct}%` } })),
      h("span", { class: "mut sm" }, hr.sub || " "),
    ),
    hr.showTraffic &&
      h(
        "div",
        { class: "hw-cell" },
        h("span", { class: "eyebrow" }, t.trafficL),
        h("span", { class: "hw-num" }, h("b", { class: "mono" }, hr.usedN), h("span", null, hr.usedOf)),
        hr.pct !== null ? h("div", { class: "bar slim" }, h("i", { class: hr.state === "limited" ? "warn" : "", style: { width: `${hr.pct}%` } })) : h("div", { class: "bar slim empty" }),
        h("span", { class: "mut sm" }, hr.caption || " "),
      ),
  );
}

function qrBox(value: string, label: string, cls = "") {
  const svg = qrSvg(value, label);
  return svg ? h("div", { class: `qr${cls ? ` ${cls}` : ""}` }, svg) : null;
}

/** The platform the ways show apps for, and the two ways themselves. */
function connect(d: MgData, s: State, a: Actions, t: Dict, support: string): Kid[] {
  const plats = platformsOf(d);
  const platform = s.platform && plats.includes(s.platform) ? s.platform : (plats[0] ?? null);
  const order = ways(d, platform);
  if (order.length === 0 || (plats.length === 0 && !d.amnezia)) {
    return [h("section", { class: "card conn-none" }, h("p", { class: "mut" }, t.noApps))];
  }
  const picker =
    plats.length > 1 &&
    h(
      "div",
      { class: "plats-row" },
      h("span", { class: "plats-l" }, t.yourDevice),
      h(
        "div",
        { class: "plats", role: "group", "aria-label": t.pickDev },
        ...plats.map((p) =>
          h("button", { class: `plat${p === platform ? " on" : ""}`, type: "button", "data-k": `plat-${p}`, "aria-pressed": p === platform, on: { click: () => a.platform(p) } }, t.platforms[p]),
        ),
      ),
    );
  const solo = order.length === 1;
  return [
    (picker || !solo) && h("div", { class: "pick" }, picker, !solo && h("p", { class: "two-ways mut" }, t.twoWays)),
    h("div", { class: `ways${solo ? " solo" : ""}` }, ...order.map((w) => (w === "link" ? linkWay(d, s, a, t, platform, solo) : keyWay({ d, s, a, t }, platform, support)))),
  ];
}

/**
 * The subscription way: install the app, add the subscription (the app's own add link, with "copy the link" right under it
 * for when the link does not open), the hint that every server shows up, the other apps of the platform, the QR code for
 * another device, and what is connected through the link.
 */
function linkWay(d: MgData, s: State, a: Actions, t: Dict, platform: Platform | null, solo: boolean): HTMLElement {
  const apps = appsFor(d, platform, "happ");
  const main = apps[0];
  const copyLink = (cls: string, label = t.copyLink, key?: string) => copyButton(a, { text: d.subscription_url, label, done: t.copiedShort, toast: t.copied, cls, key });
  const items: { title: Kid; body: Kid[] }[] = [];
  if (main) {
    const download = safeUrl(main.download_url);
    const addUrl = safeAddUrl(main.add_url);
    if (download) {
      items.push({
        title: h("span", null, t.stepInstall(main.name), apps.length > 1 && main.recommended && h("span", { class: "badge" }, t.recommended)),
        body: [main.description && h("p", { class: "step-d mut" }, main.description), downloadButton(download, t, "link-get")],
      });
    }
    items.push({
      title: t.stepAddSub,
      body: addUrl
        ? [
            h("a", { class: "btn pri", href: addUrl, "data-k": "link-add" }, icon("plus"), h("span", null, t.addTo(main.name))),
            h("div", { class: "fallback" }, copyLink("tlink", t.noOpen, "link-copy"), h("span", { class: "hint" }, t.pasteHow(main.name))),
          ]
        : [copyLink("pri", t.copyLink, "link-copy"), h("p", { class: "hint" }, t.copyHow(main.name))],
    });
  } else {
    items.push({ title: t.stepAddSub, body: [h("p", { class: "mut step-d" }, t.noAppHere(platformWord(platform ?? "", t) || "")), copyLink("sec", t.copyLink, "link-copy")] });
  }

  const phoneApp = qrApp(d);
  const q = d.options.show_qr && d.subscription_url ? qrBox(d.subscription_url, t.qrHow(phoneApp)) : null;
  const q2 = q && qrBox(d.subscription_url, t.qrHow(phoneApp));
  const devs = otherDevices(d);
  const linkName = main?.name || d.apps.find((x) => x.kind === "happ")?.name || "";
  return h(
    "section",
    { class: `card way${solo ? " solo" : ""}`, "aria-labelledby": "way-link-t", "data-way": "link" },
    wayHead("way-link-t", "link", "sky", t.linkT(appNames(apps).join(", ")), t.linkD),
    h(
      "div",
      { class: "way-body" },
      h(
        "div",
        { class: "way-main" },
        steps(items),
        main && d.server_count > 1 && h("p", { class: "all-servers" }, icon("check"), h("span", null, t.allServers(main.name, d.server_count))),
        apps.length > 1 && alts(apps.slice(1), t, (x) => altActions(x, t, copyLink)),
      ),
      // the QR code is for a phone that is not this device: beside the steps on a computer, folded away on a phone
      q &&
        h(
          "aside",
          { class: "qrside only-w" },
          q,
          h("div", { class: "qrside-t" }, h("b", null, t.qrPhoneT), h("p", { class: "mut" }, t.qrHow(phoneApp)), copyLink("tlink", t.copyLink, "qr-copy")),
        ),
    ),
    q2 &&
      h(
        "details",
        { class: "qrx only-m", open: s.qrOpen, on: { toggle: (e) => a.qrOpen((e.target as HTMLDetailsElement).open) } },
        h("summary", null, icon("qr"), h("span", null, t.qrOther), h("i", { class: "chev" }, "›")),
        h("div", { class: "qrbox" }, q2, h("span", { class: "hint c" }, t.qrHow(phoneApp)), copyLink("tlink", t.copyLink, "qr-copy-m")),
      ),
    devs.length > 0 &&
      h("div", { class: "sub-list" }, h("div", { class: "sub-h" }, h("b", { class: "sub-t" }, t.viaLink)), h("div", { class: "krows" }, ...devs.map((x) => linkRow(x, d, s, t, linkName)))),
  );
}

function altActions(x: AppEntry, t: Dict, copyLink: (cls: string, label?: string, key?: string) => HTMLElement): Kid[] {
  const dl = safeUrl(x.download_url);
  const add = safeAddUrl(x.add_url);
  return [
    dl && downloadButton(dl, t, `alt-get-${x.name}`),
    add ? h("a", { class: "btn sec", href: add }, icon("plus"), h("span", null, t.addTo(x.name))) : copyLink("sec", t.copyLink, `alt-copy-${x.name}`),
    !add && h("p", { class: "hint" }, t.copyHow(x.name)),
  ];
}

/**
 * A device of the link apps. The apps share one device on the server (no platform, no model): the row says so, without
 * guessing which app it is; a device the server knows more about is named by its model or platform.
 */
function linkRow(x: Device, d: MgData, s: State, t: Dict, app: string): HTMLElement {
  const shared = isShared(x);
  const when = x.online ? t.online : x.last_seen_unix ? (shared ? t.fetched(fmtAgo(x.last_seen_unix, s.lang)) : t.awgHandshake(fmtAgo(x.last_seen_unix, s.lang))) : t.awgNever;
  const word = platformWord(x.platform, t);
  const name = shared ? t.linkApps : x.model || word || (app ? t.devApp(app) : t.devGeneric);
  return h(
    "div",
    { class: "krow" },
    h("span", { class: "kico", "aria-hidden": "true" }, icon(shared ? "link" : deviceIcon(x.platform))),
    h(
      "div",
      { class: "ktxt" },
      h("b", null, h("span", { class: "kname" }, name)),
      h("span", { class: x.online ? "meta on" : "meta" }, [!shared && x.model && word, when].filter(Boolean).join(" · ")),
      shared && d.user.device_limit > 0 && h("span", { class: "meta" }, t.linkAppsNote),
    ),
  );
}
