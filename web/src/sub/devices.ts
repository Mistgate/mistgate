import { copyButton, label } from "./amnezia";
import { h, type Kid } from "./dom";
import { icon, type IconName } from "./icons";
import { atDeviceLimit, deviceIcon, fmtAgo, isShared, keyAppName, keyAvailability, otherDevices, platformWord, selfServe, ways } from "./logic";
import type { Ctx } from "./state";
import { dot, note, tile } from "./ui";
import type { AwgDevice, Device } from "./types";

// "My devices": one card. The shared row of the link apps (they take one slot together), then the key devices (one key each,
// one main action and a "more" menu), then "add a device": one button for both ways (add.ts asks which). A subscription that is
// not active still lists the devices: they can be renamed and removed, no key is shown or issued.

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

// ---- rows of the link apps ----

function linkRow(c: Ctx, x: Device | null, app: string): HTMLElement {
  const { d, a, t } = c;
  const shared = x === null || isShared(x);
  const word = x ? platformWord(x.platform, t) : "";
  const name = shared ? t.linkApps : x!.model || word || (app ? t.devApp(app) : t.devGeneric);
  return h(
    "div",
    { class: "dev" },
    h(
      "div",
      { class: "dev-top" },
      tile("sky", shared ? "link" : deviceIcon(x!.platform), { size: 36 }),
      h(
        "div",
        { class: "dev-b" },
        h("p", { class: "dev-n" }, name),
        h("p", { class: "dev-m" }, ...(!shared && x!.model && word ? [h("span", null, word), h("span", null, "·")] : []), ...when(c, x?.last_seen_unix ?? 0, x?.online ?? false, t.linkNever, t.fetched, true)),
        shared && h("p", { class: "hint" }, t.linkAppsNote),
      ),
    ),
    // the one action of the row: the link itself, for an app that does not take "add with one tap" (announced: "Скопировано")
    shared && d.user.status === "active" && d.access.happ && d.subscription_url !== "" && h("div", { class: "dev-acts" }, copyButton(a, { text: d.subscription_url, label: t.copyLink, done: t.copiedShort, toast: t.copied, cls: "sec grow", key: "dev-link-copy", after: a.mark, live: true })),
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
  const err = am.error && (am.errorAt === x.id || am.errorAt === `rename:${x.id}`) ? note("bad", "warn", t.err(am.error, am.retryMin), { cls: "dev-x" }) : null;

  if (ask) {
    const removing = ask.kind === "remove";
    return h(
      "div",
      { class: `dev${removing ? " danger" : ""}`, id: `amz-row-${x.id}`, role: "alertdialog", "aria-labelledby": `ask-t-${x.id}`, "aria-describedby": `ask-d-${x.id}` },
      h("div", { class: "dev-top" }, tile(removing ? "rose" : "sand", removing ? "trash" : "refresh", { size: 36 }), h("div", { class: "dev-b" }, h("p", { class: "dev-n", id: `ask-t-${x.id}` }, removing ? t.removeT(name) : t.rotateT(name)), h("p", { class: "sm mut", id: `ask-d-${x.id}` }, removing ? t.removeD : t.rotateD(app)))),
      h(
        "div",
        { class: "dev-acts" },
        h("button", { class: "btn sec grow", type: "button", "data-k": `amz-no-${x.id}`, disabled: busy, on: { click: () => a.amz.ask(null) } }, t.awgCancel),
        h("button", { class: `btn ${removing ? "bad" : "pri"} grow`, type: "button", "data-k": `amz-yes-${x.id}`, disabled: busy, on: { click: () => (removing ? a.amz.remove(x.id) : a.amz.rotate(x.id)) } }, busy ? t.awgBusy : removing ? t.removeYes : t.rotateYes),
      ),
      err,
    );
  }

  if (renaming) {
    return h(
      "form",
      {
        class: "dev",
        id: `amz-row-${x.id}`,
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
        tile("mint", ico, { size: 36 }),
        h(
          "div",
          { class: "fld grow" },
          h("label", { for: `ren-${x.id}` }, t.renameT),
          h("input", { class: "inp", id: `ren-${x.id}`, "data-k": `amz-ren-${x.id}`, "data-autofocus": "", type: "text", maxlength: 40, autocomplete: "off", value: renaming.value, on: { input: (e) => a.amz.renameInput((e.target as HTMLInputElement).value) } }),
          h("p", { class: "hint", style: { "margin-top": "-2px" } }, t.renameH(word)),
        ),
      ),
      h(
        "div",
        { class: "dev-acts" },
        h("button", { class: "btn sec grow", type: "button", "data-k": `amz-ren-no-${x.id}`, disabled: busy, on: { click: () => a.amz.renameCancel() } }, t.awgCancel),
        h("button", { class: "btn pri grow", type: "submit", "data-k": `amz-ren-yes-${x.id}`, disabled: busy }, busy ? t.awgBusy : t.save),
      ),
      err,
    );
  }

  const stale = x.stale && !inactive;
  const primary = stale ? "renew" : "show";
  const primaryLabel = stale ? t.newKey : t.showKey;
  const run = () => (stale ? a.amz.renew(x.id) : a.amz.show(x.id));
  const menuOpen = am.menu === x.id;
  const busyPrimary = busy && !am.rename;
  const moreAria = t.moreAria(!inactive);
  const menuItem = (key: string, ic: IconName, text: string, on: () => void, bad = false) =>
    h("button", { class: bad ? "bad" : "", type: "button", role: "menuitem", "data-k": key, on: { click: on } }, icon(ic), text);

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
  const add = (disabled = false, out = hasRows) => h("button", { class: `btn ${out ? "out" : "sec"}`, type: "button", "data-k": "dev-add", disabled, on: { click: () => a.amz.add(true) } }, icon("plus"), t.awgAdd);

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
  // an app with the link: always possible (the limit counts keys; the link's one slot is shared), so the button stays
  if (d.access.happ) {
    const key = keyAvailability(d);
    const hint = key === "limit" ? [t.slotsFull, true] : key === "ok" && free > 0 ? [t.freeSlots(free), false] : null;
    return h("div", { class: "dev" }, add(false, true), hint && h("p", { class: `hint dev-hint${hint[1] ? " warn-t b" : ""}` }, hint[0] as string));
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
  const { d, t } = c;
  const am = d.amnezia;
  const inactive = d.user.status !== "active";
  const keys = am && (d.access.amnezia || inactive) ? am : null;
  const serve = selfServe(d);
  const app = keyAppName(d, null);
  const others = otherDevices(d);
  const rows: Kid[] = [];

  if (inactive) rows.push(h("div", { class: "dev" }, note("", "shield", d.user.status === "disabled" ? t.devOffDisabled : t.devOff, { cls: "grow" })));
  if (others.length > 0) rows.push(...others.map((x) => linkRow(c, x, app)));
  else if (d.access.happ) rows.push(linkRow(c, null, app));
  if (keys) rows.push(...keys.devices.map((x) => keyRow(c, x, serve)));
  const add = keys || d.access.happ ? addRow(c, (keys?.devices.length ?? 0) > 0) : null;
  if (add) rows.push(add);
  if (rows.length === 0 || (inactive && rows.length === 1)) return [];
  const both = ways(d).length > 1;
  return [
    h("div", { class: "shead" }, h("h2", { class: "h2" }, t.devicesT), slots(c), both && d.user.device_limit > 0 && h("p", { class: "hint sec-sub" }, t.slotsHint)),
    h("section", { class: "card", "aria-label": t.devicesAria }, ...rows),
  ];
}