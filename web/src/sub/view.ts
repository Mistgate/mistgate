import badge from "../assets/mistgate-badge.svg?raw";
import { connectCard, moreCard, qrSide } from "./connect";
import { devicesSection } from "./devices";
import { h, type Kid } from "./dom";
import { dict, type Dict } from "./i18n";
import { staleCard } from "./amnezia";
import { icon } from "./icons";
import { fetchedUnix, fmtAgo, hero, isTelegram, safeUrl, stateText, type Hero } from "./logic";
import { serversSection } from "./servers";
import type { Actions, Ctx, State } from "./state";
import { bar, dot } from "./ui";
import type { Lang, MgData, Theme } from "./types";

// The user page as DOM. Two layouts from one tree: elements marked only-m / only-w are shown below / from 720 px
// (sub.css); the rest is shared and restyled by the same media query.
//
// Top to bottom: the status, then (a first visit) the three steps of connecting, the servers, the person's devices, help
// and the footer; a returning visitor has the servers and devices first and the steps folded into "connect one more device".
// A subscription that is not active shows the reason and "write to support", then the devices (they can be removed and renamed).

export type { Actions, State } from "./state";

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

/** The top bar: logo, name and the language switch. Shared by the page and the password form. */
export function pageHeader(d: MgData, lang: Lang, setLang: (l: Lang) => void): HTMLElement {
  const t = dict[lang];
  const wordmark = d.title.trim().toLowerCase() === d.brand.parts.join("").toLowerCase() || !d.title.trim();
  const btn = (l: Lang) =>
    h("button", { class: lang === l ? "on" : "", type: "button", "data-k": `lang-${l}`, "aria-pressed": lang === l, lang: l, on: { click: () => lang !== l && setLang(l) } }, l.toUpperCase());
  return h(
    "header",
    { class: "hd" },
    h("img", { class: "logo", src: logoSrc(d), alt: "", width: 32, height: 32, draggable: "false" }),
    wordmark && d.brand.parts[0]
      ? h("div", { class: "wm" }, d.brand.parts[0], h("b", null, d.brand.parts[1]))
      : h("div", { class: "wm plain" }, brandName(d)),
    h("div", { class: "lang", role: "group", "aria-label": t.langGroup }, btn("ru"), btn("en")),
  );
}

/** The key the closed announcement is remembered under: a new text shows again. */
export function annKey(text: string): string {
  let n = 5381;
  for (let i = 0; i < text.length; i++) n = ((n * 33) ^ text.charCodeAt(i)) >>> 0;
  return `ann:${n.toString(36)}`;
}

const sendBtn = (href: string, label: string, cls = "pri") => h("a", { class: `btn ${cls}`, href, target: "_blank", rel: "noopener noreferrer" }, icon("send"), label);

export function view(d: MgData, s: State, a: Actions): HTMLElement {
  const t = dict[s.lang];
  const active = d.user.status === "active";
  const support = d.options.show_support ? safeUrl(d.support_url) : "";
  const c: Ctx = { d, s, a, t, support };

  const ann =
    d.options.show_announcement &&
    d.announcement &&
    !s.annClosed &&
    h(
      "div",
      { class: "ann", role: "note" },
      h("span", { class: "dot" }),
      h("p", null, d.announcement),
      h("button", { class: "x", type: "button", "data-k": "ann-x", "aria-label": t.hideAnn, on: { click: () => a.closeAnn() } }, icon("close", 16)),
    );

  const children: Kid[] = [pageHeader(d, s.lang, a.lang), ann, ...statusCards(c)];

  if (!active) {
    // a state with a problem: the reason, the way to write, and the devices (they can still be removed and renamed)
    children.push(...devicesSection(c));
  } else {
    const stale = staleCard({ d, s, a, t, support });
    const servers = serversSection(c);
    const devices = devicesSection(c);
    // the help card stands in two places (beside the steps on a computer, at the end on a phone): one element each
    const qr = qrSide(c);
    const hasHelp = support !== "";
    const hasConnect = connectNeeded(d);
    if (stale) children.push(stale);
    if (s.returning) {
      children.push(...servers, ...devices);
      if (hasConnect) {
        children.push(h("div", { class: "cols" }, h("div", { class: "main" }, moreCard(c)), hasHelp && h("aside", { class: "side only-w" }, helpCard(c))));
      }
    } else {
      if (hasConnect) {
        children.push(
          h("div", { class: "shead" }, h("h2", { class: "h2" }, t.connectT), h("p", { class: "hint sec-sub" }, t.connectH)),
          h("div", { class: "cols" }, h("div", { class: "main" }, connectCard(c)), (qr || hasHelp) && h("aside", { class: "side only-w" }, qr, hasHelp && helpCard(c))),
        );
      }
      children.push(...servers, ...devices);
    }
    if (hasHelp) children.push(h("div", { class: "only-m" }, helpCard(c)));
  }
  children.push(footer(c));
  return h("main", { class: "wrap" }, ...children);
}

/** There is something to connect with: a link or a key. */
const connectNeeded = (d: MgData) => d.access.happ || (d.access.amnezia && d.amnezia !== null) || d.apps.length > 0;

function helpCard({ t, support }: Ctx): HTMLElement | null {
  if (!support) return null;
  return h(
    "section",
    { class: "card row g12 help", "aria-label": t.helpT },
    h("span", { class: "tile s36 lav only-m", "aria-hidden": "true" }, icon("chat")),
    h("div", { class: "stack grow" }, h("p", { class: "b" }, t.helpT), h("p", { class: "hint" }, isTelegram(support) ? t.helpTg : t.helpAny)),
    h("a", { class: "btn sec sm", href: support, target: "_blank", rel: "noopener noreferrer" }, t.write),
  );
}

function footer({ s, a, t }: Ctx): HTMLElement {
  const themes: Theme[] = ["auto", "light", "dark"];
  return h(
    "footer",
    { class: "foot" },
    h("p", { class: "priv" }, icon("shield", 16), t.privacy),
    h(
      "div",
      { class: "foot-row" },
      h("span", { class: "lbl" }, t.themeL),
      h(
        "div",
        { class: "seg sm", role: "radiogroup", "aria-label": t.themeL },
        ...themes.map((x) =>
          h("button", { class: s.theme === x ? "on" : "", type: "button", role: "radio", "aria-checked": s.theme === x, "data-k": `theme-${x}`, on: { click: () => a.theme(x) } }, t.theme[x]),
        ),
      ),
    ),
  );
}

// ---- the status ----

const pillOf = (hr: Hero, t: Dict) => {
  const key = hr.soon ? "soon" : hr.state;
  const tone = hr.soon ? "warn" : hr.state === "expired" ? "bad" : hr.state === "limited" ? "warn" : hr.state === "disabled" ? "off" : "";
  const live = hr.state === "active" && !hr.soon;
  return h("span", { class: `pill${tone ? ` ${tone}` : ""}` }, dot(live ? "live" : tone === "off" ? "off" : tone || "ok"), t.chip[key]);
};

const termStat = (hr: Hero, cls = "stat") =>
  h(
    "div",
    { class: cls },
    h("span", { class: "eb" }, hr.termL),
    h("div", { class: "num" }, h("b", { class: hr.soon ? "warn-t" : "" }, hr.big), h("span", null, hr.unit)),
    hr.sub && h("span", { class: "hint", style: { "margin-top": "auto" } }, hr.sub),
  );

function trafficStat(hr: Hero, t: Dict, cls = "stat", headReset = false) {
  const limited = hr.state === "limited";
  const label = t.barAria(hr.usedN, hr.usedOf);
  return h(
    "div",
    { class: `${cls}${hr.low && !limited && cls === "stat" ? " warn" : ""}` },
    headReset
      ? h("div", { class: "stat-h" }, h("span", { class: "eb" }, t.trafficL), hr.reset && h("span", { class: "hint" }, hr.reset))
      : h("span", { class: "eb" }, t.trafficL),
    h("div", { class: "num" }, h("b", { class: hr.low ? "warn-t" : "" }, hr.usedN), h("span", null, hr.usedOf)),
    hr.pct !== null && bar(hr.pct, hr.low, label),
    !headReset && hr.reset && h("span", { class: "hint" }, hr.reset),
  );
}

/** The one sentence under the status card: what happened (the date bold), what to do (only with support). */
function whyText(c: Ctx, hr: Hero): { lead: string; rest: string } | null {
  const { d, s, t, support } = c;
  if (hr.soon) {
    const [a, b] = t.soon(hr.days);
    return { lead: "", rest: support ? `${a} — ${b}.` : `${a}.` };
  }
  const why = stateText(d, s.lang, brandName(d));
  if (!why) return null;
  return { lead: why.head, rest: [why.body, support ? why.call : ""].filter(Boolean).join(" ") };
}

function gotSub(c: Ctx, cls = "row g8 sm"): HTMLElement | null {
  const at = fetchedUnix(c.d);
  if (!at || c.d.user.status !== "active") return null;
  return h("p", { class: `${cls}` }, icon("check", 16), h("span", null, c.t.appGot, " ", h("b", null, fmtAgo(at, c.s.lang))));
}

function whyEl(w: { lead: string; rest: string }): HTMLElement {
  return h("p", { class: "why" }, w.lead && h("b", null, w.lead), w.lead && " ", w.rest);
}

function statusCards(c: Ctx): Kid[] {
  return [heroMobile(c), heroWeb(c)];
}

function heroMobile(c: Ctx): HTMLElement {
  const { d, s, t, support } = c;
  const hr = hero(d, s.lang);
  const active = hr.state === "active";
  const hello = d.user.name ? t.hi(d.user.name) : t.hiAnon;
  const w = whyText(c, hr);
  const problem = !active || hr.soon;
  const tone = hr.soon ? "warn" : hr.state === "expired" ? "bad" : hr.state === "limited" ? "warn" : hr.state === "disabled" ? "off" : "";
  const sup = support ? sendBtn(support, active ? t.write : t.writeSupport) : null;
  const left = hr.left && h("p", { class: "sm row g8", style: { "align-items": "flex-start" } }, h("span", { class: "dot warn", style: { "margin-top": "6px" } }), h("span", null, hr.left));
  const got = gotSub(c);
  return h(
    "section",
    { class: `card hero only-m${tone ? ` ${tone}` : ""}`, "aria-label": t.statusAria },
    problem
      ? h("div", { class: "hero-top stackd" }, pillOf(hr, t), h("h1", { class: "h1" }, hello))
      : h("div", { class: "hero-top" }, h("h1", { class: "h1" }, hello), pillOf(hr, t)),
    hr.showTerm && hr.showTraffic && h("div", { class: "stats" }, termStat(hr), trafficStat(hr, t)),
    hr.state === "limited" && trafficStat(hr, t, "stat", true),
    left,
    got && h("div", { class: "div", style: { margin: "0 -18px" } }),
    got,
    w && whyEl(w),
    problem && sup,
    !active && h("p", { class: "hint row g8" }, icon("info", 16), t.sameInApp),
  );
}

function heroWeb(c: Ctx): HTMLElement {
  const { d, s, t, support } = c;
  const hr = hero(d, s.lang);
  const active = hr.state === "active";
  const hello = d.user.name ? t.hi(d.user.name) : t.hiAnon;
  const w = whyText(c, hr);
  const tone = hr.soon ? "warn" : hr.state === "expired" ? "bad" : hr.state === "limited" ? "warn" : hr.state === "disabled" ? "off" : "";
  const sup = support ? sendBtn(support, active ? t.write : t.writeSupport) : null;
  const got = gotSub(c, "row g8 sm mut");
  const two = !hr.showTraffic;
  const cells: Kid[] = [];
  if (hr.showTerm) cells.push(termStat(hr, "hw-cell"));
  if (hr.showTraffic) {
    const cell = trafficStat(hr, t, "hw-cell", hr.state === "limited");
    if (hr.left) cell.append(h("p", { class: "sm row g8", style: { "align-items": "flex-start" } }, h("span", { class: "dot warn", style: { "margin-top": "6px" } }), h("span", null, hr.left)));
    cells.push(cell);
  }
  if (!active) {
    cells.push(h("div", { class: "hw-cell", style: { gap: "12px" } }, sup, h("p", { class: "hint row g8", style: { "align-items": "flex-start" } }, icon("info", 16), t.sameInApp)));
  }
  return h(
    "section",
    { class: `card hero-w only-w${two ? " two" : ""}${tone ? ` ${tone}` : ""}`, "aria-label": t.statusAria },
    h(
      "div",
      { class: "hw-main" },
      pillOf(hr, t),
      h("h1", { class: "h1" }, hello),
      got && h("p", { class: "row g8 sm mut" }, icon("check", 16), h("span", null, t.appGot, " ", h("b", { style: { color: "var(--text)" } }, fmtAgo(fetchedUnix(d), s.lang)))),
      w && whyEl(w),
      active && hr.soon && sup && h("div", null, sup),
    ),
    ...cells,
  );
}
