import "./sub-b.css";
import { h, type Kid } from "./dom";
import { icon, type IconName } from "./icons";
import { countryName, deviceAbout, fmtAgo, isShared, keyAppName, linkFull, otherDevices, platformWord, selfServe } from "./logic";
import type { Ctx } from "./state";
import { connectNeeded } from "./status";
import { copyButton } from "./ui";
import type { AwgDevice, Device } from "./types";

// The devices, as the board draws them: the card of the places (how many are taken, the bar, "Add a device" and the link), then
// the list: a tile with the kind of device, the name, one quiet line (what the app said about itself, or what kind of key it is),
// when it last updated (a computer; a phone has it in the quiet line) and "more", which opens the window of that device (the key,
// rename, replace, remove: the page's windows, not this list). The devices of the link apps (each app that reports a device ID is a
// device of its own), the shared place of the apps that report none, the Telegram and TrustTunnel keys (the owner's: nothing to
// press), then the AmneziaVPN keys. A key that needs a new one says so on its row and has its own button. A subscription that is not
// active still lists the devices: they can be renamed and removed, no key is shown or issued. Blocked devices are not listed.

const whyOff = "why-off";

/** The tile of a row: the board's phone, laptop and computer; a router for a key of a device that is none of them. */
function devIcon(platform: string, key = false): IconName {
  if (platform === "ios" || platform === "android") return "ph";
  if (platform === "macos") return "laptop";
  if (platform === "windows" || platform === "linux") return "pc";
  return key ? "router" : "other";
}

/** When the device last updated (the board's .when): "in the network" with a green dot, or the time; a dim dot for one that never did. */
function when(c: Ctx, last: number, online: boolean, never: string, text: (w: string) => string): { text: string; node: Kid[] } {
  const { t, s } = c;
  if (online) return { text: t.online, node: [h("i", { class: "u-dot" }), t.online] };
  if (last) return { text: text(fmtAgo(last, s.lang)), node: [text(fmtAgo(last, s.lang))] };
  return { text: never, node: [h("i", { class: "u-dot off" }), never] };
}

type RowSpec = {
  id: string;
  ico: IconName;
  name: string;
  meta: string;
  when?: ReturnType<typeof when>;
  /** The id the "more" window is opened for (none: nothing to press). */
  more?: boolean;
  busy?: boolean;
  /** A key that needs a new one: the mark (with why, as its title) and, when the page may issue keys, the button. */
  stale?: false | "profile" | "dns";
  /** The page may issue keys (self-service): the stale mark has its button. */
  serve?: boolean;
  error?: HTMLElement | null;
  /** The row is no device ("not connected" of the apps with no device ID under STRICT): a dash tile. */
  quiet?: boolean;
};

/** The error of the last call made for a device (the window may be closed by then), under its row. */
function rowError(c: Ctx, id: string): HTMLElement | null {
  const am = c.s.amz;
  if (!am.error || ![id, `rename:${id}`, `renew:${id}`].includes(am.errorAt)) return null;
  return h("p", { class: "a-err u-dvx", role: "alert" }, icon("warn"), c.t.err(am.error, am.retryMin));
}

/** One row of the list (the board's .u-dv); the error of its last call goes under it. */
function row(c: Ctx, o: RowSpec): Kid[] {
  const { a, t } = c;
  const w = o.when;
  const tile = h("span", { class: "tl", "aria-hidden": "true" }, icon(o.ico, 18));
  const stale = o.stale
    ? h(
        "div",
        { class: "sk" },
        h("span", { class: "u-tag work", title: o.stale === "dns" ? t.staleDns : o.serve ? t.staleD : t.keysByAdmin }, t.awgStale),
        o.serve && h("button", { class: "u-btn pri", type: "button", "data-k": `amz-stale-${o.id}`, disabled: o.busy, "aria-busy": o.busy ? "true" : false, on: { click: () => a.amz.stale(o.id) } }, o.busy ? h("span", { class: "spin" }) : icon("refresh"), o.busy ? t.awgBusy : t.newKey),
      )
    : null;
  const more = o.more
    ? h(
        "button",
        { class: "u-ic", type: "button", "data-k": `amz-more-${o.id}`, disabled: o.busy, "aria-busy": o.busy ? "true" : false, "aria-haspopup": "dialog", "aria-label": t.dvMoreAria(o.name), on: { click: () => a.amz.menu(o.id) } },
        o.busy && !o.stale ? h("span", { class: "spin" }) : icon("dots"),
      )
    : null;
  return [
    h(
      "div",
      { class: `u-dv${o.stale ? " stale" : ""}${o.quiet ? " quiet" : ""}`, id: `amz-row-${o.id}` },
      tile,
      h("div", { class: "b2" }, h("span", { class: "nm" }, h("bdi", null, o.name)), h("span", { class: "mt" }, o.meta, w && h("span", { class: "only-m" }, `${o.meta ? " · " : ""}${w.text}`))),
      w && h("span", { class: "when only-w" }, ...w.node),
      stale,
      more,
    ),
    o.error ?? null,
  ];
}

// ---- the rows ----

/** The Telegram proxy key: the owner issues it, so nothing to rename or remove here; only whether Telegram uses it. */
function telegramRow(c: Ctx, x: Device): Kid[] {
  return row(c, { id: x.id, ico: "send", name: c.t.telegramT, meta: "", when: when(c, x.last_seen_unix, x.online, c.t.tgNever, c.t.tgSeen) });
}

/** The TrustTunnel key: the owner issues and revokes it, so nothing to rename or remove here; only whether an app used it. */
function trustTunnelRow(c: Ctx, x: Device): Kid[] {
  return row(c, { id: x.id, ico: "tunnel", name: c.t.ttKey, meta: "", when: when(c, x.last_seen_unix, x.online, c.t.tgNever, c.t.tgSeen) });
}

/**
 * A device of an app that reports its ID: the name, what the app said about itself ("iOS 18.1 · Happ 3.4.2"), when it last
 * updated; rename and remove are in the window "more" opens.
 */
function linkRow(c: Ctx, x: Device): Kid[] {
  const { s, t } = c;
  const am = s.amz;
  const word = platformWord(x.platform, t);
  const name = x.model || word || t.devGeneric;
  return row(c, {
    id: x.id,
    ico: devIcon(x.platform),
    name,
    meta: deviceAbout(x) || (word && word !== name ? word : ""),
    when: when(c, x.last_seen_unix, x.online, t.linkNever, t.fetchedOne),
    more: x.can_remove || x.can_rename,
    busy: am.busy === x.id || am.busy === `rename:${x.id}`,
    error: rowError(c, x.id),
  });
}

/**
 * The place the apps without a device ID share (Hiddify, v2rayNG: the server cannot tell them apart): they share one place, or
 * (STRICT, a quiet row that is no device) they do not connect. Before any app took the link it is the link's own row, "not
 * connected yet".
 */
function sharedRow(c: Ctx, x: Device | null, strict: boolean): Kid[] {
  const { t } = c;
  const first = !x && !strict;
  const app = x && !strict ? deviceAbout(x) : "";
  const meta = strict ? t.strictNote : x ? [t.sharedNote, app && t.lastApp(app)].filter(Boolean).join(" · ") : "";
  return row(c, {
    id: x?.id ?? "link",
    ico: "link",
    name: first ? t.linkApps : t.sharedT,
    meta,
    when: strict ? undefined : when(c, x?.last_seen_unix ?? 0, x?.online ?? false, t.linkNever, x ? (w) => w : t.fetched),
    quiet: strict && !x,
  });
}

/** The countries of a key's profile, when they are few enough for one line. */
function countriesOf(c: Ctx, x: AwgDevice): string {
  const list = c.d.amnezia?.profiles.find((p) => p.id === x.profile_id)?.countries ?? [];
  return list.length > 0 && list.length <= 2 ? list.map((cc) => countryName(cc, c.s.lang) || cc).join(", ") : "";
}

function keyRow(c: Ctx, x: AwgDevice, serve: boolean): Kid[] {
  const { d, s, t } = c;
  const am = s.amz;
  const inactive = d.user.status !== "active";
  const word = platformWord(x.platform, t);
  const name = x.label || word || t.devGeneric;
  const tail = [countriesOf(c, x), x.version ? `AWG ${x.version}` : ""].filter(Boolean).join(" · ");
  const busy = am.busy === x.id || am.busy === `rename:${x.id}` || am.busy === `renew:${x.id}`;
  return row(c, {
    id: x.id,
    ico: devIcon(x.platform, true),
    name,
    meta: t.keyMeta(keyAppName(d, x.platform as never), tail),
    when: when(c, x.last_handshake_unix, x.online, t.awgNever, t.awgHandshake),
    more: serve,
    busy,
    stale: x.stale && !inactive && x.stale_reason,
    serve,
    error: rowError(c, x.id),
  });
}

/** The card of the places: how many are taken (the figure, the bar), then "Add a device" and the link to copy. */
function places(c: Ctx): HTMLElement {
  const { d, a, t } = c;
  const u = d.user;
  const inactive = u.status !== "active";
  const limit = u.device_limit;
  const free = Math.max(0, limit - u.devices_used);
  const full = limit > 0 && u.devices_used >= limit;
  const left = (wide: boolean) => (limit > 0 ? t.dvTail(wide, free > 0 ? t.devFree(free) : t.devNoFree) : t.dvUnl);
  // every place taken and a new app on the link would be refused: how to free one, calmly (TRACK refuses nobody: nothing)
  const refused = !inactive && linkFull(d);
  const canAdd = connectNeeded(d);
  const copyAble = !inactive && d.access.happ && d.subscription_url !== "";
  const copy = (cls: string, label: string, done: string, key: string) => copyButton(a, { text: d.subscription_url, label, done, toast: t.copied, cls: `u-btn ${cls}`, key, after: a.mark });
  return h(
    "section",
    { class: "u-card u-dvh", "aria-label": t.dvSlotsAria, "aria-describedby": inactive ? whyOff : refused ? "dev-full" : false },
    h(
      "div",
      { class: "tx" },
      h("p", { class: "u-fig" }, t.devUsed(u.devices_used, limit), ...(limit > 0 ? [h("span", { class: "only-w" }, left(true)), h("span", { class: "only-m" }, left(false))] : [h("span", null, left(true))])),
      limit > 0 && h("div", { class: "u-prog", role: "img", "aria-label": t.slotsAria(u.devices_used, limit) }, h("i", { class: full ? "full" : "", style: { width: `${Math.min(100, Math.round((u.devices_used / limit) * 1000) / 10)}%` } })),
      inactive && h("p", { class: "u-hint", id: whyOff }, u.status === "disabled" ? t.devOffDisabled : t.devOff),
      refused && h("p", { class: "u-hint", id: "dev-full" }, h("b", null, t.fullT), " ", t.linkFullD),
    ),
    (canAdd || copyAble) &&
      h(
        "div",
        { class: "u-acts" },
        canAdd && h("button", { class: "u-btn pri", type: "button", "data-k": "dev-add", disabled: inactive, "aria-describedby": inactive ? whyOff : false, on: { click: () => a.amz.add(true) } }, icon("plus"), t.awgAdd),
        copyAble && copy("only-w", t.copyLink, t.copied, "dev-link-copy"),
        copyAble && copy("only-m", t.linkShort, t.copiedShort, "dev-link-copy-m"),
      ),
  );
}

/** The devices: the card of the places and the list. */
export function devicesSection(c: Ctx): Kid[] {
  const { d, s, a, t } = c;
  const am = d.amnezia;
  const inactive = d.user.status !== "active";
  const keys = am && (d.access.amnezia || inactive) ? am : null;
  const serve = selfServe(d);
  const others = otherDevices(d);
  const shared = others.find(isShared) ?? null;
  const strict = d.link_device_limit === "strict";
  // the devices: the one in use first, so a short list never hides it; the shared place, or STRICT's row; the keys
  const own = others.filter((x) => !isShared(x)).sort((x, y) => Number(y.online) - Number(x.online) || y.last_seen_unix - x.last_seen_unix);
  const list: Kid[][] = own.map((x) => (x.kind === "telegram" ? telegramRow(c, x) : x.kind === "trusttunnel" ? trustTunnelRow(c, x) : linkRow(c, x)));
  if (shared || (d.access.happ && (strict || own.length === 0))) list.push(sharedRow(c, shared, strict));
  if (keys) list.push(...keys.devices.map((x) => keyRow(c, x, serve)));
  if (inactive && list.length === 0) return [];
  // a long list folds to three rows, so the page stays short
  const fold = list.length > 3;
  const shown = fold && !s.devicesOpen ? list.slice(0, 3) : list;
  return [
    places(c),
    h(
      "section",
      { class: "u-card u-dvl", "aria-label": t.devicesAria },
      ...shown.flat(),
      list.length === 0 && h("p", { class: "u-empty" }, t.devNone),
      fold &&
        h(
          "div",
          { class: "u-more" },
          h("button", { class: "u-btn quiet", type: "button", "data-k": "dev-fold", "aria-expanded": s.devicesOpen, on: { click: () => a.devicesOpen(!s.devicesOpen) } }, s.devicesOpen ? t.lessDevices : t.moreDevices(list.length - 3), icon("chev")),
        ),
    ),
  ];
}
