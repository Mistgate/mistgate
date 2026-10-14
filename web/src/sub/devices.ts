import { copyButton, label } from "./amnezia";
import { h, type Kid } from "./dom";
import { icon, type IconName } from "./icons";
import { atDeviceLimit, deviceAbout, deviceIcon, fmtAgo, isShared, keyAppName, keyAvailability, linkFull, otherDevices, platformWord, selfServe } from "./logic";
import type { Ctx } from "./state";
import { dot, gap, note, tile } from "./ui";
import type { AwgDevice, Device } from "./types";

// "My devices": one card. The devices of the link apps (each app that reports a device ID is a device of its own, with a "more"
// menu to rename or remove it), the shared place of the apps that report none, then the key devices (one key each, one main
// action and a "more" menu), then "add a device": one button for both ways (add.ts asks which). When a new app would be refused
// (ENFORCE, STRICT, every place taken) a hint above the list says how to free a place. A subscription that is not active still
// lists the devices: they can be renamed and removed, no key is shown or issued. Blocked devices are not listed at all.

const sendBtn = (c: Ctx, cls = "sec") => c.support && h("a", { class: `btn ${cls}`, href: c.support, target: "_blank", rel: "noopener noreferrer" }, icon("send"), c.t.write);

/** The slot counter: one segment per slot (up to twelve), and the words. */
function slots(c: Ctx): HTMLElement | null {
  const { d, t } = c;
  const limit = d.user.device_limit;
  if (limit <= 0) return null;
  const used = d.user.devices_used;
  const full = used >= limit;
  return h(
    "span",
    { class: `slots${full ? " full" : ""}`, "aria-label": t.slotsAria(used, limit) },
    limit <= 12 && h("span", { class: "segs" }, ...Array.from({ length: limit }, (_, i) => h("i", { class: i < used ? "f" : "" }))),
    h("span", { class: `only-m${full ? " warn-t b" : ""}` }, t.slots(used, limit)),
    h("span", { class: `only-w${full ? " warn-t b" : ""}` }, t.slotsWord(used, limit)),
  );
}

function when(c: Ctx, last: number, online: boolean, never: string, text: (w: string) => string, both = false): Kid[] {
  const { t, s } = c;
  if (online) return [h("span", { class: "state" }, dot("live"), t.online), both && last ? h("span", null, "·") : null, both && last ? h("span", null, text(fmtAgo(last, s.lang))) : null];
  return last ? [h("span", null, text(fmtAgo(last, s.lang)))] : [h("span", { class: "state" }, dot("off"), never)];
}

// ---- what a row asks: remove it (or replace its key), rename it ----

/** The question of a row, in its place: the safe answer first (the keyboard goes there), the red one second. */
function askRow(c: Ctx, id: string, title: string, desc: Kid[], removing: boolean, busy: boolean, err: Kid): HTMLElement {
  const { a, t } = c;
  return h(
    "div",
    { class: `dev${removing ? " danger" : ""}`, id: `amz-row-${id}`, role: "alertdialog", "aria-labelledby": `ask-t-${id}`, "aria-describedby": `ask-d-${id}` },
    h("div", { class: "dev-top" }, tile(removing ? "rose" : "sand", removing ? "trash" : "refresh", { size: 36 }), h("div", { class: "dev-b" }, h("p", { class: "dev-n", id: `ask-t-${id}` }, title), h("p", { class: "sm mut", id: `ask-d-${id}` }, ...desc))),
    h(
      "div",
      { class: "dev-acts" },
      h("button", { class: "btn sec grow", type: "button", "data-k": `amz-no-${id}`, disabled: busy, on: { click: () => a.amz.ask(null) } }, t.awgCancel),
      h("button", { class: `btn ${removing ? "bad" : "pri"} grow`, type: "button", "data-k": `amz-yes-${id}`, disabled: busy, on: { click: () => (removing ? a.amz.remove(id) : a.amz.rotate(id)) } }, busy ? t.awgBusy : removing ? t.removeYes : t.rotateYes),
    ),
    err,
  );
}

/** The name of a device, edited in its row. */
function renameRow(c: Ctx, id: string, tone: string, ico: IconName, word: string, value: string, busy: boolean, err: Kid): HTMLElement {
  const { a, t } = c;
  return h(
    "form",
    {
      class: "dev",
      id: `amz-row-${id}`,
      on: {
        submit: (e) => {
          e.preventDefault();
          a.amz.renameSave();
        },
      },
    },
    h(
      "div",
      { class: "dev-top" },
      tile(tone, ico, { size: 36 }),
      h(
        "div",
        { class: "fld grow" },
        h("label", { for: `ren-${id}` }, t.renameT),
        h("input", { class: "inp", id: `ren-${id}`, "data-k": `amz-ren-${id}`, "data-autofocus": "", type: "text", maxlength: 40, autocomplete: "off", value, on: { input: (e) => a.amz.renameInput((e.target as HTMLInputElement).value) } }),
        h("p", { class: "hint", style: { "margin-top": "-2px" } }, t.renameH(word)),
      ),
    ),
    h(
      "div",
      { class: "dev-acts" },
      h("button", { class: "btn sec grow", type: "button", "data-k": `amz-ren-no-${id}`, disabled: busy, on: { click: () => a.amz.renameCancel() } }, t.awgCancel),
      h("button", { class: "btn pri grow", type: "submit", "data-k": `amz-ren-yes-${id}`, disabled: busy }, busy ? t.awgBusy : t.save),
    ),
    err,
  );
}

/** The error of the last call made for a device, under its row. */
function rowError(c: Ctx, id: string): HTMLElement | null {
  const am = c.s.amz;
  return am.error && (am.errorAt === id || am.errorAt === `rename:${id}`) ? note("bad", "warn", c.t.err(am.error, am.retryMin), { cls: "dev-x" }) : null;
}

const menuItem = (key: string, ic: IconName, text: string, on: () => void, bad = false) => h("button", { class: bad ? "bad" : "", type: "button", role: "menuitem", "data-k": key, on: { click: on } }, icon(ic), text);

// ---- rows of the link apps ----

/**
 * A device of an app that reports its ID: the name and one quiet line (online, or when it last updated); rename and remove in
 * "more". What the app said about itself ("iOS 18.1 · Happ 3.4.2") stands in the question, where two phones must be told apart.
 */
function linkRow(c: Ctx, x: Device): HTMLElement {
  const { s, a, t } = c;
  const am = s.amz;
  const word = platformWord(x.platform, t);
  const name = x.model || word || t.devGeneric;
  const ico = deviceIcon(x.platform);
  const busy = am.busy === x.id || am.busy === `rename:${x.id}`;
  const err = rowError(c, x.id);
  if (am.confirm?.id === x.id) {
    const about = deviceAbout(x);
    return askRow(c, x.id, t.removeT(name), about ? [h("span", { class: "dev-about" }, about), h("br", null), t.removeLinkD] : [t.removeLinkD], true, busy, err);
  }
  if (am.rename?.id === x.id) return renameRow(c, x.id, "sky", ico, word, am.rename.value, busy, err);

  const menuOpen = am.menu === x.id;
  const moreAria = t.moreAria(false);
  return h(
    "div",
    { class: "dev", id: `amz-row-${x.id}` },
    h(
      "div",
      { class: "dev-top" },
      tile("sky", ico, { size: 36 }),
      h("div", { class: "dev-b" }, h("p", { class: "dev-n", id: `dev-n-${x.id}` }, name), h("p", { class: "dev-m" }, ...when(c, x.last_seen_unix, x.online, t.linkNever, t.fetchedOne))),
      (x.can_remove || x.can_rename) &&
        h(
          "div",
          { class: "dev-more" },
          h("button", { class: "ibtn", type: "button", "data-k": `amz-more-${x.id}`, disabled: busy, "aria-haspopup": "menu", "aria-expanded": menuOpen, "aria-label": moreAria, "aria-describedby": `dev-n-${x.id}`, on: { click: () => a.amz.menu(menuOpen ? null : x.id) } }, busy ? h("span", { class: "spin" }) : icon("dots")),
          menuOpen &&
            h(
              "div",
              { class: "pop menu", role: "menu", "aria-label": moreAria },
              x.can_rename && menuItem(`amz-m-ren-${x.id}`, "pencil", t.rename, () => a.amz.renameStart(x.id)),
              x.can_rename && x.can_remove && h("div", { class: "sep", role: "separator" }),
              x.can_remove && menuItem(`amz-m-del-${x.id}`, "trash", t.removeKey, () => a.amz.ask({ id: x.id, kind: "remove" }), true),
            ),
        ),
    ),
    err,
  );
}

/**
 * The place the apps without a device ID share (Hiddify, v2rayNG: the server cannot tell them apart), in one short line: they
 * share one place, or (STRICT, a quiet row that is no device) they do not connect. Before any app took the link it is the link's
 * own row, "not connected yet".
 */
function sharedRow(c: Ctx, x: Device | null, strict: boolean): HTMLElement {
  const { t } = c;
  const first = !x && !strict;
  // one short line: the time alone, the place's words say what it is
  const line = strict ? [h("span", null, t.strictNote)] : x ? [h("span", null, t.sharedNote), h("span", { "aria-hidden": "true" }, "·"), ...when(c, x.last_seen_unix, x.online, t.linkNever, (w) => w)] : when(c, 0, false, t.linkNever, t.fetched);
  return h(
    "div",
    { class: "dev" },
    h("div", { class: "dev-top" }, tile(strict && !x ? "dash" : "sky", "link", { size: 36 }), h("div", { class: "dev-b" }, h("p", { class: "dev-n" }, first ? t.linkApps : t.sharedT), h("p", { class: "dev-m" }, ...line))),
  );
}

// ---- the key devices ----

const whyOff = "why-off";

function keyRow(c: Ctx, x: AwgDevice, serve: boolean): HTMLElement {
  const { d, s, a, t } = c;
  const am = s.amz;
  const name = label(x, t);
  const word = platformWord(x.platform, t);
  const inactive = d.user.status !== "active";
  const busy = am.busy === x.id || am.busy === `rename:${x.id}` || am.busy === `renew:${x.id}`;
  const ask = am.confirm?.id === x.id ? am.confirm : null;
  const renaming = am.rename?.id === x.id ? am.rename : null;
  const app = keyAppName(d, x.platform as never);
  const ico: IconName = deviceIcon(x.platform);
  const err = rowError(c, x.id);

  if (ask) {
    const removing = ask.kind === "remove";
    return askRow(c, x.id, removing ? t.removeT(name) : t.rotateT(name), [removing ? t.removeD : t.rotateD(app)], removing, busy, err);
  }
  if (renaming) return renameRow(c, x.id, "mint", ico, word, renaming.value, busy, err);

  const stale = x.stale && !inactive;
  const primary = stale ? "renew" : "show";
  const primaryLabel = stale ? t.newKey : t.showKey;
  const run = () => (stale ? a.amz.renew(x.id) : a.amz.show(x.id));
  const menuOpen = am.menu === x.id;
  const busyPrimary = busy && !am.rename;
  const moreAria = t.moreAria(!inactive);

  // one block for a phone and a computer: the main action, and "more" with the rest
  const acts = serve
    ? h(
        "div",
        { class: "dev-acts" },
        h(
          "button",
          { class: `btn ${stale ? "pri" : "sec"} grow${busyPrimary ? " busy" : ""}`, type: "button", "data-k": `amz-${primary}-${x.id}`, disabled: busy || (inactive && !stale), "aria-busy": busyPrimary, "aria-describedby": inactive ? whyOff : false, on: { click: run } },
          busyPrimary ? h("span", { class: "spin" }) : icon(stale ? "refresh" : "eye"),
          busyPrimary ? t.awgBusy : primaryLabel,
        ),
        h("button", { class: "ibtn big", type: "button", "data-k": `amz-more-${x.id}`, disabled: busy, "aria-haspopup": "menu", "aria-expanded": menuOpen, "aria-label": moreAria, on: { click: () => a.amz.menu(menuOpen ? null : x.id) } }, icon("dots")),
        menuOpen &&
          h(
            "div",
            { class: "pop menu", role: "menu", "aria-label": moreAria },
            menuItem(`amz-m-ren-${x.id}`, "pencil", t.rename, () => a.amz.renameStart(x.id)),
            !inactive && menuItem(`amz-m-rot-${x.id}`, "refresh", t.rotateKey, () => a.amz.ask({ id: x.id, kind: "rotate" })),
            h("div", { class: "sep", role: "separator" }),
            menuItem(`amz-m-del-${x.id}`, "trash", t.removeKey, () => a.amz.ask({ id: x.id, kind: "remove" }), true),
          ),
      )
    : null;

  return h(
    "div",
    { class: "dev", id: `amz-row-${x.id}` },
    h(
      "div",
      { class: "dev-top" },
      tile("mint", ico, { size: 36 }),
      h(
        "div",
        { class: "dev-b" },
        h("p", { class: "dev-n" }, name),
        h("p", { class: "dev-m" }, ...(word && word !== name ? [h("span", null, word), h("span", null, "·")] : []), ...when(c, x.last_handshake_unix, x.online, t.awgNever, t.awgHandshake)),
        stale && h("span", { class: "stag" }, dot("warn"), t.awgStale),
      ),
    ),
    acts,
    err,
  );
}

/** What stands under the rows: add a device, or why that cannot be done. One button for both ways; add.ts asks which. */
function addRow(c: Ctx, hasRows: boolean): HTMLElement | null {
  // hasRows: the person has keys already (the button is then a quiet outline; the first one is the way out of an empty list)
  const { d, a, t } = c;
  const am = d.amnezia;
  const app = keyAppName(d, null);
  const inactive = d.user.status !== "active";
  const top = (tone: string, ic: IconName, title: string, text: string, dash = false) =>
    h("div", { class: "dev-top" }, h("span", { class: `tile s36 ${dash ? "dash" : tone}`, "aria-hidden": "true" }, icon(ic)), h("div", { class: "dev-b" }, h("p", { class: "dev-n" }, title), h("p", { class: "sm mut" }, text)));
  const add = (disabled = false, out = hasRows, cls = "") => h("button", { class: `btn ${out ? "out" : "sec"}${cls}`, type: "button", "data-k": "dev-add", disabled, on: { click: () => a.amz.add(true) } }, icon("plus"), t.awgAdd);

  if (inactive) {
    return h(
      "div",
      { class: "dev" },
      h("button", { class: "btn out", type: "button", disabled: true, "aria-describedby": whyOff }, icon("plus"), t.awgAdd),
      h("p", { class: "hint dev-hint", id: whyOff, style: { "text-align": "center", "margin-top": "-4px" } }, t.whyOff),
    );
  }
  const limit = d.user.device_limit;
  const free = limit > 0 ? limit - d.user.devices_used : 0;
  // an app with the link: "Add a device" is the guided way, and next to it the link itself, one tap: copied once, it goes into
  // every device, and each app takes its own place (or the shared one). On a phone a square with a tooltip, on a computer its words.
  if (d.access.happ) {
    const key = keyAvailability(d);
    const hint = key === "limit" && !linkFull(d) ? [t.slotsFull, true] : key === "ok" && free > 0 ? [t.freeSlots(free), false] : null;
    const link =
      d.subscription_url !== "" &&
      h(
        "span",
        { class: "tipwrap" },
        copyButton(a, { text: d.subscription_url, label: t.copyLink, aria: t.copyLink, textCls: "only-w", done: t.copiedShort, toast: t.copied, cls: "out dev-copy", key: "dev-link-copy", after: a.mark }),
        h("span", { class: "tip only-m", "aria-hidden": "true" }, t.copyLink),
      );
    return h("div", { class: "dev" }, h("div", { class: "dev-acts" }, add(false, true, " grow"), link), hint && h("p", { class: `hint dev-hint${hint[1] ? " warn-t b" : ""}` }, hint[0] as string));
  }
  // keys alone
  if (!am) return null;
  if (!am.self_service) return h("div", { class: "dev" }, top("", "key", t.keysByAdminT, t.keysByAdminD(app), true), sendBtn(c));
  if (!selfServe(d)) return null; // the admin's preview: nothing to press
  if (am.profiles.length === 0) return h("div", { class: "dev" }, top("sand", "clock", t.noProfileT, t.noProfile), sendBtn(c));
  if (atDeviceLimit(d)) {
    return h("div", { class: "dev" }, note("warn", "warn", t.limit(d.user.devices_used, d.user.device_limit, c.support !== ""), { cls: "dev-x" }), h("div", { class: "dev-acts" }, h("button", { class: "btn out grow", type: "button", disabled: true, "data-k": "dev-add" }, icon("plus"), t.awgAdd), sendBtn(c, "sec grow")));
  }
  return hasRows ? h("div", { class: "dev" }, add(), free > 0 && h("p", { class: "hint dev-hint" }, t.freeSlots(free))) : h("div", { class: "dev" }, top("", "key", t.keysT(app), t.keysNone, true), add());
}

/** The "My devices" section. */
export function devicesSection(c: Ctx): Kid[] {
  const { d, s, a, t } = c;
  const am = d.amnezia;
  const inactive = d.user.status !== "active";
  const keys = am && (d.access.amnezia || inactive) ? am : null;
  const serve = selfServe(d);
  const others = otherDevices(d);
  const shared = others.find(isShared) ?? null;
  const strict = d.link_device_limit === "strict";
  const rows: Kid[] = [];

  if (inactive) rows.push(h("div", { class: "dev" }, note("", "shield", d.user.status === "disabled" ? t.devOffDisabled : t.devOff, { cls: "grow" })));
  // the devices: the one in use first, so a short list never hides it; the shared place, or STRICT's row; the keys
  const own = others.filter((x) => !isShared(x)).sort((x, y) => Number(y.online) - Number(x.online) || y.last_seen_unix - x.last_seen_unix);
  const list: HTMLElement[] = own.map((x) => linkRow(c, x));
  if (shared || (d.access.happ && (strict || own.length === 0))) list.push(sharedRow(c, shared, strict));
  if (keys) list.push(...keys.devices.map((x) => keyRow(c, x, serve)));
  // a long list folds to three rows, so the page stays short; never when every place is taken (that is when an old one is looked for)
  const fold = list.length > 4 && !(d.user.device_limit > 0 && d.user.devices_used >= d.user.device_limit);
  rows.push(...(fold && !s.devicesOpen ? list.slice(0, 3) : list));
  if (fold) {
    rows.push(
      h(
        "div",
        { class: "dev dev-fold" },
        h("button", { class: "tlink", type: "button", "data-k": "dev-fold", "aria-expanded": s.devicesOpen, on: { click: () => a.devicesOpen(!s.devicesOpen) } }, s.devicesOpen ? t.lessDevices : t.moreDevices(list.length - 3), icon("chev", 16)),
      ),
    );
  }
  const add = keys || d.access.happ ? addRow(c, (keys?.devices.length ?? 0) > 0) : null;
  if (add) rows.push(add);
  if (rows.length === 0 || (inactive && rows.length === 1)) return [];
  // every place taken and a new app would be refused: how to free one, calmly, under the title (TRACK refuses nobody: nothing)
  const full = linkFull(d);
  return [
    h("div", { class: "shead" }, h("h2", { class: "h2" }, t.devicesT), slots(c), full && h("p", { class: "hint sec-sub", id: "dev-full" }, h("b", null, t.fullT), gap(t.fullT), t.linkFullD)),
    h("section", { class: "card", "aria-label": t.devicesAria, "aria-describedby": full ? "dev-full" : false }, ...rows),
  ];
}