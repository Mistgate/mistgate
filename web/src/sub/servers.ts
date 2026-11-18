import { h, type Kid } from "./dom";
import { icon } from "./icons";
import {
  canChooseDns,
  canChooseLinkDns,
  showsLinkDns,
  dnsDefault,
  dnsPresetOf,
  hasKeys,
  hasLink,
  keyAppName,
  LINK,
  linkAppName,
  countryName,
  presetName,
  serverName,
  serversOf,
  serverText,
  showsKeyDns,
  usesKeys,
} from "./logic";
import type { Ctx } from "./state";
import type { LoadLevel, Platform, ServerEntry } from "./types";

// The servers of the person, as the board draws them: a card with a table (a computer) or a list of rows (a phone). The bar over it
// says whether the servers work, how busy they are, and (for the apps that use the link) which DNS they get, with the way to
// change it. A row: the flag and the name, how busy the server is, the route (through a node in another country, the spare
// WARP exit, or direct), the DNS of its AmneziaVPN keys with a pencil. The legend under the table says what the marks mean.
// The page does not switch servers: the apps do. Two DNS choices, never mixed up: the apps that use the link have one DNS for all
// servers (the bar), the keys have one per server (a row); both are chosen in a window (dlg-dns.ts), where what happens next is said.

const fills: Record<LoadLevel, number> = { low: 1, medium: 2, high: 3 };
const rank: Record<LoadLevel, number> = { low: 0, medium: 1, high: 2 };
/** The countries the boards draw a flag of in CSS (.fl2.de ...); any other gets its two letters (.fl2.cc). */
const drawn = new Set(["de", "nl", "ru", "ee", "pl", "ro", "by"]);

/**
 * The flag of a server (or of a proxy link): the board's .fl2 (a flag in CSS for the countries the boards draw, the two letters of
 * the code for any other, a plain tile for a server with no country). `decor`: the name stands beside it, so it says nothing.
 */
export function flag(c: Ctx, s: Pick<ServerEntry, "country_code" | "place" | "label">, decor = false): HTMLElement {
  const cc = s.country_code.trim().toLowerCase();
  const ok = /^[a-z]{2}$/.test(cc);
  const named = ok && !decor;
  const kind = !ok ? "nc" : drawn.has(cc) ? cc : "cc";
  return h("span", { class: `fl2 ${kind}`, role: named ? "img" : false, "aria-label": named ? c.t.flagOf(serverName(s, c.s.lang).country) : false, "aria-hidden": named ? false : "true" }, kind === "cc" && cc.toUpperCase());
}

/** "In the app": the names the apps show this server under, without repeats. */
const appNamesOf = (s: ServerEntry): string[] => [...new Set((s.connections.some((x) => x.way === "link" && x.app_name) ? s.connections.filter((x) => x.way === "link").map((x) => x.app_name) : s.app_names).filter(Boolean))];

/** The number a server has among the ones of its country ("DE-2"), when it has twins and no place of its own to tell it apart. "" otherwise. */
function numOf(all: ServerEntry[], x: ServerEntry): string {
  const cc = x.country_code.trim().toUpperCase();
  if (!cc || x.place) return "";
  const same = all.filter((y) => y.country_code.trim().toUpperCase() === cc && !y.place);
  if (same.length < 2) return "";
  // the panel names the twins "Germany 2": its number is the server's; else the order in the subscription
  const n = /\s(\d{1,2})$/.exec(x.label.trim());
  return `${cc}-${n ? n[1] : same.indexOf(x) + 1}`;
}

/** How a server is reached, besides the plain way: the countries of the nodes it is entered through, the spare WARP exit. */
function routeOf(x: ServerEntry) {
  const vias = [...new Set(x.connections.filter((k) => k.relay).map((k) => (k.via ?? "").toUpperCase()))];
  return { vias, warp: x.connections.some((k) => k.exit === "warp"), plain: x.connections.length > 0 && !x.connections.some((k) => k.relay || k.exit === "warp") };
}

/** The route marks of a row (the board's .u-tag): "through RU", WARP and, when there is nothing else, "direct" (a phone has no such mark). */
function tags(c: Ctx, x: ServerEntry, phone: boolean): HTMLElement[] {
  const { t } = c;
  const r = routeOf(x);
  return [
    ...r.vias.map((cc) => h("span", { class: "u-tag ru", title: t.viaHint(cc) }, t.chipVia(cc))),
    r.warp && h("span", { class: "u-tag warp", title: t.exitHint }, t.chipExit),
    !phone && r.plain && h("span", { class: "u-tag dir" }, t.dirTag),
  ].filter((k): k is HTMLElement => !!k);
}

/** What the DNS cell of a row says: the preset the server's keys get, or that it is one for all. "" for a server that does not answer. */
function dnsText(c: Ctx, x: ServerEntry): string {
  const { d, s, t } = c;
  if (!x.online) return "";
  const preset = x.dns && showsKeyDns(d, x) ? dnsPresetOf(d, x.dns.effective || dnsDefault(x.dns)) : undefined;
  return preset ? presetName(preset, s.lang) : t.dnsShared;
}

const meter = (level: LoadLevel) => h("span", { class: `u-meter${level === "high" ? " hi" : ""}`, "aria-hidden": "true" }, ...[1, 2, 3].map((n) => h("i", { class: n <= fills[level] ? "f" : "" })));

/** The name cell of a row: the flag, the country, the owner's place, the number among the twins. `inApp` is what the apps call it (a hint). */
function nameCell(c: Ctx, all: ServerEntry[], x: ServerEntry, phone: boolean): HTMLElement {
  const { t, s } = c;
  const n = serverName(x, s.lang);
  const names = appNamesOf(x);
  const num = numOf(all, x);
  return h(
    phone ? "span" : "div",
    { class: "u-name", title: names.length > 0 ? `${t.inApp}: ${names.join(", ")}` : false },
    flag(c, x, phone),
    h("span", null, h("bdi", null, n.country)),
    n.place && h("small", null, h("bdi", null, n.place)),
    num && h("span", { class: "id" }, num),
  );
}

/** The pencil of a row (and the whole row of a phone) opens the DNS window for this server: its keys' DNS can be chosen. */
const editable = (c: Ctx, x: ServerEntry) => x.online && canChooseDns(c.d, x);

function deskRow(c: Ctx, all: ServerEntry[], x: ServerEntry, dnsCol: boolean): HTMLElement {
  const { t, a } = c;
  const label = serverText(x, c.s.lang);
  const cell = x.online
    ? h("div", { class: `u-load${x.load === "high" ? " hi" : ""}` }, ...(x.load ? [meter(x.load), t.loadLevel[x.load]] : [h("span", { class: "u-dim" }, "—")]))
    : h("div", { class: "u-load" }, h("i", { class: "u-dot off" }), t.down);
  return h(
    "div",
    { class: `u-tr only-w${x.online ? "" : " off"}` },
    nameCell(c, all, x, false),
    cell,
    h("div", null, ...(x.online ? tags(c, x, false) : [h("span", { class: "u-dim" }, t.downS)])),
    dnsCol && h("span", { class: "u-dns" }, h("bdi", null, dnsText(c, x))),
    dnsCol &&
      h("span", null, editable(c, x) && h("button", { class: "u-ic", type: "button", "data-k": `dns-${x.id}`, "aria-label": t.dnsEditAria(label), on: { click: () => a.dns.open(x.id) } }, icon("pencil"))),
  );
}

function phoneRow(c: Ctx, all: ServerEntry[], x: ServerEntry): HTMLElement {
  const { t, a } = c;
  const label = serverText(x, c.s.lang);
  const num = numOf(all, x);
  const state = x.online ? [t.up, x.load ? t.srvLoad(t.loadLevel[x.load]) : ""].filter(Boolean).join(t.listSep) : t.down;
  const kids: Kid[] = [nameCell(c, all, x, true), ...(x.online ? tags(c, x, true) : []), x.online ? x.load && meter(x.load) : h("span", { class: "u-dim" }, t.down)];
  const edit = editable(c, x);
  const aria = `${label}${num ? ` ${num}` : ""}: ${state}`;
  if (!edit) return h("div", { class: `u-sr only-m${x.online ? "" : " off"}`, role: "group", "aria-label": aria }, ...kids);
  const chev = icon("next");
  chev.classList.add("chv");
  return h("button", { class: "u-sr only-m", type: "button", "data-k": `dns-m-${x.id}`, "aria-label": `${aria}. ${t.dnsEditAria(label)}`, on: { click: () => a.dns.open(x.id) } }, ...kids, chev);
}

/** The servers whose row has a DNS for the keys. */
const keyDnsServers = (c: Ctx) => serversOf(c.d).filter((x) => x.online && showsKeyDns(c.d, x));

/** The link app the titles name: the one the page puts first on this device. */
const linkApp = (c: Ctx) => linkAppName(c.d, (c.s.amz.here || null) as Platform | null);

/**
 * The DNS of the apps that use the link, one for all servers (the bar's right side: the board's "DNS for all" with "Change for
 * all"). Shown to a person who uses the link apps when the owner offers a choice, or when the keys have DNS of their own on
 * the rows, so the two are told apart. The button is there only when the choice can be made (not in the owner's preview).
 */
function linkDns(c: Ctx): { name: string; can: boolean } | null {
  const { d, s } = c;
  if (!d.dns || !d.access.happ) return null;
  if (!showsLinkDns(d) && keyDnsServers(c).length === 0) return null;
  const preset = dnsPresetOf(d, d.dns.link.effective || d.dns.link.default);
  return preset ? { name: presetName(preset, s.lang), can: canChooseLinkDns(d) } : null;
}

/** The bar over the table: how many servers work and how busy they are; the DNS of the link apps. */
function bar(c: Ctx, all: ServerEntry[]): HTMLElement {
  const { t, a } = c;
  const up = all.filter((x) => x.online);
  const busiest = up.reduce<LoadLevel | null>((m, x) => (x.load && (m === null || rank[x.load] > rank[m]) ? x.load : m), null);
  const tone = up.length === 0 ? " off" : up.length < all.length ? " warn" : "";
  const link = linkDns(c);
  const app = linkApp(c);
  const aria = `${t.dnsChange}: ${t.dnsLinkT(app)}`;
  return h(
    "div",
    { class: "u-bar" },
    h("p", { class: "l" }, h("span", { class: `u-dot${tone}` }), up.length === all.length ? t.srvAllUp : up.length === 0 ? t.srvNoneUp : t.srvSomeUp(up.length, all.length), busiest && h("span", null, `· ${t.srvLoad(t.loadLevel[busiest])}`)),
    link &&
      h(
        "p",
        { class: "r" },
        h("span", { class: "only-w" }, `${t.barLink}: `, h("b", null, h("bdi", null, link.name))),
        h("span", { class: "only-m" }, `${t.barLinkShort}: `, h("b", null, h("bdi", null, link.name))),
        link.can && h("button", { class: "u-btn only-w", type: "button", "data-k": "dns-link", "aria-label": aria, on: { click: () => a.dns.open(LINK) } }, t.dnsAll),
        link.can && h("button", { class: "u-btn quiet only-m", type: "button", "data-k": "dns-link-m", "aria-label": aria, on: { click: () => a.dns.open(LINK) } }, t.dnsChange),
      ),
  );
}

/** Under the list: a server that is very busy suggests the calmest other one, if there is one. */
function busyNote(c: Ctx, all: ServerEntry[]): HTMLElement | null {
  const { t, s } = c;
  const up = all.filter((x) => x.online);
  const busy = up.find((x) => x.load === "high");
  if (!busy) return null;
  const calm = up.find((x) => x.load === "low") ?? up.find((x) => x.load === "medium");
  const text = calm ? t.busyTry(serverName(busy, s.lang).country, serverName(calm, s.lang).country) : t.busyAll(serverName(busy, s.lang).country);
  return h("p", { class: "u-busy", role: "status" }, icon("warn"), text);
}

/** The legend: what the ways and the marks mean, only for what this person's servers have. */
function legend(c: Ctx, all: ServerEntry[]): HTMLElement | null {
  const { d, t, s } = c;
  const live = all.filter((x) => x.online);
  const link = d.access.happ || live.some(hasLink);
  const key = usesKeys(d) && live.some(hasKeys);
  const app = keyAppName(d, null);
  const ways = link && key ? t.ntBoth(app) : link ? t.ntLink : key ? t.ntKey(app) : null;
  const vias = [...new Set(live.flatMap((x) => routeOf(x).vias))];
  const warp = live.some((x) => routeOf(x).warp);
  const mihomo = live.flatMap((x) => x.connections.filter((k) => k.way === "link"));
  const item = (head: Kid, text: string) => h("p", null, h("b", null, head), text);
  const items = [
    ways && item(ways[0], ways[1]),
    vias.length > 0 && item(h("span", { class: "u-tag ru" }, t.chipVia(vias.length === 1 ? vias[0]! : "")), vias.length === 1 && vias[0] === "RU" ? t.ntRu : t.ntVia(vias.filter(Boolean).map((cc) => countryName(cc, s.lang) || cc).join(", "))),
    warp && item(h("span", { class: "u-tag warp" }, t.chipExit), t.ntWarp),
    mihomo.some((k) => k.mihomo_only) && h("p", null, t.mihomoOnly(mihomo.every((k) => k.mihomo_only) && !live.some(hasKeys))),
  ].filter((k): k is HTMLParagraphElement => !!k);
  return items.length > 0 ? h("div", { class: "u-note" }, ...items) : null;
}

/** The servers: one card, a table on a computer and a list on a phone (both are in the tree; sub-b.css shows one). */
export function serversSection(c: Ctx): Kid[] {
  const { d, t } = c;
  const all = serversOf(d);
  if (all.length === 0) return [h("section", { class: "u-card", "aria-label": t.serversT }, h("p", { class: "u-empty" }, t.srvNone))];
  // the DNS of the keys is a column only for a person whose servers have one
  const dnsCol = all.some((x) => showsKeyDns(d, x));
  return [
    h(
      "section",
      { class: `u-card u-tbl${dnsCol ? "" : " k3"}`, "aria-label": t.serversT },
      bar(c, all),
      h("div", { class: "u-tr u-th only-w" }, h("span", null, t.thServer), h("span", null, t.loadK), h("span", null, t.thRoute), dnsCol && h("span", null, t.thDns), dnsCol && h("span", null)),
      ...all.map((x) => deskRow(c, all, x, dnsCol)),
      ...all.map((x) => phoneRow(c, all, x)),
      busyNote(c, all),
      legend(c, all),
    ),
  ];
}
