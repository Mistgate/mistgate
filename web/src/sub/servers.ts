import { grab, sheetHead } from "./amnezia";
import { h, type Kid } from "./dom";
import { icon } from "./icons";
import {
  canChooseDns,
  canChooseLinkDns,
  showsLinkDns,
  dnsChoices,
  dnsDefault,
  dnsName,
  dnsPresetOf,
  flagEmoji,
  hasKeys,
  hasLink,
  keyAppName,
  LINK,
  linkAppName,
  linkChoices,
  presetDescription,
  presetName,
  serverName,
  serversOf,
  serverText,
  showsKeyDns,
  type DnsChoice,
} from "./logic";
import type { Ctx } from "./state";
import { dot, note } from "./ui";
import type { LoadLevel, Platform, ServerEntry } from "./types";

// The servers of the person: one card each (country, working or not, how busy, the ways to reach it, its name in the
// apps, and the DNS of its AmneziaVPN keys). The page does not switch servers: the apps do. Two DNS choices, never mixed up:
// the apps that use the link have one DNS for all servers (a card above the list, named by the apps), and the keys have one
// per server (a row in the server's card, named by the keys, shown only to a person who uses them). Both are chosen in a
// sheet (a window on a computer); what happens after the choice is said where it was made.

const fills: Record<LoadLevel, number> = { low: 1, medium: 2, high: 3 };

function flag(c: Ctx, s: ServerEntry): HTMLElement | null {
  const name = serverName(s, c.s.lang).country;
  if (c.s.amz.here === "windows") return s.country_code ? h("span", { class: "cc", "aria-hidden": "true" }, s.country_code) : null;
  const e = flagEmoji(s.country_code);
  return e ? h("span", { class: "flag", role: "img", "aria-label": c.t.flagOf(name) }, e) : null;
}

/** "In the app": the names the apps show this server under, without repeats. */
const appNamesOf = (s: ServerEntry): string[] => [...new Set((s.connections.some((x) => x.way === "link" && x.app_name) ? s.connections.filter((x) => x.way === "link").map((x) => x.app_name) : s.app_names).filter(Boolean))];

/** A failed save, where it was made; a lost connection offers "Retry". */
function dnsError(c: Ctx, target: string, cls: string): HTMLElement | null {
  const { s, a, t } = c;
  const err = s.dns.error && s.dns.error.server === target ? s.dns.error : null;
  if (!err) return null;
  return h(
    "div",
    { class: `note bad sm ${cls}`, role: "alert", style: { "align-items": "center" } },
    icon("warn", 16),
    h("p", { class: "grow" }, t.dnsErr(err.code, err.retryMin)),
    err.code === "network" && h("button", { class: "tlink", type: "button", "data-k": `dns-retry-${target === LINK ? "link" : target}`, style: { "min-height": "32px" }, on: { click: () => a.dns.retry() } }, t.dnsRetry),
  );
}

const busyMark = (c: Ctx) => h("span", { class: "busy", "aria-live": "polite" }, h("span", { class: "spin" }), c.t.dnsSaving);
const doneNote = (cls: string, [b, rest]: [string, string]) => h("div", { class: `note ok sm ${cls}`, role: "status" }, icon("check", 16), h("p", null, h("b", null, b), " ", rest));

/** The DNS of the server's AmneziaVPN keys: what applies, "Change", and what happens after a change. */
function dnsRow(c: Ctx, s: ServerEntry): Kid[] {
  const { d, s: st, a, t } = c;
  if (!s.dns || !showsKeyDns(d, s)) return [];
  const id = s.dns.effective || dnsDefault(s.dns);
  const preset = dnsPresetOf(d, id);
  if (!preset) return [];
  const busy = st.dns.busy === s.id;
  const choose = canChooseDns(d, s);
  const desc = choose ? "" : presetDescription(preset, st.lang);
  const out: Kid[] = [
    h(
      "div",
      { class: `dns${choose || busy ? "" : " plain"}` },
      icon("key", 16),
      h("span", { class: "k" }, t.dnsKeyK),
      h("span", { class: "v" }, presetName(preset, st.lang), desc && h("small", null, desc)),
      busy
        ? busyMark(c)
        : choose && h("button", { class: "tlink", type: "button", "data-k": `dns-${s.id}`, "aria-label": `${t.dnsChange}: ${t.dnsKeyK} ${serverText(s, st.lang)}`, on: { click: () => a.dns.open(s.id) } }, t.dnsChange),
    ),
    dnsError(c, s.id, "dns-err"),
  ];
  const keys = refreshDevices(c, s);
  if (st.dns.done[s.id] && keys.length === 0) out.push(doneNote("dns-note", t.dnsDoneSaved()));
  if (keys.length > 0) {
    const app = keyAppName(d, null);
    out.push(
      h(
        "div",
        { class: "dns-keys", role: "status" },
        h("div", { class: "row g12", style: { "align-items": "flex-start" } }, h("span", { class: "tile s36 sand", "aria-hidden": "true" }, icon("refresh")), h("div", { class: "stack g4 grow" }, h("p", { class: "b" }, t.dnsRefreshT(keys.length)), h("p", { class: "sm mut", style: { "text-wrap": "pretty" } }, t.dnsRefreshD(app)))),
        h(
          "div",
          { class: "dns-keys-list" },
          ...keys.map((x) =>
            h(
              "div",
              { class: "dns-keys-row" },
              h("span", { class: "tile s36 mint", "aria-hidden": "true" }, icon("key")),
              h("div", { class: "stack grow" }, h("p", { class: "b sm" }, x.label || t.devGeneric), x.platform && h("p", { class: "hint" }, t.platforms[x.platform as keyof typeof t.platforms] ?? "")),
              h("button", { class: "btn sec sm", type: "button", "data-k": `dns-renew-${s.id}-${x.id}`, disabled: st.amz.busy !== "", on: { click: () => a.amz.renew(x.id) } }, st.amz.busy === `renew:${x.id}` ? t.awgBusy : t.dnsRefresh),
            ),
          ),
        ),
      ),
    );
  }
  return out;
}

/** The key devices of this server that still hold the old DNS. */
function refreshDevices(c: Ctx, s: ServerEntry) {
  const ids = new Set(s.dns?.keys_to_refresh ?? []);
  return (c.d.amnezia?.devices ?? []).filter((x) => ids.has(x.id) && x.stale);
}

function card(c: Ctx, s: ServerEntry): HTMLElement {
  const { t, s: st } = c;
  const dns = s.online ? dnsRow(c, s) : [];
  const names = appNamesOf(s);
  const warp = s.connections.some((x) => x.exit === "warp");
  // the twins through relays, one chip per entry country
  const vias = [...new Set(s.connections.filter((x) => x.relay).map((x) => x.via ?? ""))];
  const links = s.connections.filter((x) => x.way === "link");
  const mihomo = links.some((x) => x.mihomo_only);
  const name = serverName(s, st.lang);
  return h(
    "article",
    { class: `card srv${s.online ? "" : " off"}${dns.length === 0 ? " nodns" : ""}` },
    h(
      "div",
      { class: "srv-top" },
      flag(c, s),
      h("h3", { class: "srv-name" }, name.country, name.place && h("span", null, ` · ${name.place}`)),
      s.online ? h("span", { class: "state" }, dot("live"), t.up) : h("span", { class: "state mut" }, dot("off"), t.down),
    ),
    s.online && s.load && h("div", { class: "load" }, h("span", { class: "k" }, t.loadK), h("span", { class: `meter${s.load === "high" ? " hi" : ""}`, "aria-hidden": "true" }, ...[1, 2, 3].map((n) => h("i", { class: n <= fills[s.load!] ? "f" : "" }))), h("b", { class: s.load === "high" ? "warn-t" : "" }, t.loadLevel[s.load])),
    s.online && s.connections.length > 0 && h("div", { class: "chips" }, hasLink(s) && h("span", { class: "chip sky" }, icon("link", 14), t.chipLink), hasKeys(s) && h("span", { class: "chip mint" }, icon("key", 14), t.chipKey), warp && h("span", { class: "chip warp", title: t.exitHint }, icon("exit", 14), t.chipExit), ...vias.map((cc) => h("span", { class: "chip lav", title: t.viaHint(cc) }, icon("next", 14), t.chipVia(cc)))),
    s.online && names.length > 0 && h("div", { class: "srv-app" }, t.inApp, ...names.map((n) => h("span", { class: "mono" }, n))),
    s.online && mihomo && h("p", { class: "sm mut srv-mihomo" }, t.mihomoOnly(links.every((x) => x.mihomo_only))),
    !s.online && h("p", { class: "sm mut" }, t.downS),
    ...dns,
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
  return h("div", { class: `note sm${calm ? "" : " warn"}`, role: "status" }, icon("warn", 16), h("p", null, text));
}

/** The servers whose card has a DNS row for the keys. */
const keyDnsServers = (c: Ctx) => serversOf(c.d).filter((x) => x.online && showsKeyDns(c.d, x));

/** The link app the titles name: the one the page puts first on this device. */
const linkApp = (c: Ctx) => linkAppName(c.d, (c.s.amz.here || null) as Platform | null);

/**
 * The DNS of the apps that use the link, one for all servers: the first thing above the list, named by the apps it reaches. A
 * person who also uses the keys is told in one line that the keys have their own, on each server's card. In the owner's
 * preview it says what applies, without "Change".
 */
function linkDnsCard(c: Ctx): HTMLElement | null {
  const { d, s, a, t } = c;
  if (!d.dns || !showsLinkDns(d)) return null;
  const preset = dnsPresetOf(d, d.dns.link.effective || d.dns.link.default);
  if (!preset) return null;
  const title = t.dnsLinkT(linkApp(c));
  const desc = presetDescription(preset, s.lang);
  return h(
    "section",
    { class: "card dns-link", "aria-labelledby": "dns-link-t" },
    h(
      "div",
      { class: "dns-link-top" },
      h("span", { class: "tile s36 sky", "aria-hidden": "true" }, icon("globe")),
      h(
        "div",
        { class: "stack g6 grow" },
        h("h3", { class: "b", id: "dns-link-t" }, title),
        h(
          "div",
          { class: "dns-link-row" },
          h("p", { class: "dns-link-v grow" }, presetName(preset, s.lang), desc && h("span", { class: "hint" }, desc)),
          s.dns.busy === LINK ? busyMark(c) : canChooseLinkDns(d) && h("button", { class: "tlink", type: "button", "data-k": "dns-link", "aria-label": `${t.dnsChange}: ${title}`, on: { click: () => a.dns.open(LINK) } }, t.dnsChange),
        ),
      ),
    ),
    keyDnsServers(c).length > 0 && h("p", { class: "hint row g8 dns-link-keys" }, icon("key", 14), h("span", null, t.dnsLinkKeys(keyAppName(d, null)))),
    dnsError(c, LINK, "dns-link-err"),
    s.dns.done[LINK] && doneNote("dns-link-note", t.dnsDoneLink(d.dns.refresh_hours)),
  );
}

/**
 * The servers: heading, the DNS of the link apps (the choice, or a remark that they share one DNS while the keys of the cards
 * below have theirs), the cards, the busy note.
 */
export function serversSection(c: Ctx): Kid[] {
  const { d, s, t } = c;
  const all = serversOf(d);
  if (all.length === 0) return [];
  const link = linkDnsCard(c);
  const linkName = !link && d.dns && d.access.happ && keyDnsServers(c).length > 0 ? dnsName(d, d.dns.link.effective, s.lang) : "";
  const remark = linkName ? note("", "info", t.dnsLinkNote(linkName.replace(/\s+—\s+/, ", "), keyAppName(d, null))) : null;
  return [h("div", { class: "shead" }, h("h2", { class: "h2" }, t.serversT), h("p", { class: "hint sec-sub" }, t.serversS)), link, remark, h("div", { class: "srv-grid" }, ...all.map((x) => card(c, x))), busyNote(c, all)].filter((x): x is HTMLElement => !!x);
}

// ---- the DNS choice (a sheet on a phone, a window on a computer) ----

/** The server whose picker is open. */
export const dnsServer = (c: Pick<Ctx, "d" | "s">): ServerEntry | undefined => (c.s.dns.open ? c.d.servers.find((x) => x.id === c.s.dns.open) : undefined);

/** The radio list of a picker and its buttons; `now` is the preset that applies. */
function pickerBody(c: Ctx, choices: DnsChoice[], now: string): HTMLElement[] {
  const { s, a, t } = c;
  return [
    h(
      "div",
      { class: "stack g8", role: "radiogroup", "aria-labelledby": "dlg-t", "aria-describedby": "dd-d" },
      ...choices.map((o) => {
        const value = o.isDefault ? "" : o.id;
        const on = s.dns.pick === value;
        return h(
          "button",
          { class: `opt${on ? " on" : ""}`, type: "button", role: "radio", "aria-checked": on, "data-k": `dns-opt-${value || "default"}`, "data-autofocus": on, on: { click: () => a.dns.pick(value) } },
          h("span", { class: `radio${on ? " on" : ""}` }, on && icon("check", 13)),
          h(
            "span",
            { class: "opt-b" },
            h("span", { class: "row g8", style: { "flex-wrap": "wrap" } }, h("span", { class: "opt-t" }, o.isDefault ? t.dnsDefault(o.name.split(/\s+—\s+/)[0] ?? o.name) : o.name), o.id === now && h("span", { class: "chip sm" }, t.dnsNow)),
            o.description && h("span", { class: "opt-d" }, o.description),
          ),
        );
      }),
    ),
  ];
}

const pickerButtons = (c: Ctx) =>
  h("div", { class: "btnrow dlg-btns" }, h("button", { class: "btn sec", type: "button", "data-k": "dns-cancel", on: { click: () => c.a.dns.open(null) } }, c.t.awgCancel), h("button", { class: "btn pri", type: "button", "data-k": "dns-apply", on: { click: () => c.a.dns.apply() } }, c.t.dnsApply));

/** What the dialog holds for the open DNS picker: the link apps', or the keys' of one server. */
export function dnsModal(c: Ctx): Kid[] {
  const { d, s, a, t } = c;
  if (s.dns.open === LINK) {
    if (!d.dns) return [];
    return [
      grab(),
      sheetHead(t, { tone: "sky", ico: "globe", id: "dlg-t", title: t.dnsLinkT(linkApp(c)), sub: t.dnsLinkSub }, () => a.dns.open(null)),
      h("p", { class: "sm mut", id: "dd-d", style: { "margin-top": "-6px" } }, t.dnsLinkIntro(d.dns.refresh_hours)),
      ...pickerBody(c, linkChoices(d, s.lang), d.dns.link.effective || d.dns.link.default),
      pickerButtons(c),
    ];
  }
  const srv = dnsServer(c);
  if (!srv || !srv.dns) return [];
  const app = keyAppName(d, null);
  // the key devices whose DNS is inside the key: the ones that live on this server's key profiles
  const profiles = new Set(srv.connections.filter((x) => x.way === "key").map((x) => x.profile_id));
  const names = (d.amnezia?.devices ?? []).filter((x) => profiles.size === 0 || profiles.has(x.profile_id)).map((x) => x.label || t.devGeneric);
  const keyNote = hasKeys(srv) && names.length > 0 ? t.dnsKeys(names) : "";
  return [
    grab(),
    sheetHead(t, { tone: "mint", ico: "key", id: "dlg-t", title: t.dnsT, sub: [flag(c, srv), serverText(srv, s.lang)], flagRow: true }, () => a.dns.open(null)),
    h("p", { class: "sm mut", id: "dd-d", style: { "margin-top": "-6px" } }, t.dnsIntro(app)),
    ...pickerBody(c, dnsChoices(d, srv, s.lang), srv.dns.effective || dnsDefault(srv.dns)),
    keyNote && note("", "info", keyNote),
    pickerButtons(c),
  ];
}

export const dnsLabel = (c: Ctx): string => {
  if (c.s.dns.open === LINK) return c.t.dnsLinkT(linkApp(c));
  const srv = dnsServer(c);
  return srv ? `${c.t.dnsT} ${serverText(srv, c.s.lang)}` : c.t.dnsT;
};
