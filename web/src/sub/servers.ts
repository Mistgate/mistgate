import { grab, sheetHead } from "./amnezia";
import { h, type Kid } from "./dom";
import { icon } from "./icons";
import {
  canChooseDns,
  dnsChoices,
  dnsDefault,
  dnsName,
  dnsPresetOf,
  flagEmoji,
  hasKeys,
  hasLink,
  keyAppName,
  describePreset,
  serverName,
  serversOf,
  serverText,
} from "./logic";
import type { Ctx } from "./state";
import { dot, note } from "./ui";
import type { LoadLevel, ServerEntry } from "./types";

// The servers of the person: one card each (country, working or not, how busy, the ways to reach it, its name in the
// apps, and its DNS). The page does not switch servers: the apps do. DNS is chosen per server in a sheet (a window on a
// computer); what happens after the choice is said in the card.

const fills: Record<LoadLevel, number> = { low: 1, medium: 2, high: 3 };

function flag(c: Ctx, s: ServerEntry): HTMLElement | null {
  const name = serverName(s, c.s.lang).country;
  if (c.s.amz.here === "windows") return s.country_code ? h("span", { class: "cc", "aria-hidden": "true" }, s.country_code) : null;
  const e = flagEmoji(s.country_code);
  return e ? h("span", { class: "flag", role: "img", "aria-label": c.t.flagOf(name) }, e) : null;
}

/** "In the app": the names the apps show this server under, without repeats. */
const appNamesOf = (s: ServerEntry): string[] => [...new Set((s.connections.some((x) => x.way === "link" && x.app_name) ? s.connections.filter((x) => x.way === "link").map((x) => x.app_name) : s.app_names).filter(Boolean))];

function dnsRow(c: Ctx, s: ServerEntry): Kid[] {
  const { d, s: st, a, t } = c;
  if (!s.dns) return [];
  const id = s.dns.effective || dnsDefault(s.dns);
  const preset = dnsPresetOf(d, id);
  const name = preset?.name ?? "";
  if (!name) return [];
  const busy = st.dns.busy === s.id;
  const choose = canChooseDns(d, s);
  const desc = !choose && preset ? describePreset(preset.description, st.lang) : "";
  const err = st.dns.error && st.dns.error.server === s.id ? st.dns.error : null;
  const out: Kid[] = [
    h(
      "div",
      { class: `dns${choose || busy ? "" : " plain"}` },
      icon("globe", 16),
      h("span", { class: "k" }, t.dnsK),
      h("span", { class: "v" }, name, desc && h("small", null, desc)),
      busy
        ? h("span", { class: "busy", "aria-live": "polite" }, h("span", { class: "spin" }), t.dnsSaving)
        : choose && h("button", { class: "tlink", type: "button", "data-k": `dns-${s.id}`, "aria-label": `${t.dnsChange}: ${t.dnsK} ${serverText(s, st.lang)}`, on: { click: () => a.dns.open(s.id) } }, t.dnsChange),
    ),
  ];
  if (err) {
    out.push(
      h(
        "div",
        { class: "note bad sm dns-err", role: "alert", style: { "align-items": "center" } },
        icon("warn", 16),
        h("p", { class: "grow" }, t.dnsErr(err.code, err.retryMin)),
        err.code === "network" && h("button", { class: "tlink", type: "button", "data-k": `dns-retry-${s.id}`, style: { "min-height": "32px" }, on: { click: () => a.dns.retry() } }, t.dnsRetry),
      ),
    );
  }
  const keys = refreshDevices(c, s);
  const auto = !!d.dns && d.dns.link.per_server && hasLink(s);
  if (st.dns.done[s.id]) {
    if (auto) {
      const [b, rest] = t.dnsDoneLink(d.dns?.refresh_hours ?? 12);
      if (keys.length === 0) out.push(h("div", { class: "note ok sm dns-note", role: "status" }, icon("check", 16), h("p", null, h("b", null, b), " ", rest)));
    } else if (keys.length === 0) {
      const [b, rest] = t.dnsDoneSaved();
      out.push(h("div", { class: "note ok sm dns-note", role: "status" }, icon("check", 16), h("p", null, h("b", null, b), " ", rest)));
    }
  }
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
              h("button", { class: "btn sec sm", type: "button", "data-k": `amz-stale-${x.id}`, disabled: st.amz.busy !== "", on: { click: () => a.amz.renew(x.id) } }, st.amz.busy === `renew:${x.id}` ? t.awgBusy : t.dnsRefresh),
            ),
          ),
        ),
        auto && h("p", { class: "hint row g8" }, icon("check", 14), t.dnsAuto),
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
    s.online && s.connections.length > 0 && h("div", { class: "chips" }, hasLink(s) && h("span", { class: "chip sky" }, icon("link", 14), t.chipLink), hasKeys(s) && h("span", { class: "chip mint" }, icon("key", 14), t.chipKey), warp && h("span", { class: "chip" }, icon("exit", 14), t.chipExit)),
    s.online && warp && h("p", { class: "hint", style: { "margin-top": "-4px" } }, t.exitHint),
    s.online && names.length > 0 && h("div", { class: "srv-app" }, t.inApp, ...names.map((n) => h("span", { class: "mono" }, n))),
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

/** The servers: heading, the DNS remark (link apps share one DNS), the cards, the busy note. */
export function serversSection(c: Ctx): Kid[] {
  const { d, t } = c;
  const all = serversOf(d);
  if (all.length === 0) return [];
  const linkName = d.dns && !d.dns.link.per_server ? dnsName(d, d.dns.link.effective) : "";
  const remark = linkName && all.some((x) => x.dns) ? note("", "info", t.dnsLinkNote(linkName.replace(/\s+—\s+/, ", "), keyAppName(d, null))) : null;
  return [h("div", { class: "sec" }, h("h2", { class: "h2" }, t.serversT), h("p", { class: "hint sec-sub" }, t.serversS)), remark, h("div", { class: "srv-grid" }, ...all.map((x) => card(c, x))), busyNote(c, all)].filter((x): x is HTMLElement => !!x);
}

// ---- the DNS choice (a sheet on a phone, a window on a computer) ----

/** The server whose picker is open. */
export const dnsServer = (c: Pick<Ctx, "d" | "s">): ServerEntry | undefined => (c.s.dns.open ? c.d.servers.find((x) => x.id === c.s.dns.open) : undefined);

/** What the dialog holds for the open DNS picker. */
export function dnsModal(c: Ctx): Kid[] {
  const { d, s, a, t } = c;
  const srv = dnsServer(c);
  if (!srv || !srv.dns) return [];
  const choices = dnsChoices(d, srv, s.lang);
  const now = srv.dns.effective || dnsDefault(srv.dns);
  const f = flag(c, srv);
  // the key devices whose DNS is inside the key: the ones that live on this server's key profiles
  const profiles = new Set(srv.connections.filter((x) => x.way === "key").map((x) => x.profile_id));
  const names = (d.amnezia?.devices ?? []).filter((x) => profiles.size === 0 || profiles.has(x.profile_id)).map((x) => x.label || t.devGeneric);
  const keyNote = hasKeys(srv) && names.length > 0 ? t.dnsKeys(names, !!d.dns?.link.per_server && hasLink(srv)) : "";
  return [
    grab(),
    sheetHead(t, { tone: "sky", ico: "globe", id: "dlg-t", title: t.dnsT, sub: [f, serverText(srv, s.lang)], flagRow: true }, () => a.dns.open(null)),
    h("p", { class: "sm mut", id: "dd-d", style: { "margin-top": "-6px" } }, t.dnsIntro),
    h(
      "div",
      { class: "stack g8", role: "radiogroup", "aria-labelledby": "dlg-t" },
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
    keyNote && note("", "info", keyNote),
    h("div", { class: "btnrow dlg-btns" }, h("button", { class: "btn sec", type: "button", "data-k": "dns-cancel", on: { click: () => a.dns.open(null) } }, t.awgCancel), h("button", { class: "btn pri", type: "button", "data-k": "dns-apply", on: { click: () => a.dns.apply() } }, t.dnsApply)),
  ];
}

export const dnsLabel = (c: Ctx): string => {
  const srv = dnsServer(c);
  return srv ? `${c.t.dnsT} ${serverText(srv, c.s.lang)}` : c.t.dnsT;
};
